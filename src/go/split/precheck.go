package split

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Pre-flight check.
//
// A backend that caps a folder's direct children (Feishu: 1500) rejects the
// create/upload call in the middle of a run, after some files are already across.
// The point of checking first is to fail before anything has moved, with a
// message that names the offending directory.
//
// The limit itself comes from the backend (see LimitReporter in limits.go), not
// from this package: a local disk has no such cap, and refusing a local sync over
// a Feishu number was a real bug.

// Finding is one directory that is at or over the limit.
type Finding struct {
	// RelPath is relative to the pair root, using forward slashes. Empty means
	// the root itself.
	RelPath string
	// Files and Dirs are the direct children, split by kind.
	Files int
	Dirs  int
}

// Total is the number of direct children.
func (f Finding) Total() int { return f.Files + f.Dirs }

// OverLimitError means a directory holds more direct children than the backend
// accepts, so a sync must not be attempted.
//
// It is a distinct type because the daemon reports it differently: this is not a
// failure of the run, it is a condition the user has to fix by splitting the
// directory, and saying "sync failed" would send them looking for a bug.
type OverLimitError struct {
	// Root is the local root that was checked, for the message.
	Root string
	// Limit is the threshold that was exceeded.
	Limit int
	// Backend names the destination for the message, e.g. "feishu". Empty when
	// the caller does not know it, in which case the message stays generic.
	Backend string
	// BackendLimit is the backend's documented hard limit, for context. 0 means
	// unknown, and is then left out.
	BackendLimit int
	// Findings is every over-limit directory, largest first.
	Findings []Finding
}

func (e *OverLimitError) Error() string {
	if len(e.Findings) == 0 {
		if e.Backend != "" {
			return fmt.Sprintf("a directory under %q exceeds the %s limit of %d entries",
				e.Root, e.Backend, e.Limit)
		}
		return fmt.Sprintf("a directory under %q exceeds the %d-entry limit", e.Root, e.Limit)
	}
	f := e.Findings[0]
	where := f.RelPath
	if where == "" {
		where = "(根目录)"
	}

	// Naming the backend matters: the same message used to say "飞书" for a pair
	// whose destination was a local disk.
	var exceeded string
	switch {
	case e.Backend != "" && e.BackendLimit > 0:
		exceeded = fmt.Sprintf(
			"超过后端 %q 的单目录上限（我们按 %d 拦截，后端硬限 %d）",
			e.Backend, e.Limit, e.BackendLimit)
	case e.Backend != "":
		exceeded = fmt.Sprintf("超过后端 %q 的单目录上限（按 %d 拦截）", e.Backend, e.Limit)
	default:
		exceeded = fmt.Sprintf("超过单目录上限（按 %d 拦截）", e.Limit)
	}

	msg := fmt.Sprintf(
		"目录 %q 有 %d 个直接子项，%s。"+
			"NOTHING was uploaded. 请先拆分该目录（界面「拆分」或 weftsync split 命令）",
		where, f.Total(), exceeded)
	if len(e.Findings) > 1 {
		msg += fmt.Sprintf("。另有 %d 个目录也超限", len(e.Findings)-1)
	}
	return msg
}

// PrecheckOptions tunes the scan.
type PrecheckOptions struct {
	// RefuseAt is the direct-child count that makes a directory over-limit. It
	// must be positive: "no limit" is expressed by not calling Precheck at all,
	// which is decided from the backend's declaration (see LimitOf/RefuseAtFor).
	//
	// Requiring it rather than defaulting is deliberate. A default here is how a
	// Feishu number ended up applied to every backend.
	RefuseAt int
	// MaxDepth stops the walk, counting the pair root as 1. The backend's own
	// depth limit (Feishu: 15) is the natural value: a local tree deeper than the
	// backend allows will fail for a different reason, so there is no point
	// walking further looking for entry-count problems. 0 means the default 15.
	MaxDepth int
}

// Precheck scans a local tree for directories holding too many direct children.
//
// Only direct children are counted, matching what the backends with a limit
// enforce. It returns nil when every directory is within the limit; a non-nil
// error means the scan itself failed (a permission problem, a vanished
// directory), which must not be reported as "within limits".
func Precheck(root string, opts PrecheckOptions) ([]Finding, error) {
	if opts.RefuseAt <= 0 {
		return nil, fmt.Errorf(
			"precheck: the refuse threshold must be positive (skip the scan instead " +
				"when the backend declares no limit)")
	}
	if opts.MaxDepth <= 0 {
		opts.MaxDepth = 15
	}

	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("check %q: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%q is not a directory", root)
	}

	var findings []Finding
	err = walkDirs(root, "", 1, opts, &findings)
	if err != nil {
		return nil, err
	}

	// Largest first: the biggest offender is the one to fix first.
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].Total() != findings[j].Total() {
			return findings[i].Total() > findings[j].Total()
		}
		return findings[i].RelPath < findings[j].RelPath
	})
	return findings, nil
}

// walkDirs counts one directory and recurses into its subdirectories.
func walkDirs(absDir, relDir string, depth int, opts PrecheckOptions, out *[]Finding) error {
	entries, err := os.ReadDir(absDir)
	if err != nil {
		return fmt.Errorf("read %q: %w", absDir, err)
	}

	// The trash folder is excluded from sync by the job layer, so counting it
	// would refuse a run over files that are never uploaded.
	f := Finding{RelPath: relDir}
	for _, e := range entries {
		if depth == 1 && e.IsDir() && isVersioningDir(e.Name()) {
			continue
		}
		if e.IsDir() {
			f.Dirs++
		} else {
			f.Files++
		}
	}
	if f.Total() > opts.RefuseAt {
		*out = append(*out, f)
	}

	if depth >= opts.MaxDepth {
		return nil
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if depth == 1 && isVersioningDir(e.Name()) {
			continue
		}
		if err := walkDirs(filepath.Join(absDir, e.Name()),
			relJoin(relDir, e.Name()), depth+1, opts, out); err != nil {
			return err
		}
	}
	return nil
}

// isVersioningDir reports whether name is the versioning folder.
//
// The default is checked by name because the precheck does not know the pair's
// configured versioning directory; a custom name pattern is still counted. Better
// to over-count slightly than to let a real over-limit directory through.
func isVersioningDir(name string) bool {
	return name == "_weftsync_trash"
}

// Entries reads one directory's direct children for planning.
//
// Symlinks are followed for their type only (a symlinked file is treated as a
// file); weftsync never writes through them, and a symlink loop would otherwise
// hang the walk.
func Entries(root, relDir string) ([]Entry, error) {
	abs := root
	if relDir != "" {
		abs = filepath.Join(root, filepath.FromSlash(relDir))
	}
	items, err := os.ReadDir(abs)
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", abs, err)
	}

	out := make([]Entry, 0, len(items))
	for _, it := range items {
		e := Entry{Name: it.Name(), IsDir: it.IsDir()}
		if info, err := it.Info(); err == nil {
			e.Size = info.Size()
			e.ModTime = info.ModTime()
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// CleanRelPath validates a directory path coming from a request.
//
// The same rule as the conflict resolver: reject rather than rewrite, because a
// silently corrected path would operate on a different directory than the user
// asked for. This one is more important still, since the operation moves files.
func CleanRelPath(p string) (string, error) {
	if p == "" {
		return "", nil
	}
	if filepath.IsAbs(p) || p[0] == '/' || p[0] == '\\' {
		return "", fmt.Errorf("path %q must be relative to the pair root", p)
	}
	if filepath.VolumeName(p) != "" || (len(p) > 1 && p[1] == ':') {
		return "", fmt.Errorf("path %q must be relative to the pair root", p)
	}
	for _, part := range splitPath(p) {
		if part == ".." {
			return "", fmt.Errorf("path %q must not contain ..", p)
		}
	}
	return relJoin("", filepath.ToSlash(p)), nil
}

// splitPath splits on either separator.
func splitPath(p string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(p); i++ {
		if p[i] == '/' || p[i] == '\\' {
			if i > start {
				parts = append(parts, p[start:i])
			}
			start = i + 1
		}
	}
	if start < len(p) {
		parts = append(parts, p[start:])
	}
	return parts
}

// StatOf returns size and mtime for a file, used when recording a snapshot.
func StatOf(abs string) (int64, time.Time, error) {
	info, err := os.Stat(abs)
	if err != nil {
		return 0, time.Time{}, err
	}
	return info.Size(), info.ModTime(), nil
}
