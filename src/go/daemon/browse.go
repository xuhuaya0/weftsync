package daemon

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
)

// Browsing sync roots from the console.
//
// A web page cannot open a native folder picker and cannot learn the path of a
// folder it does pick, so "Browse" has to be served by the daemon: list the
// subdirectories of a path and let the user click down. Without it the only way
// to set a pair's root is to type an absolute path, which is exactly the sort of
// thing a sync tool should not require.
//
// The same picker serves cloud roots. That half cannot read a disk: it asks the
// backend through rclone, which is also the only way to know whether a remote is
// reachable at all - a path that lists is a path that will sync.

// browseTimeout bounds one listing. A local directory returns immediately; a cloud
// folder can hang on a bad token or an unreachable endpoint, and a picker that
// spins forever is worse than one that reports a timeout.
const browseTimeout = 20 * time.Second

// BrowseKind says which world the listed path belongs to, so the picker can label
// it correctly and offer the right starting points.
type BrowseKind string

const (
	// BrowseLocal is a directory on this machine.
	BrowseLocal BrowseKind = "local"
	// BrowseRemote is a directory behind an rclone remote.
	BrowseRemote BrowseKind = "remote"
)

// BrowseEntry is one row in the folder picker.
type BrowseEntry struct {
	Name string `json:"name"`
	// Path is what the picker should send back for this entry: an absolute path
	// locally, "<remote>:<path>" for the cloud.
	Path string `json:"path"`
	// IsDir is always true today; kept so a future file picker can reuse this.
	IsDir bool `json:"is_dir"`
}

// BrowseRemoteRef is one configured remote, offered as a starting point.
//
// The type is included because the name alone does not say what the user is
// picking: "backup" could be a disk or a Feishu folder, and the type is the only
// clue the picker can show.
type BrowseRemoteRef struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// BrowseView is what the picker needs to render one directory.
type BrowseView struct {
	// Path is the directory being listed, always absolute (or <remote>:<path>).
	Path string `json:"path"`
	// Parent is the directory above it, empty at a drive root, "/" or a remote root.
	Parent string `json:"parent,omitempty"`
	// Kind is local or remote; the picker uses it to switch its wording.
	Kind BrowseKind `json:"kind"`
	// Dirs are the subdirectories, sorted by name.
	Dirs []BrowseEntry `json:"dirs"`
	// Roots are shortcuts shown when Path is empty: drive letters on Windows,
	// common mount points elsewhere.
	Roots []string `json:"roots,omitempty"`
	// Remotes are the configured rclone remotes, so a cloud root can be picked
	// without knowing the config file by heart.
	Remotes []BrowseRemoteRef `json:"remotes,omitempty"`
	// Error explains why a path could not be listed, without failing the request:
	// the picker still has to render the parent so the user can go back.
	Error string `json:"error,omitempty"`
}

// isRemotePath reports whether p names an rclone remote rather than a disk path.
//
// The colon is the only marker rclone has, so it has to be read carefully:
//
//   - "C:\data" and "C:/data" are drive letters, not remotes - one letter before
//     the colon is the tell.
//   - ":local:C:/data" also starts with a colon, but that is rclone's on-the-fly
//     backend form for a *local* path; stripLocalBackend handles it first.
//   - anything else of the shape "name:..." is a remote.
func isRemotePath(p string) bool {
	i := strings.Index(p, ":")
	if i <= 0 {
		return false
	}
	head := p[:i]
	if len(head) == 1 && isASCIILetter(head[0]) {
		return false // Windows drive letter
	}
	// A remote name cannot contain a separator; if it does, this is a path that
	// happens to contain a colon, which is a legal filename character on Unix.
	if strings.ContainsAny(head, `/\`) {
		return false
	}
	return true
}

func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// localBackendPrefix is rclone's on-the-fly form for the local backend.
const localBackendPrefix = ":local:"

// stripLocalBackend turns ":local:C:/data" into "C:/data".
//
// A pair may store its root in either form, and the picker has to treat both as a
// local directory: otherwise it would try to open ":local:" as a remote name and
// fail on a path the user can see in the box.
func stripLocalBackend(p string) (string, bool) {
	if len(p) >= len(localBackendPrefix) &&
		strings.EqualFold(p[:len(localBackendPrefix)], localBackendPrefix) {
		return p[len(localBackendPrefix):], true
	}
	return p, false
}

// defaultBrowseRoot returns the directory a picker starts from.
//
// Empty means "show the roots": on Windows that is the drive list, which is the
// only honest starting point because there is no single filesystem root.
func defaultBrowseRoot() string {
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return ""
}

// browseRoots lists the places a picker can start from.
func browseRoots() []string {
	if os.PathSeparator == '\\' {
		var out []string
		for c := 'A'; c <= 'Z'; c++ {
			root := string(c) + ":\\"
			if _, err := os.Stat(root); err == nil {
				out = append(out, root)
			}
		}
		return out
	}
	// Unix-like: "/" is always valid and is the real root.
	return []string{"/"}
}

// listDirs returns the subdirectories of dir.
//
// Unreadable entries are skipped rather than failing the listing: a permission
// error on one child must not hide the rest of the folder.
func listDirs(dir string) ([]BrowseEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]BrowseEntry, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		// Hidden directories are listed: a sync root is often inside one
		// (.config, .local), and hiding them would make those roots unreachable.
		out = append(out, BrowseEntry{
			Name:  name,
			Path:  filepath.Join(dir, name),
			IsDir: true,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// browseView builds the picker view for one path.
//
// An empty path with no home directory falls back to the roots list, so the
// picker is never a dead end.
func browseView(ctx context.Context, raw string) BrowseView {
	raw = strings.TrimSpace(raw)
	if p, ok := stripLocalBackend(raw); ok {
		raw = p
	}
	if isRemotePath(raw) {
		return remoteBrowseView(ctx, raw)
	}
	return localBrowseView(raw)
}

// localBrowseView lists a directory on this machine.
func localBrowseView(raw string) BrowseView {
	path := raw
	if path == "" {
		path = defaultBrowseRoot()
	}
	if path == "" {
		return BrowseView{Kind: BrowseLocal, Roots: browseRoots()}
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return BrowseView{Kind: BrowseLocal, Path: path, Roots: browseRoots(), Error: err.Error()}
	}
	abs = filepath.Clean(abs)

	// The starting points are always sent, not only when the listing fails: on
	// Windows there is no single root, so without the drive list the picker cannot
	// ever leave the drive it opened on.
	view := BrowseView{
		Kind:    BrowseLocal,
		Path:    abs,
		Roots:   browseRoots(),
		Remotes: configuredRemotes(),
	}
	// A file path lists its directory instead of failing: the user may have
	// pasted a file where a folder is expected.
	if info, statErr := os.Stat(abs); statErr == nil && !info.IsDir() {
		abs = filepath.Dir(abs)
		view.Path = abs
	}

	if parent := filepath.Dir(abs); parent != abs {
		view.Parent = parent
	}

	dirs, listErr := listDirs(abs)
	if listErr != nil {
		view.Error = listErr.Error()
		return view
	}
	view.Dirs = dirs
	return view
}

// configuredRemotes lists the remotes in the rclone config file.
//
// These are what the picker offers when the user has not typed a cloud path yet.
// A remote with no type is not offered: it is an incomplete section, and opening
// it would fail with a message about a missing type rather than about the thing
// the user came to pick.
func configuredRemotes() []BrowseRemoteRef {
	names := config.FileSections()
	sort.Strings(names)

	out := make([]BrowseRemoteRef, 0, len(names))
	for _, name := range names {
		typ, _ := config.FileGetValue(name, "type")
		if typ == "" {
			continue
		}
		out = append(out, BrowseRemoteRef{Name: name, Type: typ})
	}
	return out
}

// remoteBrowseView lists a directory behind an rclone remote.
//
// The listing goes through the backend, not the config file, so a remote whose
// credentials are wrong reports that here - in the picker, before a pair is saved
// - instead of at the next run.
func remoteBrowseView(ctx context.Context, anchor string) BrowseView {
	view := BrowseView{
		Kind:    BrowseRemote,
		Path:    displayRemotePath(anchor),
		Remotes: configuredRemotes(),
		// The local roots come along too: the picker shows one dialog for both
		// worlds, and without them switching back to this machine would mean
		// closing and reopening the dialog.
		Roots: browseRoots(),
	}

	ctx, cancel := context.WithTimeout(ctx, browseTimeout)
	defer cancel()

	f, err := fs.NewFs(ctx, anchor)
	if err != nil {
		view.Error = fmt.Sprintf("打不开 %s：%v", remoteNameOf(anchor), err)
		return view
	}

	entries, err := f.List(ctx, "")
	if err != nil {
		view.Error = fmt.Sprintf("列目录失败：%v", err)
		return view
	}

	dirs := make([]BrowseEntry, 0, len(entries))
	for _, e := range entries {
		if _, isDir := e.(fs.Directory); !isDir {
			continue
		}
		// The backend's own identifier differs per backend (Feishu has a token,
		// SMB a name), so the path to send back is built from the name the user
		// sees, not from the identifier.
		dirs = append(dirs, BrowseEntry{
			Name:  e.Remote(),
			Path:  joinRemotePath(anchor, e.Remote()),
			IsDir: true,
		})
	}
	sort.Slice(dirs, func(i, j int) bool {
		return strings.ToLower(dirs[i].Name) < strings.ToLower(dirs[j].Name)
	})
	view.Dirs = dirs

	// The root of a remote has no parent: there is nothing above "feishu:" for
	// the picker to show, and offering one would mean inventing it.
	view.Parent = remoteParent(anchor)
	return view
}

// remoteNameOf is the remote part of "<remote>:<path>", for messages.
func remoteNameOf(anchor string) string {
	if i := strings.Index(anchor, ":"); i > 0 {
		return anchor[:i]
	}
	return anchor
}

// remotePathOf is the in-remote path of "<remote>:<path>", without leading or
// trailing separators, because rclone wants a relative path when listing.
func remotePathOf(anchor string) string {
	i := strings.Index(anchor, ":")
	if i < 0 {
		return ""
	}
	p := anchor[i+1:]
	// An rclone path always uses forward slashes, whatever the host OS calls a
	// separator.
	p = strings.ReplaceAll(p, `\`, "/")
	return strings.Trim(p, "/")
}

// joinRemotePath appends a child directory name to an anchor.
func joinRemotePath(anchor, name string) string {
	name = strings.Trim(strings.ReplaceAll(name, `\`, "/"), "/")
	base := remotePathOf(anchor)
	if base == "" {
		return remoteNameOf(anchor) + ":" + name
	}
	return remoteNameOf(anchor) + ":" + base + "/" + name
}

// remoteParent is the anchor one level up, or "" at the remote root.
func remoteParent(anchor string) string {
	base := remotePathOf(anchor)
	if base == "" {
		return ""
	}
	up := path.Dir(base)
	if up == "." || up == "/" || up == "" {
		return remoteNameOf(anchor) + ":"
	}
	return remoteNameOf(anchor) + ":" + up
}

// displayRemotePath is the anchor as the user should see it.
//
// An anchor that arrived with a trailing separator would otherwise render as
// "feishu:/a/b/", which reads like a different path than the one being listed.
func displayRemotePath(anchor string) string {
	name := remoteNameOf(anchor)
	base := remotePathOf(anchor)
	if base == "" {
		return name + ":"
	}
	return name + ":" + base
}
