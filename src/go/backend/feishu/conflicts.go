package feishu

import (
	"context"
	"fmt"
	"sort"

	"github.com/rclone/rclone/fs"
)

// Conflict detection.
//
// Feishu gives hard signals for "the remote is not what we left there", which is
// unusual and worth spelling out (PLAN.md §4.2):
//
//   - re-uploading a name always creates a NEW token, there is no in-place update
//   - a folder may hold several files with the same name
//
// So a remote change shows up as either "the recorded token is gone", "a
// different token now carries that name", or "there are now two entries with that
// name". None of it relies on guessing from timestamps, which on Feishu are just
// the upload moment.
//
// Detection lives in the backend because only it sees the raw listing; List
// collapses same-named entries on purpose and would hide whole categories.

// Conflict kinds, matching PLAN.md's signal table.
const (
	// ConflictRemoteDeleted means the recorded token is nowhere in the folder.
	ConflictRemoteDeleted = "remote-deleted"
	// ConflictRemoteReplaced means the name exists but no longer with our token.
	ConflictRemoteReplaced = "remote-replaced"
	// ConflictDuplicateName means the name appears more than once.
	ConflictDuplicateName = "duplicate-name"
)

// conflictItem is one detected conflict.
type conflictItem struct {
	// Dir is the folder, relative to the Fs root. Empty means the root itself.
	Dir string `json:"dir"`
	// Name is the file name.
	Name string `json:"name"`
	// Kind is one of the Conflict* constants.
	Kind string `json:"kind"`
	// Recorded is the token the snapshot holds (empty when it had no record).
	Recorded string `json:"recorded,omitempty"`
	// Found lists every token currently carrying that name.
	Found []string `json:"found,omitempty"`
	// Detail is a human sentence, meant to be shown as-is.
	Detail string `json:"detail"`
}

// conflictReport is the command result.
type conflictReport struct {
	// Available is false when there is no snapshot to compare against.
	//
	// This is reported as a field, not an error: a missing state_file is the
	// DEFAULT configuration, so failing here made every pair without one abort
	// with "conflict detection needs a state_file". Not being able to check is
	// not the same as the check failing.
	Available bool `json:"available"`
	// Conflicts is sorted by Dir then Name.
	Conflicts []conflictItem `json:"conflicts"`
	// Checked is how many files were compared.
	Checked int `json:"checked"`
	// Summary is a one-line outcome.
	Summary string `json:"summary"`
}

// conflictsCommand scans the tree and reports every path where the remote has
// diverged from the snapshot.
func (f *Fs) conflictsCommand(ctx context.Context, dir string) (*conflictReport, error) {
	if f.st == nil {
		return &conflictReport{
			Available: false,
			Summary: "conflict detection is unavailable because this remote has no " +
				"state_file, so there is no baseline to compare the folder against",
		}, nil
	}

	remote, dirs, err := f.scanRemote(ctx, dir)
	if err != nil {
		return nil, err
	}

	report := &conflictReport{Available: true}
	seen := map[string]bool{}

	// Signal 1 and 2: every recorded path must still be that token, exactly once.
	for path, entry := range f.stateEntries() {
		if dir != "" && path != dir && !hasPathPrefix(path, dir) {
			continue
		}
		report.Checked++

		tokens := remote[path]
		switch {
		case len(tokens) == 0:
			report.Conflicts = append(report.Conflicts, conflictItem{
				Dir: parentDir(path), Name: baseName(path),
				Kind: ConflictRemoteDeleted, Recorded: entry.RemoteToken,
				Detail: "the file recorded in the snapshot is no longer in the folder",
			})
		case len(tokens) > 1:
			report.Conflicts = append(report.Conflicts, conflictItem{
				Dir: parentDir(path), Name: baseName(path),
				Kind: ConflictDuplicateName, Recorded: entry.RemoteToken, Found: tokens,
				Detail: fmt.Sprintf("%d files share this name", len(tokens)),
			})
		case tokens[0] != entry.RemoteToken:
			report.Conflicts = append(report.Conflicts, conflictItem{
				Dir: parentDir(path), Name: baseName(path),
				Kind: ConflictRemoteReplaced, Recorded: entry.RemoteToken, Found: tokens,
				Detail: "this name now carries a different file than the one we uploaded",
			})
		}
		seen[path] = true
	}

	// Signal 3: duplicates at paths the snapshot never recorded. Those matter too
	// - they are somebody else's copies, and a sync would silently pick one.
	for path, tokens := range remote {
		if seen[path] || len(tokens) < 2 {
			continue
		}
		if dir != "" && path != dir && !hasPathPrefix(path, dir) {
			continue
		}
		report.Conflicts = append(report.Conflicts, conflictItem{
			Dir: parentDir(path), Name: baseName(path),
			Kind: ConflictDuplicateName, Found: tokens,
			Detail: fmt.Sprintf("%d files share this name and none is in the snapshot",
				len(tokens)),
		})
	}

	sort.SliceStable(report.Conflicts, func(i, j int) bool {
		a, b := report.Conflicts[i], report.Conflicts[j]
		if a.Dir != b.Dir {
			return a.Dir < b.Dir
		}
		return a.Name < b.Name
	})

	switch n := len(report.Conflicts); n {
	case 0:
		report.Summary = fmt.Sprintf("no conflicts across %d checked file(s)", report.Checked)
	default:
		byKind := map[string]int{}
		for _, c := range report.Conflicts {
			byKind[c.Kind]++
		}
		report.Summary = fmt.Sprintf("%d conflict(s): ", n)
		first := true
		for _, kind := range []string{
			ConflictRemoteDeleted, ConflictRemoteReplaced, ConflictDuplicateName,
		} {
			if byKind[kind] > 0 {
				if !first {
					report.Summary += ", "
				}
				report.Summary += fmt.Sprintf("%d %s", byKind[kind], kind)
				first = false
			}
		}
	}
	_ = dirs
	return report, nil
}

// scanRemote walks the tree and returns path -> every token carrying that path.
//
// Directories are skipped; only files participate in conflict checks.
func (f *Fs) scanRemote(ctx context.Context, dir string) (map[string][]string, []string, error) {
	type node struct {
		rel   string
		token string
	}

	out := map[string][]string{}
	var dirs []string

	queue := []node{{rel: normalizePath(dir)}}
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
				return nil, nil, fmt.Errorf("scan %q: %w", cur.rel, err)
			}
			token = t
		}

		entries, err := f.resolver.listFresh(ctx, token)
		if err != nil {
			return nil, nil, fmt.Errorf("scan %q: %w", cur.rel, err)
		}

		for _, e := range entries {
			rel := joinPath(cur.rel, e.Name)
			if e.IsDir {
				dirs = append(dirs, rel)
				queue = append(queue, node{rel: rel, token: e.Token})
				continue
			}
			out[rel] = append(out[rel], e.Token)
		}
	}
	sort.Strings(dirs)
	return out, dirs, nil
}

// stateEntries snapshots the current entries under the lock.
func (f *Fs) stateEntries() map[string]stateEntryView {
	f.stateMu.Lock()
	defer f.stateMu.Unlock()

	out := make(map[string]stateEntryView, len(f.st.Entries))
	for k, v := range f.st.Entries {
		out[k] = stateEntryView{RemoteToken: v.RemoteToken, Rebuilt: v.Rebuilt}
	}
	return out
}

// stateEntryView is the subset of a snapshot entry conflict detection needs.
type stateEntryView struct {
	RemoteToken string
	Rebuilt     bool
}

func baseName(p string) string {
	if i := lastSlash(p); i >= 0 {
		return p[i+1:]
	}
	return p
}

func parentDir(p string) string {
	if i := lastSlash(p); i >= 0 {
		return p[:i]
	}
	return ""
}

func lastSlash(p string) int {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return i
		}
	}
	return -1
}

func hasPathPrefix(path, prefix string) bool {
	prefix = normalizePath(prefix)
	if prefix == "" {
		return true
	}
	return path == prefix || (len(path) > len(prefix) &&
		path[:len(prefix)] == prefix && path[len(prefix)] == '/')
}

var _ = fs.ErrorDirNotFound
