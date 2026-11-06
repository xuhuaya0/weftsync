package feishu

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/hash"
)

// Fs is the Feishu Drive backend instance.
type Fs struct {
	name     string
	root     string // logical path inside rootToken
	opt      Options
	client   *client
	resolver *dirResolver
	features *fs.Features
}

// newFs builds the backend, validating credentials and the root folder.
func newFs(ctx context.Context, name, root string, opt *Options) (*Fs, error) {
	if missing := opt.resolveEnv(); len(missing) > 0 {
		return nil, fmt.Errorf(
			"feishu: missing required configuration: %s.\n"+
				"Set them on the remote, or export the matching environment variables",
			strings.Join(missing, ", "))
	}

	rootToken := ExtractFolderToken(opt.RootToken)
	if rootToken == "" {
		return nil, ErrNoRootToken
	}

	hc := &http.Client{Timeout: defaultTimeout}
	ts := newTokenSource(opt.AppID, opt.AppSecret, opt.BaseURL, hc)
	c := newClient(opt.BaseURL, ts, hc)

	f := &Fs{
		name:   name,
		root:   normalizePath(root),
		opt:    *opt,
		client: c,
	}
	f.features = (&fs.Features{
		DuplicateFiles:          true, // verified: two same-named files can coexist
		CanHaveEmptyDirectories: true, // verified: create_folder makes empty dirs
		CaseInsensitive:         false,
		ReadMimeType:            false,
		WriteMimeType:           false,
		SlowModTime:             false,
		PartialUploads:          false,
	}).Fill(ctx, f)

	f.resolver = newDirResolver(c, rootToken, f.root, f.root)

	// Validate the root eagerly so the user hears about a bad token now rather
	// than on the first operation.
	if _, err := c.getFolderMeta(ctx, rootToken); err != nil {
		return nil, fmt.Errorf(
			"feishu: cannot open root folder %q: %w.\n"+
				"The app must be a chat bot and the folder must be shared with "+
				"the chat containing that app, with edit permission",
			rootToken, err)
	}

	return f, nil
}

// ---------------------------------------------------------------- fs.Info

func (f *Fs) Name() string   { return f.name }
func (f *Fs) Root() string   { return f.root }
func (f *Fs) String() string { return fmt.Sprintf("Feishu root %q", f.root) }

// Precision is one second: modified_time is a whole number of seconds (E5).
func (f *Fs) Precision() time.Duration { return precision }

// Hashes reports none: the listing carries no content hash (E3).
func (f *Fs) Hashes() hash.Set { return hash.Set(hash.None) }

func (f *Fs) Features() *fs.Features { return f.features }

// ---------------------------------------------------------------- fs.Fs

// List returns the direct children of dir.
//
// Duplicate names are collapsed by pickRemoteEntry so rclone's engine never
// sees two entries for one path (research.md R5).
func (f *Fs) List(ctx context.Context, dir string) (fs.DirEntries, error) {
	dir = normalizePath(dir)

	folderToken, err := f.resolver.resolveDir(ctx, dir)
	if err != nil {
		if isNotFound(err) {
			return nil, fs.ErrorDirNotFound
		}
		return nil, err
	}

	entries, err := f.resolver.listCached(ctx, folderToken)
	if err != nil {
		return nil, err
	}

	// Collapse duplicates by name, keeping directories and files distinct.
	byName := make(map[string][]remoteEntry, len(entries))
	order := make([]string, 0, len(entries))
	for _, e := range entries {
		key := e.Name
		if _, seen := byName[key]; !seen {
			order = append(order, key)
		}
		byName[key] = append(byName[key], e)
	}

	out := make(fs.DirEntries, 0, len(order))
	for _, name := range order {
		group := byName[name]
		chosen := pickRemoteEntry(group, "")
		if len(group) > 1 {
			fs.Debugf(f, "folder %q holds %d entries named %q; using token %s",
				dir, len(group), name, chosen.Token)
		}

		if chosen.IsDir {
			d := fs.NewDir(joinPath(dir, chosen.Name), time.Unix(chosen.ModTime, 0))
			d.SetID(chosen.Token)
			out = append(out, d)
			continue
		}
		out = append(out, &Object{
			fs:     f,
			remote: joinPath(dir, chosen.Name),
			entry:  chosen,
		})
	}

	return out, nil
}

// NewObject finds a single remote path.
func (f *Fs) NewObject(ctx context.Context, remote string) (fs.Object, error) {
	remote = normalizePath(remote)
	if remote == "" {
		return nil, fs.ErrorObjectNotFound
	}

	entry, err := f.resolver.resolveEntry(ctx, remote)
	if err != nil {
		if isNotFound(err) {
			return nil, fs.ErrorObjectNotFound
		}
		return nil, err
	}
	if entry == nil {
		return nil, fs.ErrorObjectNotFound
	}
	if entry.IsDir {
		return nil, fs.ErrorIsDir
	}

	return &Object{fs: f, remote: remote, entry: *entry}, nil
}

// Put uploads a local file through the direct (single request) path.
//
// Files above 20MB are rejected with a message that says so; chunked upload is
// F2's scope (FR-013).
func (f *Fs) Put(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) (fs.Object, error) {
	remote := normalizePath(src.Remote())
	if remote == "" {
		return nil, errors.New("feishu: Put requires a non-empty remote path")
	}

	// Read at most cap+1 bytes so an oversized stream is detected without
	// buffering the whole thing (the probe showed 21MB is rejected, so the
	// limit is exact rather than advisory).
	limited := io.LimitReader(in, MaxDirectUpload+1)
	content, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("feishu: read source %q: %w", remote, err)
	}
	if len(content) > MaxDirectUpload {
		return nil, &ErrFileTooLarge{Name: remote, Size: int64(len(content))}
	}

	parentPath, name := parentOf(remote)
	parentToken, err := f.resolver.resolveDir(ctx, parentPath)
	if err != nil {
		if !isNotFound(err) {
			return nil, fmt.Errorf("feishu: upload target folder for %q: %w", remote, err)
		}
		// FR-012: create missing parent directories rather than failing. The
		// first file of a new tree arrives before rclone's own mkdir pass.
		if mkErr := f.Mkdir(ctx, parentPath); mkErr != nil {
			return nil, fmt.Errorf("feishu: create upload target folder for %q: %w", remote, mkErr)
		}
		parentToken, err = f.resolver.resolveDir(ctx, parentPath)
		if err != nil {
			return nil, fmt.Errorf("feishu: upload target folder for %q: %w", remote, err)
		}
	}

	token, err := f.client.uploadAll(ctx, parentToken, name, content)
	if err != nil {
		return nil, err
	}

	f.resolver.invalidate(parentToken)

	entry := remoteEntry{
		Name:        name,
		Token:       token,
		IsDir:       false,
		ModTime:     time.Now().Unix(),
		CreatedTime: time.Now().Unix(),
		ParentToken: parentToken,
		RawType:     "file",
	}

	return &Object{fs: f, remote: remote, entry: entry}, nil
}

// Mkdir creates a directory and all missing parents. It is idempotent.
//
// rclone calls Mkdir("") when the remote itself is the directory to create
// (e.g. `rclone mkdir remote:a/b` creates the Fs at "a/b" then makes "").
// Returning early on an empty path would therefore silently do nothing, so the
// Fs root path is ensured here too.
func (f *Fs) Mkdir(ctx context.Context, dir string) error {
	target := joinPath(f.root, normalizePath(dir))
	if target == "" {
		return nil // the configured root_token folder always exists
	}

	current := f.resolver.rootToken
	pathSoFar := ""

	for _, segment := range strings.Split(target, "/") {
		if segment == "" {
			continue
		}
		pathSoFar = joinPath(pathSoFar, segment)

		entries, err := f.resolver.listCached(ctx, current)
		if err != nil {
			return err
		}

		var found string
		for _, e := range entries {
			if e.IsDir && e.Name == segment {
				found = e.Token
				break
			}
		}
		if found != "" {
			current = found
			continue
		}

		token, err := f.client.createFolder(ctx, current, segment)
		if err != nil {
			return fmt.Errorf("feishu: create folder %q: %w", pathSoFar, err)
		}
		f.resolver.invalidate(current)
		current = token
	}

	return nil
}

// Rmdir is not implemented in F1; deleting directories arrives with F3.
func (f *Fs) Rmdir(ctx context.Context, dir string) error {
	return fs.ErrorNotImplemented
}

// tokenStillPresent reports whether token is among the current children of the
// folder at logicalPath.
//
// Used before deleting a previous version (FR-035): a stale token from an old
// state file must never be used to delete something, because Feishu has no
// trash and the loss would be permanent.
func (f *Fs) tokenStillPresent(ctx context.Context, logicalPath, token string) bool {
	if token == "" {
		return false
	}
	folderToken, err := f.resolver.resolveDir(ctx, logicalPath)
	if err != nil {
		return false
	}
	// Force a fresh listing: a cached one may predate the file being deleted
	// by someone else.
	f.resolver.invalidate(folderToken)

	entries, err := f.resolver.listCached(ctx, folderToken)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.Token == token {
			return true
		}
	}
	return false
}

// rootIsFile reports whether the Fs root path names an existing file, and if
// so returns its parent directory.
//
// rclone relies on this to split "remote:dir/file.txt" into a parent Fs plus a
// filename. Missing paths (the mkdir case) are simply "not a file".
func (f *Fs) rootIsFile(ctx context.Context) (parent string, isFile bool) {
	if f.root == "" {
		return "", false
	}

	parentPath, name := parentOf(f.root)
	parentToken, err := f.resolver.walk(ctx, parentPath)
	if err != nil {
		return "", false
	}

	entries, err := f.resolver.listCached(ctx, parentToken)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if e.Name == name && !e.IsDir {
			return parentPath, true
		}
	}
	return "", false
}

// isNotFound reports whether err means "this path does not exist".
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Code == CodeParentNotFound
	}
	return errors.Is(err, fs.ErrorDirNotFound) || errors.Is(err, fs.ErrorObjectNotFound)
}
