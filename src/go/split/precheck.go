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
// Feishu rejects a folder with more than 1500 direct children, and it does so when
// the create/upload call arrives - which is in the middle of a run, after some
// files are already across. The point of checking first is to fail before anything
// has moved, with a message that names the offending directory.

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
	// Findings is every over-limit directory, largest first.
	Findings []Finding
}

func (e *OverLimitError) Error() string {
	if len(e.Findings) == 0 {
		return fmt.Sprintf("a directory under %q exceeds the %d-entry limit", e.Root, e.Limit)
	}
	f := e.Findings[0]
	where := f.RelPath
	if where == "" {
		where = "(根目录)"
	}
	msg := fmt.Sprintf(
		"目录 %q 有 %d 个直接子项，超过飞书单层上限（我们按 %d 拦截，飞书硬限 %d）。"+
			"NOTHING was uploaded. 请先拆分该目录（界面「拆分」或 weftsync split 命令）",
		where, f.Total(), e.Limit, FeishuLimit)
	if len(e.Findings) > 1 {
		msg += fmt.Sprintf("。另有 %d 个目录也超限", len(e.Findings)-1)
	}
	return msg
}

// DefaultRefuseAt is where a sync is refused.
//
// 1400 rather than 1500 on purpose: a directory sitting exactly on the hard limit
// starts failing as soon as one file is added, and a run that dies halfway is far
// worse than an early, clear refusal that names the directory.
const DefaultRefuseAt = 1400

// PrecheckOptions tunes the scan.
type PrecheckOptions struct {
	// RefuseAt is the direct-child count that makes a directory over-limit.
	RefuseAt int
	// MaxDepth stops the walk. Feishu also limits directory depth to 15, and a
	// local tree deeper than that will fail for a different reason, so there is
	// no point walking further looking for entry-count problems.
	MaxDepth int
}

// Precheck scans a local tree for directories holding too many direct children.
//
// Only direct children are counted, matching what Feishu limits. It returns nil
// when every directory is within the limit; a non-nil error means the scan itself
// failed (a permission problem, a vanished directory), which must not be reported
// as "within limits".
func Precheck(root string, opts PrecheckOptions) ([]Finding, error) {
	if opts.RefuseAt <= 0 {
		opts.RefuseAt = DefaultRefuseAt
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
