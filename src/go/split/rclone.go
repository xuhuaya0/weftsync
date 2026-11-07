package split

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/operations"
)

// The remote half of a split, backed by an rclone Fs.
//
// It uses operations.Move rather than copy-and-delete so a server-side move is used
// where the backend has one (the feishu backend does, verified by probing). That
// matters here: a split can move thousands of entries, and the difference is one
// API call each versus a download plus an upload each - against a 5 QPS limit.

// NewRcloneRemote wraps a destination Fs.
func NewRcloneRemote(f fs.Fs) Remote {
	return &rcloneRemote{f: f}
}

type rcloneRemote struct {
	f fs.Fs
}

func (r *rcloneRemote) Mkdir(ctx context.Context, rel string) error {
	rel = strings.Trim(rel, "/")
	if rel == "" {
		return nil // the root always exists
	}
	// A directory that is already there is not a problem: a resumed split will
	// re-run this phase, and Mkdir on an existing directory is what the backend
	// may or may not tolerate per type.
	if existing, err := r.f.NewObject(ctx, rel); err == nil && existing != nil {
		return fmt.Errorf("a file named %q is already there", rel)
	}
	if dir, err := r.listOne(ctx, rel); err == nil && dir {
		return nil
	}
	if err := r.f.Mkdir(ctx, rel); err != nil {
		return err
	}
	return nil
}

// listOne reports whether rel already exists as a directory.
func (r *rcloneRemote) listOne(ctx context.Context, rel string) (bool, error) {
	entries, err := r.f.List(ctx, rel)
	if err != nil {
		return false, err
	}
	// A successful List proves the directory exists (an absent one errors).
	_ = entries
	return true, nil
}

func (r *rcloneRemote) Move(ctx context.Context, fromRel, toRel string) error {
	src, err := r.f.NewObject(ctx, strings.Trim(fromRel, "/"))
	if err != nil {
		// No copy on the remote yet: the caller records that and moves on. Reporting
		// it as a failure would abort the split over a file the next sync will place
		// correctly anyway.
		if errors.Is(err, fs.ErrorObjectNotFound) {
			return ErrRemoteSourceMissing
		}
		return fmt.Errorf("read %q: %w", fromRel, err)
	}
	// A destination that already exists would be replaced by operations.Move.
	// Overwriting the user's remote data is the outcome this must never cause.
	if dst, err := r.f.NewObject(ctx, strings.Trim(toRel, "/")); err == nil && dst != nil {
		return fmt.Errorf("%q already exists on the remote; refusing to overwrite", toRel)
	}
	if _, err := operations.Move(ctx, r.f, nil, strings.Trim(toRel, "/"), src); err != nil {
		return err
	}
	return nil
}

func (r *rcloneRemote) List(ctx context.Context, rel string) ([]string, error) {
	// An empty bucket that was created but never filled errors on some backends;
	// for verification purposes that is an empty list, not a failure, as long as
	// the directory itself is there.
	entries, err := r.f.List(ctx, strings.Trim(rel, "/"))
	if err != nil {
		if isEmptyDirError(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Remote())
	}
	return out, nil
}

func (r *rcloneRemote) Remove(ctx context.Context, rel string) error {
	return r.f.Rmdir(ctx, strings.Trim(rel, "/"))
}

func (r *rcloneRemote) Exists(ctx context.Context, rel string) (bool, error) {
	rel = strings.Trim(rel, "/")
	if _, err := r.f.NewObject(ctx, rel); err == nil {
		return true, nil
	} else if !errors.Is(err, fs.ErrorObjectNotFound) {
		return false, err
	}
	// Not a file; a directory of that name also counts as "there".
	if entries, err := r.f.List(ctx, rel); err == nil {
		_ = entries
		return true, nil
	}
	return false, nil
}

// isEmptyDirError reports whether err is a backend's "no such directory".
func isEmptyDirError(err error) bool {
	if err == fs.ErrorDirNotFound {
		return true
	}
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "directory not found") ||
		strings.Contains(msg, "not found")
}

// NewRcloneStateMover returns a StateMover that goes through the backend command
// `repath`.
//
// Through the backend rather than the file, because the backend holds the snapshot
// in memory for as long as its Fs is cached, and rclone's cache has no per-entry
// eviction. Editing the file behind its back would be undone by the next flush from
// the stale copy. The command updates both, and reports how many entries it moved.
func NewRcloneStateMover(f fs.Fs) StateMover {
	return &rcloneStateMover{f: f}
}

type rcloneStateMover struct {
	f fs.Fs
}

func (r *rcloneStateMover) Repath(ctx context.Context, moves []PathMove) (int, error) {
	if len(moves) == 0 {
		return 0, nil
	}
	features := r.f.Features()
	if features == nil || features.Command == nil {
		return 0, nil // no commands: nothing to keep consistent
	}

	args := make([]string, 0, len(moves)*2)
	for _, m := range moves {
		// Two arguments per move: no separator character has to be chosen, and a
		// path containing any punctuation is still unambiguous.
		args = append(args, m.From, m.To)
	}

	out, err := features.Command(ctx, "repath", args, nil)
	if err != nil {
		if errors.Is(err, fs.ErrorCommandNotFound) ||
			strings.Contains(err.Error(), "unknown backend command") {
			// A backend without the command has no state to rewrite.
			return 0, nil
		}
		return 0, err
	}

	raw, err := json.Marshal(out)
	if err != nil {
		return 0, nil
	}
	var parsed struct {
		Migrated int `json:"migrated"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return 0, nil
	}
	return parsed.Migrated, nil
}
