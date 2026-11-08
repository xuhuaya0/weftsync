package feishu

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/hash"
	"weftsync/state"
)

// Fs is the Feishu Drive backend instance.
type Fs struct {
	name     string
	root     string // logical path inside rootToken
	opt      Options
	client   *client
	resolver *dirResolver
	features *fs.Features

	// st is the local snapshot that makes incremental sync possible; nil when
	// no state_file is configured. See state.go for why it exists.
	//
	// rclone runs several transfers in parallel, so every access goes through
	// stateMu: the entry map cannot be read and written at the same time, and
	// two concurrent Save calls would fight over the same .tmp file (observed on
	// Windows as "the file is being used by another process").
	stateMu  sync.Mutex
	st       *state.File
	stPath   string
	storable bool // false if the snapshot cannot be persisted

	// repair is true when the snapshot had to be rebuilt, which forbids all
	// deletions for this run (contracts/state-file-format.md §5).
	repair       bool
	repairReason string
}

// newFs builds the backend, validating credentials and the root folder.
func newFs(ctx context.Context, name, root string, opt *Options) (*Fs, error) {
	missing, secretForms := opt.resolveEnv()
	if len(missing) > 0 {
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
	// The other interpretation of the stored secret, tried once if the first is
	// rejected. Empty when there is nothing else worth trying.
	if len(secretForms) > 1 {
		ts.altSecret = secretForms[1]
	}
	c := newClient(opt.BaseURL, ts, hc)
	c.withLimits(opt.QPS, opt.MovePerMinute)

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
		// Move is server-side (verified by probing). It also makes
		// deletion=versioning cheap, because rclone's --backup-dir retires old
		// versions with Move instead of re-uploading them.
		//
		// PartialUploads stays false, so checkPartial still forces inplace and
		// existing destinations still route through Update(): adding Move does
		// not change the upload path.
		Move: f.Move,
		// Command exposes `backend duplicates` / `backend dedupe`. They live on
		// the backend because it is the only layer that sees the raw listing
		// (List collapses duplicates on purpose) and holds the snapshot tokens.
		Command: f.command,
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

	f.loadState()

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

// Put uploads a local file, choosing the single-request path or the block path
// by size.
//
// The split point is the server's: upload_all rejects anything above 20MB with
// code 1061002 (measured during M0), so larger files must use
// prepare/part/finish. See upload.go.
func (f *Fs) Put(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) (fs.Object, error) {
	remote := normalizePath(src.Remote())
	if remote == "" {
		return nil, errors.New("feishu: Put requires a non-empty remote path")
	}

	parentToken, name, err := f.resolvePutParent(ctx, remote)
	if err != nil {
		return nil, err
	}

	// The block protocol needs the total size up front, so it is only usable
	// when the source can report one.
	if size := src.Size(); size > MaxDirectUpload {
		token, err := f.putChunked(ctx, in, remote, name, parentToken, size)
		if err != nil {
			return nil, err
		}
		return f.uploaded(ctx, remote, name, parentToken, token, size, src), nil
	}

	// Read at most cap+1 bytes so an oversized stream is detected without
	// buffering the whole thing.
	//
	// The +1 is load-bearing: with a plain cap, a stream a single byte too long
	// would be truncated to the cap and uploaded as if complete - no error, a
	// healthy-looking token, and a file that is quietly wrong. route_test.go
	// fails if this becomes just MaxDirectUpload.
	limited := io.LimitReader(in, MaxDirectUpload+1)
	content, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("feishu: read source %q: %w", remote, err)
	}
	if len(content) > MaxDirectUpload {
		// Reaching here means src.Size() was <= 20MB or unknown while the
		// stream is not, so the announced size cannot be trusted.
		return nil, &ErrFileTooLarge{Name: remote, Size: int64(len(content)), SizeUnknown: true}
	}

	token, err := f.client.uploadAll(ctx, parentToken, name, content)
	if err != nil {
		return nil, err
	}
	return f.uploaded(ctx, remote, name, parentToken, token, int64(len(content)), src), nil
}

// resolvePutParent returns the folder token and leaf name for an upload,
// creating missing parent directories (FR-012).
func (f *Fs) resolvePutParent(ctx context.Context, remote string) (parentToken, name string, err error) {
	parentPath, name := parentOf(remote)

	parentToken, err = f.resolver.resolveDir(ctx, parentPath)
	if err == nil {
		return parentToken, name, nil
	}
	if !isNotFound(err) {
		return "", "", fmt.Errorf("feishu: upload target folder for %q: %w", remote, err)
	}

	// The first file of a new tree arrives before rclone's own mkdir pass.
	if mkErr := f.Mkdir(ctx, parentPath); mkErr != nil {
		return "", "", fmt.Errorf("feishu: create upload target folder for %q: %w", remote, mkErr)
	}
	parentToken, err = f.resolver.resolveDir(ctx, parentPath)
	if err != nil {
		return "", "", fmt.Errorf("feishu: upload target folder for %q: %w", remote, err)
	}
	return parentToken, name, nil
}

// uploaded builds the resulting Object and records the upload in the snapshot.
//
// Shared by both upload paths so the incremental logic cannot drift between
// them: a file uploaded in blocks must be skipped next run exactly like one
// uploaded in a single request.
func (f *Fs) uploaded(
	ctx context.Context,
	remote, name, parentToken, token string,
	size int64,
	src fs.ObjectInfo,
) *Object {
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

	// FR-027: remember what we sent so the next run can skip it.
	f.recordUpload(ctx, remote, src, size, token)

	return &Object{fs: f, remote: remote, entry: entry}
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

// Rmdir deletes a directory.
//
// dir is relative to the Fs root, exactly like Put's remote path — it must not
// be prefixed with f.root again (doing so produced "_inc_test/_inc_test" and
// every rmdir failed with "directory not found").
//
// Rmdir("") means "remove the Fs root folder itself", which rclone asks for
// when purging "remote:path". That is allowed, but only when the folder is
// already empty: Feishu deletes a folder recursively in a single call, so a
// non-empty folder would take its contents with it and there is no trash to
// recover from.
func (f *Fs) Rmdir(ctx context.Context, dir string) error {
	dir = normalizePath(dir)
	if !f.deletionsAllowed() {
		return ErrDeletionBlocked
	}

	// Rmdir(""): remove the Fs root folder itself. Its token comes straight
	// from resolveDir(""), because resolveDir works in "relative to the Fs root"
	// coordinates and cannot express the Fs root's own parent.
	if dir == "" {
		if f.root == "" {
			// The configured root_token folder is the user's drive folder, not
			// something weftsync created. Never delete it.
			return errors.New(
				"feishu: refusing to delete the root folder configured as root_token")
		}
		token, err := f.resolver.resolveDir(ctx, "")
		if err != nil {
			if isNotFound(err) {
				return fs.ErrorDirNotFound
			}
			return err
		}
		children, err := f.resolver.listFresh(ctx, token)
		if err != nil {
			return err
		}
		if len(children) > 0 {
			return fmt.Errorf(
				"feishu: refusing to delete folder %q: it still holds %d item(s). "+
					"Feishu deletes folders recursively and has no trash",
				f.root, len(children))
		}
		if err := f.client.deleteFolder(ctx, token); err != nil {
			return err
		}
		// The whole subtree just disappeared.
		f.resolver.invalidateAll()
		return nil
	}

	parentPath, name := parentOf(dir)
	parentToken, err := f.resolver.resolveDir(ctx, parentPath)
	if err != nil {
		if isNotFound(err) {
			return fs.ErrorDirNotFound
		}
		return err
	}

	// Fresh listing: the caller may have deleted the children moments ago and a
	// cached answer would still show them.
	entries, err := f.resolver.listFresh(ctx, parentToken)
	if err != nil {
		return err
	}

	token := ""
	for _, e := range entries {
		if e.IsDir && e.Name == name {
			token = e.Token
			break
		}
	}
	if token == "" {
		return fs.ErrorDirNotFound
	}

	children, err := f.resolver.listFresh(ctx, token)
	if err != nil {
		return err
	}
	if len(children) > 0 {
		return fmt.Errorf(
			"feishu: refusing to delete folder %q: it still holds %d item(s). "+
				"Feishu deletes folders recursively and has no trash",
			dir, len(children))
	}

	if err := f.client.deleteFolder(ctx, token); err != nil {
		return err
	}
	f.resolver.invalidate(parentToken)
	return nil
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
