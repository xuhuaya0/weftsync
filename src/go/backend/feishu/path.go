package feishu

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
)

// tokenExtractionHosts are the URL shapes accepted for root_token, so users can
// paste a folder URL straight from the Feishu client instead of hunting for the
// token. Verified shapes:
//
//	https://xxx.feishu.cn/drive/folder/KARRfvoYtlX9T3dOoaTcmdELnyv
//	https://xxx.feishu.cn/drive/folder/KARRfvoYtlX9T3dOoaTcmdELnyv?from=space
//	KARRfvoYtlX9T3dOoaTcmdELnyv
func ExtractFolderToken(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	lower := strings.ToLower(value)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return value
	}

	// Strip scheme then query/fragment, then walk the path segments.
	rest := value
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	if i := strings.IndexAny(rest, "?#"); i >= 0 {
		rest = rest[:i]
	}
	slash := strings.Index(rest, "/")
	if slash < 0 {
		return ""
	}
	parts := strings.Split(strings.Trim(rest[slash+1:], "/"), "/")
	if len(parts) == 0 {
		return ""
	}

	for i, p := range parts {
		if p == "folder" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	if len(parts) >= 2 && parts[0] == "drive" && parts[1] != "folder" && parts[1] != "file" {
		return parts[1]
	}
	return parts[len(parts)-1]
}

// normalizePath converts a caller-supplied path into the canonical form stored
// in state and used for comparisons (FR-026, data-model.md §2.3):
//
//   - separators become "/"
//   - no leading or trailing "/"
//   - case is preserved (Feishu is case sensitive even on Windows)
//   - no Unicode normalisation (byte-identical to the local file system)
//
// The separator conversion is platform-aware. A backslash is a separator only on
// Windows; on Linux it is a legal filename character, and rewriting it split a
// file literally named "weird\name.txt" into a directory "weird" plus "name.txt",
// addressing a different path than the caller asked for. filepath.ToSlash does
// the right thing on both platforms.
func normalizePath(p string) string {
	p = filepath.ToSlash(p)
	p = strings.Trim(p, "/")
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	return p
}

// joinPath appends one segment to a logical path.
func joinPath(base, name string) string {
	base = normalizePath(base)
	name = normalizePath(name)
	switch {
	case base == "":
		return name
	case name == "":
		return base
	default:
		return base + "/" + name
	}
}

// parentOf returns everything before the last "/", and the trailing name.
func parentOf(p string) (parent, name string) {
	p = normalizePath(p)
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[:i], p[i+1:]
	}
	return "", p
}

// dirResolver maps logical paths to folder tokens.
//
// Lookups hit the API only on a cache miss, and the cache lives for the
// duration of one command run. Persisting it would be F4's job; doing it here
// would introduce stale-token bugs for no benefit (research.md R4).
type dirResolver struct {
	// rootToken is the folder the remote points at.
	rootToken string
	// rootPath is the logical prefix corresponding to rootToken (may be empty).
	rootPath string
	// basePath is the directory the remote was created at, used to translate
	// rclone's relative remotes into our logical paths.
	basePath string

	client *client

	mu    sync.Mutex
	cache map[string][]remoteEntry // folder token -> entries
	// inflight merges concurrent requests for the same folder.
	inflight map[string]*listCall
	// gen is bumped by every invalidate. A listing that started before the bump
	// must not store its result, otherwise its stale snapshot would silently
	// resurrect the entry that invalidate just removed.
	gen uint64
}

// listCall tracks one in-flight listing so concurrent callers can share it.
type listCall struct {
	done    chan struct{}
	entries []remoteEntry
	err     error
}

func newDirResolver(c *client, rootToken, rootPath, basePath string) *dirResolver {
	return &dirResolver{
		rootToken: rootToken,
		rootPath:  normalizePath(rootPath),
		basePath:  normalizePath(basePath),
		client:    c,
		cache:     make(map[string][]remoteEntry),
		inflight:  make(map[string]*listCall),
	}
}

// listCached returns the children of a folder token, reusing a previous result.
//
// Concurrent callers asking for the same folder share one request. Without
// that coalescing, rclone's parallel checkers would each listing the same
// folder, burning through the documented 20 QPS listing budget for nothing.
func (r *dirResolver) listCached(ctx context.Context, token string) ([]remoteEntry, error) {
	r.mu.Lock()
	if entries, ok := r.cache[token]; ok {
		r.mu.Unlock()
		return entries, nil
	}
	if call, ok := r.inflight[token]; ok {
		r.mu.Unlock()
		<-call.done
		return call.entries, call.err
	}
	call := &listCall{done: make(chan struct{})}
	r.inflight[token] = call
	startedAt := r.gen
	r.mu.Unlock()

	entries, err := r.client.listFolder(ctx, token)

	r.mu.Lock()
	delete(r.inflight, token)
	// Only cache when nothing was invalidated while we were waiting: our result
	// predates that write and would hide it.
	if err == nil && r.gen == startedAt {
		r.cache[token] = entries
	}
	call.entries, call.err = entries, err
	r.mu.Unlock()

	close(call.done)
	return entries, err
}

// listFresh bypasses both the cache and any in-flight request.
//
// Used to confirm a "not found" result: the earlier listing may predate a
// concurrent createFolder, and joining its in-flight call would repeat the same
// stale answer.
func (r *dirResolver) listFresh(ctx context.Context, token string) ([]remoteEntry, error) {
	entries, err := r.client.listFolder(ctx, token)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.cache[token] = entries
	r.mu.Unlock()
	return entries, nil
}

// invalidate drops one folder from the cache (after a write mutates it).
func (r *dirResolver) invalidate(token string) {
	r.mu.Lock()
	delete(r.cache, token)
	r.gen++
	r.mu.Unlock()
}

// invalidateAll clears the whole cache, used when the Fs root changes.
func (r *dirResolver) invalidateAll() {
	r.mu.Lock()
	r.cache = make(map[string][]remoteEntry)
	r.gen++
	r.mu.Unlock()
}

// resolveDir returns the folder token for a path expressed relative to the Fs
// root.
//
// An empty path means "the Fs root", which still has to be walked when the
// remote was created below root_token (rclone passes e.g. root="a/b" when the
// user runs `mkdir remote:a/b`).
//
// A "not found" answer is re-checked once against a fresh listing. rclone
// uploads several files in parallel, so a folder created a moment ago by
// another goroutine may be missing from a listing that was already in flight.
func (r *dirResolver) resolveDir(ctx context.Context, rel string) (string, error) {
	full := joinPath(r.rootPath, rel)

	token, err := r.walk(ctx, full)
	if err == nil {
		return token, nil
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != CodeParentNotFound {
		return "", err
	}
	return r.walkWith(ctx, full, r.listFresh)
}

// walk starts at rootToken and follows each segment of full.
//
// full is relative to rootToken and must already be normalised.
func (r *dirResolver) walk(ctx context.Context, full string) (string, error) {
	return r.walkWith(ctx, full, r.listCached)
}

// walkWith is walk with a pluggable lister, so a miss can be confirmed against
// an uncached listing.
func (r *dirResolver) walkWith(
	ctx context.Context,
	full string,
	list func(context.Context, string) ([]remoteEntry, error),
) (string, error) {
	if full == "" {
		return r.rootToken, nil
	}

	current := r.rootToken
	walked := ""
	for _, segment := range strings.Split(full, "/") {
		if segment == "" {
			continue
		}
		entries, err := list(ctx, current)
		if err != nil {
			return "", err
		}
		found := ""
		for _, e := range entries {
			if e.IsDir && e.Name == segment {
				found = e.Token
				break
			}
		}
		if found == "" {
			return "", &APIError{
				Code: CodeParentNotFound,
				Msg: fmt.Sprintf("folder %q not found inside %q",
					segment, orRoot(walked)),
				Where: "resolveDir",
			}
		}
		current = found
		walked = joinPath(walked, segment)
	}
	return current, nil
}

// orRoot renders an empty path readably in error messages.
func orRoot(p string) string {
	if p == "" {
		return "the remote root"
	}
	return p
}

// resolveEntry looks up a single path and reports the matching entry.
//
// When several entries share the name (Feishu allows it), pickRemoteEntry
// decides which one is authoritative.
func (r *dirResolver) resolveEntry(ctx context.Context, rel string) (*remoteEntry, error) {
	rel = normalizePath(rel)
	if rel == "" {
		return nil, nil // the root itself is a directory, not an object
	}

	parentRel, name := parentOf(rel)
	parentToken, err := r.resolveDir(ctx, parentRel)
	if err != nil {
		return nil, err
	}

	entries, err := r.listCached(ctx, parentToken)
	if err != nil {
		return nil, err
	}

	var matches []remoteEntry
	for _, e := range entries {
		if e.Name == name {
			matches = append(matches, e)
		}
	}
	if len(matches) == 0 {
		return nil, nil
	}

	chosen := pickRemoteEntry(matches, "")
	return &chosen, nil
}

// pickRemoteEntry chooses one entry when a folder holds duplicate names.
//
// Feishu permits duplicates, and rclone documents that duplicate files confuse
// its sync engine, so List must collapse them (research.md R5):
//
//  1. if we have a remembered token for this path, prefer it — that is the copy
//     we created and can safely replace
//  2. otherwise prefer the most recently created one, which matches the
//     intuition "the newest upload wins"
//
// Returning several would make the sync engine pick arbitrarily.
func pickRemoteEntry(matches []remoteEntry, preferredToken string) remoteEntry {
	if preferredToken != "" {
		for _, m := range matches {
			if m.Token == preferredToken {
				return m
			}
		}
	}
	best := matches[0]
	for _, m := range matches[1:] {
		if m.CreatedTime > best.CreatedTime {
			best = m
		} else if m.CreatedTime == best.CreatedTime && m.ModTime > best.ModTime {
			best = m
		}
	}
	return best
}
