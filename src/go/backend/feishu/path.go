package feishu

import (
	"context"
	"fmt"
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
func normalizePath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
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
	r.mu.Unlock()

	entries, err := r.client.listFolder(ctx, token)

	r.mu.Lock()
	delete(r.inflight, token)
	if err == nil {
		r.cache[token] = entries
	}
	call.entries, call.err = entries, err
	r.mu.Unlock()

	close(call.done)
	return entries, err
}

// invalidate drops one folder from the cache (after a write mutates it).
func (r *dirResolver) invalidate(token string) {
	r.mu.Lock()
	delete(r.cache, token)
	r.mu.Unlock()
}

// invalidateAll clears the whole cache, used when the Fs root changes.
func (r *dirResolver) invalidateAll() {
	r.mu.Lock()
	r.cache = make(map[string][]remoteEntry)
	r.mu.Unlock()
}

// resolveDir returns the folder token for a path expressed relative to the Fs
// root.
//
// An empty path means "the Fs root", which still has to be walked when the
// remote was created below root_token (rclone passes e.g. root="a/b" when the
// user runs `mkdir remote:a/b`).
func (r *dirResolver) resolveDir(ctx context.Context, rel string) (string, error) {
	return r.walk(ctx, joinPath(r.rootPath, rel))
}

// walk starts at rootToken and follows each segment of full.
//
// full is relative to rootToken and must already be normalised.
func (r *dirResolver) walk(ctx context.Context, full string) (string, error) {
	if full == "" {
		return r.rootToken, nil
	}

	current := r.rootToken
	for _, segment := range strings.Split(full, "/") {
		if segment == "" {
			continue
		}
		entries, err := r.listCached(ctx, current)
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
				Code:  CodeParentNotFound,
				Msg:   fmt.Sprintf("folder %q not found under %q", segment, full),
				Where: "resolveDir",
			}
		}
		current = found
	}
	return current, nil
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
