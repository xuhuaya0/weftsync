package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/operations"
)

// Resolving one conflict, on request.
//
// The two actions are deliberately asymmetric in what they touch, because that is
// what makes them safe to offer as buttons:
//
//	ActionPush ("local wins")   edits only the sync state. The next run uploads the
//	                            local file over the remote one. Nothing in the
//	                            cloud changes now, so a mis-click costs one
//	                            redundant upload and nothing else.
//	ActionPull ("remote wins")  downloads the remote file over the local one, then
//	                            records it as the baseline. This is the only way
//	                            "accept the remote copy" can be true: the state's
//	                            local fingerprint is meant to describe a file whose
//	                            content the remote really has, so adopting a
//	                            fingerprint for a file we never copied would make
//	                            the snapshot claim an upload that never happened.
//
// Neither action deletes anything anywhere. Deleting is what the pair-level
// conflict policy and the delete guard are for.

// Conflict actions.
const (
	ActionPush = "push"
	ActionPull = "pull"
)

// ResolveConflict settles one path of one pair.
//
// pairName may be empty when the job has exactly one pair.
func ResolveConflict(ctx context.Context, cfg *Config, pairName, path, action string) error {
	p := findPair(cfg, pairName)
	if p == nil {
		return fmt.Errorf("no pair named %q in job %q", pairName, cfg.Job.Name)
	}
	clean, err := cleanConflictPath(path)
	if err != nil {
		return err
	}

	switch action {
	case ActionPush:
		return callBackendResolve(ctx, p, clean, map[string]string{"action": "forget"})
	case ActionPull:
		return pullConflict(ctx, p, clean)
	default:
		return fmt.Errorf("unknown conflict action %q (expected %q or %q)",
			action, ActionPush, ActionPull)
	}
}

// pullConflict downloads the remote file over the local one and adopts it.
func pullConflict(ctx context.Context, p *Pair, path string) error {
	remotePath, err := p.RemotePath()
	if err != nil {
		return err
	}
	fdst, err := fs.NewFs(ctx, remotePath)
	if err != nil {
		return fmt.Errorf("open destination %q: %w", remotePath, err)
	}
	fsrc, err := fs.NewFs(ctx, p.LocalPath())
	if err != nil {
		return fmt.Errorf("open source %q: %w", p.LocalPath(), err)
	}

	remoteObj, err := fdst.NewObject(ctx, path)
	if err != nil {
		return fmt.Errorf("read remote %q: %w", path, err)
	}
	if _, err := operations.Copy(ctx, fsrc, nil, path, remoteObj); err != nil {
		return fmt.Errorf("download %q: %w", path, err)
	}

	// rclone copies the source's modification time, which for this backend is
	// either the remembered one or the upload moment - both whole seconds. The
	// local filesystem keeps sub-second values, so an untruncated mtime would
	// differ from the recorded one by a fraction of a second and the next run
	// would upload the file right back. Truncating makes the comparison exact.
	if err := truncateMTime(filepath.Join(p.LocalPath(), filepath.FromSlash(path))); err != nil {
		return err
	}

	localInfo, err := fsrc.NewObject(ctx, path)
	if err != nil {
		return fmt.Errorf("re-read downloaded file %q: %w", path, err)
	}
	modTime := localInfo.ModTime(ctx)
	if modTime.IsZero() {
		return fmt.Errorf("the downloaded file %q has no usable modification time", path)
	}

	return callBackendResolve(ctx, p, path, map[string]string{
		"action": "adopt",
		"token":  objectToken(remoteObj),
		"mtime":  fmt.Sprintf("%d", modTime.Unix()),
		"size":   fmt.Sprintf("%d", localInfo.Size()),
	})
}

// truncateMTime drops the sub-second part of a local file's modification time.
func truncateMTime(localPath string) error {
	info, err := os.Stat(localPath)
	if err != nil {
		return fmt.Errorf("stat downloaded file: %w", err)
	}
	t := info.ModTime().Truncate(time.Second)
	if err := os.Chtimes(localPath, t, t); err != nil {
		return fmt.Errorf("set modification time on the downloaded file: %w", err)
	}
	return nil
}

// callBackendResolve invokes the backend's resolve command.
//
// The path travels in opt, not as the positional argument: rclone resolves the
// positional argument as an Fs, so a file name there fails with "is a file not a
// directory".
func callBackendResolve(ctx context.Context, p *Pair, path string, opt map[string]string) error {
	remotePath, err := p.RemotePath()
	if err != nil {
		return err
	}
	fdst, err := fs.NewFs(ctx, remotePath)
	if err != nil {
		return fmt.Errorf("open destination %q: %w", remotePath, err)
	}

	features := fdst.Features()
	if features == nil || features.Command == nil {
		return errors.New("this backend cannot resolve conflicts")
	}

	if opt == nil {
		opt = map[string]string{}
	}
	opt["path"] = path

	out, err := features.Command(ctx, "resolve", nil, opt)
	if err != nil {
		if errors.Is(err, fs.ErrorCommandNotFound) ||
			strings.Contains(err.Error(), "unknown backend command") {
			return errors.New("this backend does not support resolving conflicts one path at a time")
		}
		return err
	}

	// The command reports what it did; surface a refusal rather than letting the
	// caller believe a state edit happened.
	raw, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	var parsed struct {
		Changed bool   `json:"changed"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil
	}
	if !parsed.Changed {
		return fmt.Errorf("nothing was changed: %s", parsed.Summary)
	}
	return nil
}

// findPair picks the pair to act on.
func findPair(cfg *Config, name string) *Pair {
	if name == "" {
		if len(cfg.Pair) == 1 {
			return &cfg.Pair[0]
		}
		return nil
	}
	for i := range cfg.Pair {
		if cfg.Pair[i].Name == name {
			return &cfg.Pair[i]
		}
	}
	return nil
}

// cleanConflictPath validates a path coming from an HTTP request.
//
// The path is used to edit the snapshot and to write a local file, so it must not
// be able to reach outside the pair: a leading "/" would look like an absolute
// path to the backend, and ".." would let a request replace a file anywhere on
// disk. Both are checked rather than cleaned, because quietly rewriting what
// somebody asked for is how a "resolve" turns into an edit of a different file.
func cleanConflictPath(p string) (string, error) {
	trimmed := strings.TrimSpace(p)
	if trimmed == "" {
		return "", errors.New("a file path is required")
	}
	if strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, `\`) {
		return "", fmt.Errorf("path %q must be relative to the pair root", p)
	}
	if filepath.IsAbs(trimmed) {
		return "", fmt.Errorf("path %q must be relative to the pair root", p)
	}
	for _, part := range strings.FieldsFunc(trimmed, func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		if part == ".." {
			return "", fmt.Errorf("path %q must not contain ..", p)
		}
	}
	return path2slash(trimmed), nil
}

// path2slash normalises separators the way the rest of the package does.
func path2slash(p string) string { return filepath.ToSlash(p) }
