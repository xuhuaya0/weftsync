package daemon

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Browsing the filesystem from the console.
//
// A web page cannot open a native folder picker and cannot learn the path of a
// folder it does pick, so "Browse" has to be served by the daemon: list the
// subdirectories of a path and let the user click down. Without it the only way
// to set a pair's root is to type an absolute path, which is exactly the sort of
// thing a sync tool should not require.

// BrowseEntry is one row in the folder picker.
type BrowseEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// IsDir is always true today; kept so a future file picker can reuse this.
	IsDir bool `json:"is_dir"`
}

// BrowseView is what the picker needs to render one directory.
type BrowseView struct {
	// Path is the directory being listed, always absolute and cleaned.
	Path string `json:"path"`
	// Parent is the directory above it, empty at a drive root or "/".
	Parent string `json:"parent,omitempty"`
	// Dirs are the subdirectories, sorted by name.
	Dirs []BrowseEntry `json:"dirs"`
	// Roots are shortcuts shown when Path is empty: drive letters on Windows,
	// common mount points elsewhere.
	Roots []string `json:"roots,omitempty"`
	// Error explains why a path could not be listed, without failing the request:
	// the picker still has to render the parent so the user can go back.
	Error string `json:"error,omitempty"`
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
func browseView(path string) BrowseView {
	path = strings.TrimSpace(path)
	if path == "" {
		path = defaultBrowseRoot()
	}
	if path == "" {
		return BrowseView{Roots: browseRoots()}
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return BrowseView{Path: path, Roots: browseRoots(), Error: err.Error()}
	}
	abs = filepath.Clean(abs)

	view := BrowseView{Path: abs}
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
		view.Roots = browseRoots()
		return view
	}
	view.Dirs = dirs
	return view
}
