package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/filter"
	"github.com/rclone/rclone/fs/operations"
)

// Conflict handling.
//
// The detection itself lives in the backend (backend/feishu/conflicts.go) because
// only the backend can see the raw listing: List collapses same-named entries,
// which would hide the duplicate-name signal entirely. The job layer reaches it
// through fs.Features().Command, which every backend exposes, so no shared types
// are needed and a backend without the command simply has no conflicts.
//
// What happens after detection is a policy decision, and that belongs here:
// "should this run change the cloud" is a whole-pair question the backend cannot
// answer from a single call.

// Conflict is one detected divergence, restated for reporting.
type Conflict struct {
	Path     string   `json:"path"`
	Kind     string   `json:"kind"`
	Detail   string   `json:"detail"`
	Recorded string   `json:"recorded,omitempty"`
	Found    []string `json:"found,omitempty"`
	// RecordedMTime and RecordedSize are what the snapshot says the local file
	// looked like at the last sync. A two-way run uses them to tell "the cloud
	// changed" from "both sides changed"; a one-way run ignores them.
	RecordedMTime int64 `json:"recorded_mtime,omitempty"`
	RecordedSize  int64 `json:"recorded_size,omitempty"`
}

// Conflict kinds reported by the feishu backend.
const (
	ConflictKindRemoteDeleted  = "remote-deleted"
	ConflictKindRemoteReplaced = "remote-replaced"
	ConflictKindDuplicateName  = "duplicate-name"
)

// Kinds the job layer synthesises from the extra lists rather than receiving as
// conflicts. They are not problems; they describe the two sides lining up, which
// is what a two-way run needs before it can decide anything.
const (
	// ConflictKindUnchanged marks a path whose recorded cloud token is still in
	// place. Its presence plus a missing local file means "deleted locally".
	ConflictKindUnchanged = "unchanged"
	// ConflictKindRemoteAdded marks a file the folder gained since the last sync.
	ConflictKindRemoteAdded = "remote-added"
)

// ConflictScan is the result of asking the backend about conflicts.
type ConflictScan struct {
	// Available is false when there is no baseline to compare against, which is
	// the normal state when no state_file is configured.
	Available bool
	Summary   string
	Conflicts []Conflict
	// Matched are the paths whose recorded cloud token is unchanged. A two-way
	// run needs these to recognise a local deletion: "the cloud still holds what
	// we recorded, and the local file is gone".
	Matched []Conflict
	// RemoteAdded are files the folder gained since the last sync. Empty on a
	// first run, where every file would qualify.
	RemoteAdded []Conflict
}

// detectConflictsFn is the conflict source RunPair uses.
//
// It is a variable so tests can supply a snapshot without a Feishu backend: the
// local backend has no `conflicts` command, so the interesting half of the
// two-way variant (what to do with a reported conflict) would otherwise be
// untestable. Production code never reassigns it.
var detectConflictsFn = detectConflicts

// detectConflicts asks the backend for conflicts under dir.
//
// Available=false (with no error) means "could not check": either the backend has
// no such command, or it has no snapshot. Neither is a failure. Treating them as
// errors made every pair without a state_file abort the whole run, which is the
// default configuration.
func detectConflicts(ctx context.Context, fdst fs.Fs, dir string) (ConflictScan, error) {
	f := fdst.Features()
	if f == nil || f.Command == nil {
		return ConflictScan{}, nil
	}

	out, err := f.Command(ctx, "conflicts", []string{dir}, nil)
	if err != nil {
		// A backend that does not know this command has no conflicts to report;
		// that is not a failure of the run. Anything else is.
		if errors.Is(err, fs.ErrorCommandNotFound) ||
			strings.Contains(err.Error(), "unknown backend command") {
			return ConflictScan{}, nil
		}
		return ConflictScan{}, fmt.Errorf("detect conflicts: %w", err)
	}

	raw, err := json.Marshal(out)
	if err != nil {
		return ConflictScan{}, fmt.Errorf("detect conflicts: %w", err)
	}
	var parsed struct {
		Available *bool  `json:"available"`
		Summary   string `json:"summary"`
		Conflicts []struct {
			Dir           string   `json:"dir"`
			Name          string   `json:"name"`
			Kind          string   `json:"kind"`
			Recorded      string   `json:"recorded"`
			RecordedMTime int64    `json:"recorded_mtime"`
			RecordedSize  int64    `json:"recorded_size"`
			Found         []string `json:"found"`
			Detail        string   `json:"detail"`
		} `json:"conflicts"`
		Matched     []jsonConflict `json:"matched"`
		RemoteAdded []jsonConflict `json:"remote_added"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return ConflictScan{}, fmt.Errorf("detect conflicts: decode: %w", err)
	}

	// A backend that predates the field means "available" by omission.
	scan := ConflictScan{Available: true, Summary: parsed.Summary}
	if parsed.Available != nil {
		scan.Available = *parsed.Available
	}

	seen := map[string]bool{}
	scan.Conflicts = make([]Conflict, 0, len(parsed.Conflicts))
	for _, c := range parsed.Conflicts {
		p := path.Join(c.Dir, c.Name)
		// One path may trigger two signals (missing token AND a duplicate); the
		// caller acts per path, so keep only the first.
		key := p + "\x00" + c.Kind
		if seen[key] {
			continue
		}
		seen[key] = true
		scan.Conflicts = append(scan.Conflicts, Conflict{
			Path: p, Kind: c.Kind, Detail: c.Detail,
			Recorded: c.Recorded, Found: c.Found,
			RecordedMTime: c.RecordedMTime, RecordedSize: c.RecordedSize,
		})
	}
	sort.SliceStable(scan.Conflicts, func(i, j int) bool {
		return scan.Conflicts[i].Path < scan.Conflicts[j].Path
	})

	scan.Matched = toConflicts(parsed.Matched, ConflictKindUnchanged)
	scan.RemoteAdded = toConflicts(parsed.RemoteAdded, ConflictKindRemoteAdded)
	return scan, nil
}

// jsonConflict is one entry of the matched / remote_added lists.
type jsonConflict struct {
	Dir           string   `json:"dir"`
	Name          string   `json:"name"`
	Recorded      string   `json:"recorded"`
	RecordedMTime int64    `json:"recorded_mtime"`
	RecordedSize  int64    `json:"recorded_size"`
	Found         []string `json:"found"`
}

// toConflicts flattens dir+name into a path and stamps the kind on.
func toConflicts(in []jsonConflict, kind string) []Conflict {
	if len(in) == 0 {
		return nil
	}
	out := make([]Conflict, 0, len(in))
	for _, c := range in {
		out = append(out, Conflict{
			Path: path.Join(c.Dir, c.Name), Kind: kind,
			Recorded: c.Recorded, Found: c.Found,
			RecordedMTime: c.RecordedMTime, RecordedSize: c.RecordedSize,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// reportConflicts prints a table and returns the paths involved.
func reportConflicts(w io.Writer, pairName string, conflicts []Conflict) []string {
	if len(conflicts) == 0 {
		return nil
	}

	fmt.Fprintf(w, "\npair %q: %d conflict(s) between the folder and the sync state\n",
		pairName, len(conflicts))
	fmt.Fprintf(w, "  %-40s %-18s %s\n", "path", "kind", "detail")
	paths := make([]string, 0, len(conflicts))
	for _, c := range conflicts {
		fmt.Fprintf(w, "  %-40s %-18s %s\n", truncate(c.Path, 40), c.Kind, c.Detail)
		paths = append(paths, c.Path)
	}
	return paths
}

// excludePaths removes the given paths from the sync set for this run.
//
// This is how "report" honours "do not touch the cloud": the paths are filtered
// out before the engine runs, so nothing about them is uploaded, deleted or
// overwritten. Using filters rather than skipping after the fact means a conflict
// can never be half-handled.
func excludePaths(ctx context.Context, paths []string) (context.Context, error) {
	if len(paths) == 0 {
		return ctx, nil
	}
	ctx, fi := filter.AddConfig(ctx)
	for _, p := range paths {
		glob := "/" + strings.TrimPrefix(p, "/")
		if err := fi.Add(false, glob); err != nil {
			return ctx, fmt.Errorf("exclude conflicting path %q: %w", p, err)
		}
	}
	return ctx, nil
}

// keepBoth uploads the local copy of a conflicting path under a new name,
// leaving the remote copy untouched.
//
// The new name carries the host name and a timestamp so the two versions can be
// told apart later; PLAN.md calls this "<name> (from <host> <ts>)<ext>".
func keepBoth(ctx context.Context, fdst, fsrc fs.Fs, conflictPath, host string, seq int) (string, error) {
	srcObj, err := fsrc.NewObject(ctx, conflictPath)
	if err != nil {
		return "", fmt.Errorf("read local %q: %w", conflictPath, err)
	}

	dir := path.Dir(conflictPath)
	if dir == "." {
		dir = ""
	}
	ext := path.Ext(conflictPath)
	base := strings.TrimSuffix(path.Base(conflictPath), ext)

	stamp := nowStamp()
	newName := fmt.Sprintf("%s (from %s %s)%s", base, host, stamp, ext)
	if seq > 1 {
		newName = fmt.Sprintf("%s (from %s %s %d)%s", base, host, stamp, seq, ext)
	}
	newRel := path.Join(dir, newName)

	dstObj, err := operations.Copy(ctx, fdst, nil, newRel, srcObj)
	if err != nil {
		return "", fmt.Errorf("upload %q as %q: %w", conflictPath, newRel, err)
	}
	// The token is diagnostics only. rclone does not always hand back an object
	// carrying one, and printing "token ?" in a log line that otherwise reads as
	// a success is worse than saying nothing about it.
	if token := objectToken(dstObj); token != "?" {
		fs.Logf(nil, "kept both: uploaded %q as %q (token %s)", conflictPath, newRel, token)
	} else {
		fs.Logf(nil, "kept both: uploaded %q as %q", conflictPath, newRel)
	}
	return newRel, nil
}

// adoptCloudCopy records the cloud copy as the baseline for a path whose local
// file was moved aside by keep-both.
//
// The token comes from the caller, which already has it: a `remote-replaced`
// conflict lists the tokens currently carrying the name. Asking the object again
// is what D44 records as a mistake - after a replacement the object read back can
// carry no token at all (`?`), and the local backend never does.
func adoptCloudCopy(ctx context.Context, p *Pair, conflictPath, token string) error {
	if token == "" || token == "?" {
		// Without a token there is nothing to record. Saying so is better than
		// writing `?` into the snapshot, which would make every later comparison
		// fail and bring the conflict back forever.
		return fmt.Errorf("no cloud token for %q, so the baseline cannot be recorded; "+
			"the next run will report the same conflict again", conflictPath)
	}

	remotePath, err := p.RemotePath()
	if err != nil {
		return err
	}
	_ = remotePath
	fsrc, err := fs.NewFs(ctx, p.LocalPath())
	if err != nil {
		return fmt.Errorf("open source %q: %w", p.LocalPath(), err)
	}

	localObj, err := fsrc.NewObject(ctx, conflictPath)
	if err != nil {
		return fmt.Errorf("read local %q after keep-both: %w", conflictPath, err)
	}

	return resolveFn(ctx, p, conflictPath, map[string]string{
		"action": "adopt",
		"token":  token,
		"mtime":  fmt.Sprintf("%d", localObj.ModTime(ctx).Unix()),
		"size":   fmt.Sprintf("%d", localObj.Size()),
	})
}

// truncate shortens s for a fixed-width column.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return "..." + s[len(s)-(n-3):]
}

// nowStamp is the timestamp used in keep-both file names; a variable so tests can
// pin it.
var nowStamp = func() string {
	return time.Now().Format("20060102-150405")
}

func objectToken(o fs.Object) string {
	if o == nil {
		return "?"
	}
	if ider, ok := o.(interface{ ID() string }); ok {
		return ider.ID()
	}
	return "?"
}
