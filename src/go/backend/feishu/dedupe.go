package feishu

import (
	"context"
	"fmt"
	"sort"

	"github.com/rclone/rclone/fs"
)

// Duplicate cleanup.
//
// Why this is needed at all: when the snapshot is lost or corrupt, repair mode
// re-uploads everything and duplicates appear (docs/decisions.md D19). Removing
// them is not something a normal sync can do, because List collapses same-named
// entries on purpose, so rclone never sees the extras.
//
// The safety rule here is the point of the whole command: a duplicate is only
// ever deleted when the snapshot positively identifies a different entry as
// "ours". With no snapshot there is no way to tell our own copy from somebody
// else's, and deletion in Feishu cannot be undone, so the command refuses. No
// heuristics like "keep the newest" -?that would delete a file another machine
// uploaded a minute ago.

// dupEntry is one raw entry with a duplicated name.
type dupEntry struct {
	Name      string `json:"name"`
	Token     string `json:"token"`
	ModTime   int64  `json:"mod_time"`
	CreatedAt int64  `json:"created_at"`
	Keep      bool   `json:"keep"`
	Reasons   string `json:"reason,omitempty"`
}

// dupGroup is every entry sharing one name in one folder.
type dupGroup struct {
	Dir     string     `json:"dir"`
	Name    string     `json:"name"`
	Entries []dupEntry `json:"entries"`
}

// dedupeReport is the command result.
type dedupeReport struct {
	// Action is "list", "delete" or "nothing".
	Action string `json:"action"`
	// DryRun is true when nothing was deleted.
	DryRun bool `json:"dry_run"`
	// Groups holds every same-named group found.
	Groups []dupGroup `json:"groups"`
	// Deleted lists the tokens that were removed.
	Deleted []string `json:"deleted,omitempty"`
	// Skipped lists groups left alone, with the reason.
	Skipped []string `json:"skipped,omitempty"`
	// Summary is a one-line outcome for humans.
	Summary string `json:"summary"`
}

// dedupeListCommand implements `backend dedupe-list`.
func (f *Fs) dedupeListCommand(ctx context.Context, dir string) (*dedupeReport, error) {
	groups, err := f.findDuplicates(ctx, dir)
	if err != nil {
		return nil, err
	}
	rep := &dedupeReport{Action: "list", DryRun: true, Groups: groups}
	rep.Summary = fmt.Sprintf("found %d name(s) with duplicates; nothing was changed", len(groups))
	return rep, nil
}

// dedupeCommand implements `backend dedupe`.
//
// opt["confirm"] must be "true" to actually delete. Without it the command lists
// what it would do, so the destructive path is never one keystroke away.
func (f *Fs) dedupeCommand(ctx context.Context, dir string, opt map[string]string) (*dedupeReport, error) {
	groups, err := f.findDuplicates(ctx, dir)
	if err != nil {
		return nil, err
	}

	confirm := opt["confirm"] == "true"
	dryRun := fs.GetConfig(ctx).DryRun || !confirm

	rep := &dedupeReport{DryRun: dryRun, Groups: groups}
	if !confirm {
		rep.Action = "list"
	}
	if confirm && !fs.GetConfig(ctx).DryRun {
		rep.Action = "delete"
	}

	if f.st == nil {
		// No snapshot: cannot tell our copy from anyone else's.
		rep.Skipped = append(rep.Skipped, "no state_file configured")
		rep.Summary = "refusing to delete: no state_file, so weftsync cannot tell which copy is its own. " +
			"Set state_file on this remote (or in the job file) after the next successful sync, then retry"
		return rep, nil
	}
	if f.repair {
		rep.Skipped = append(rep.Skipped, "state was rebuilt this run")
		rep.Summary = "refusing to delete: the state file was just rebuilt, so it is not evidence of anything yet. " +
			"Run a sync first, then retry"
		return rep, nil
	}

	var deleted []string
	var refused []string

	for gi := range rep.Groups {
		g := &rep.Groups[gi]
		keepIdx := -1

		// Prefer the entry the snapshot recorded. That is the only entry we can
		// prove is our own upload.
		if recorded, ok := f.previousToken(joinPath(dir, g.Name)); ok {
			for i := range g.Entries {
				if g.Entries[i].Token == recorded {
					keepIdx = i
					break
				}
			}
			if keepIdx < 0 {
				refused = append(refused, fmt.Sprintf(
					"%s/%s: the snapshot records token %s, which is not in this folder. "+
						"Not deleting anything here: the recorded file may have been replaced outside weftsync",
					dir, g.Name, recorded))
				continue
			}
			g.Entries[keepIdx].Keep = true
			g.Entries[keepIdx].Reasons = "recorded in the sync state"
		} else {
			refused = append(refused, fmt.Sprintf(
				"%s/%s: the snapshot has no record of this path, so none of the %d copies can be "+
					"proven to be ours",
				dir, g.Name, len(g.Entries)))
			continue
		}

		for i := range g.Entries {
			if i == keepIdx {
				continue
			}
			g.Entries[i].Reasons = "not the recorded copy"
			if dryRun {
				continue
			}
			if err := f.client.deleteFile(ctx, g.Entries[i].Token); err != nil {
				return rep, fmt.Errorf("delete duplicate %q (token %s): %w",
					g.Name, g.Entries[i].Token, err)
			}
			deleted = append(deleted, g.Entries[i].Token)
			// Forget nothing else: the kept entry keeps its snapshot record.
		}
		f.resolver.invalidateAll()
	}

	rep.Deleted = deleted
	rep.Skipped = append(rep.Skipped, refused...)

	switch {
	case dryRun:
		planned := countRemovable(rep.Groups)
		rep.Summary = fmt.Sprintf("would delete %d duplicate(s) across %d name(s); nothing was changed",
			planned, len(rep.Groups))
		if len(refused) > 0 {
			rep.Summary += fmt.Sprintf("; %d name(s) left alone (see skipped)", len(refused))
		}
		if !confirm {
			rep.Summary += ". Re-run with -o confirm=true to actually delete"
		}
	default:
		rep.Summary = fmt.Sprintf("deleted %d duplicate(s)", len(deleted))
		if len(refused) > 0 {
			rep.Summary += fmt.Sprintf("; %d name(s) left alone (see skipped)", len(refused))
		}
	}
	return rep, nil
}

// countRemovable counts non-kept entries.
func countRemovable(groups []dupGroup) int {
	n := 0
	for _, g := range groups {
		for _, e := range g.Entries {
			if !e.Keep {
				n++
			}
		}
	}
	return n
}

// findDuplicates walks the tree under dir and reports every name that appears
// more than once in the same folder.
//
// It uses the raw listing: listCached returns every entry, whereas List collapses
// them, which is exactly why the duplicates are invisible to normal commands.
func (f *Fs) findDuplicates(ctx context.Context, dir string) ([]dupGroup, error) {
	type node struct {
		rel   string // logical path relative to the Fs root
		token string
	}
	var out []dupGroup

	queue := []node{{rel: normalizePath(dir)}}
	if queue[0].rel == "" {
		queue[0].token = "" // resolved later
	}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		token := cur.token
		if token == "" {
			t, err := f.resolver.resolveDir(ctx, cur.rel)
			if err != nil {
				if isNotFound(err) && cur.rel == "" {
					continue
				}
				return nil, fmt.Errorf("list %q: %w", cur.rel, err)
			}
			token = t
		}

		entries, err := f.resolver.listFresh(ctx, token)
		if err != nil {
			return nil, fmt.Errorf("list %q: %w", cur.rel, err)
		}

		byName := map[string][]remoteEntry{}
		order := make([]string, 0, len(entries))
		for _, e := range entries {
			if _, seen := byName[e.Name]; !seen {
				order = append(order, e.Name)
			}
			byName[e.Name] = append(byName[e.Name], e)
		}

		for _, name := range order {
			group := byName[name]
			if group[0].IsDir {
				queue = append(queue, node{rel: joinPath(cur.rel, name), token: group[0].Token})
				continue
			}
			if len(group) < 2 {
				continue
			}
			g := dupGroup{Dir: cur.rel, Name: name}
			for _, e := range group {
				g.Entries = append(g.Entries, dupEntry{
					Name:      e.Name,
					Token:     e.Token,
					ModTime:   e.ModTime,
					CreatedAt: e.CreatedTime,
				})
			}
			sort.SliceStable(g.Entries, func(i, j int) bool {
				return g.Entries[i].CreatedAt < g.Entries[j].CreatedAt
			})
			out = append(out, g)
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir < out[j].Dir
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// command dispatches the backend commands.
func (f *Fs) command(ctx context.Context, name string, arg []string, opt map[string]string) (any, error) {
	dir := ""
	if len(arg) > 0 {
		dir = arg[0]
	}
	switch name {
	case "duplicates":
		return f.dedupeListCommand(ctx, dir)
	case "dedupe":
		return f.dedupeCommand(ctx, dir, opt)
	default:
		return nil, fmt.Errorf("feishu: unknown backend command %q (try \"help\")", name)
	}
}

// commandHelp documents the backend commands.
func commandHelp() []fs.CommandHelp {
	return []fs.CommandHelp{
		{
			Name:  "duplicates",
			Short: "List names that exist more than once in a folder",
			Long: `Reports every name that appears twice or more in the same folder.

Normal listings hide these: the backend collapses same-named entries so that
rclone never sees two objects for one path. This command talks to the raw
listing, so it can show them.

Nothing is deleted by this command.`,
		},
		{
			Name:  "dedupe",
			Short: "Remove duplicate copies that the sync state does not record",
			Long: `Removes extra copies of a name, keeping the one the sync state recorded.

Safety rules, all deliberate:

  - Requires a state_file. Without a snapshot there is no way to tell our own
    copy from one another machine or person uploaded, and deletion in Feishu
    cannot be undone.
  - Requires -o confirm=true to delete anything. Without it the command only
    reports what it would do.
  - Never guesses. If the recorded token is missing from the folder, that name is
    left completely alone and reported.
  - Honours -n/--dry-run as well.

Example:

  rclone backend duplicates remote:path
  rclone backend dedupe remote:path                    # report only
  rclone backend dedupe remote:path -o confirm=true    # delete`,
		},
	}
}
