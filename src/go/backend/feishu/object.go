package feishu

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/hash"
)

// Object is one file in Feishu Drive.
type Object struct {
	fs     *Fs
	remote string // logical path relative to the Fs root
	entry  remoteEntry
}

var _ fs.Object = (*Object)(nil)

// ---------------------------------------------------------------- ObjectInfo

func (o *Object) Fs() fs.Info    { return o.fs }
func (o *Object) Remote() string { return o.remote }
func (o *Object) String() string { return o.remote }
func (o *Object) Storable() bool { return true }

// ModTime returns the local modification time the file had when we uploaded it,
// as long as the remote entry still carries the token we recorded.
//
// This is the whole point of the state file: rclone compares the source's
// modtime against this value, so returning the original local mtime makes an
// unchanged file look equal and it is skipped. Returning the remote's own
// modified_time (the upload moment) would make every file look different on
// every run.
func (o *Object) ModTime(ctx context.Context) time.Time {
	if mtime, _, ok := o.fs.stateFingerprint(o.remote, o.entry.Token); ok {
		return time.Unix(mtime, 0)
	}
	return time.Unix(o.entry.ModTime, 0)
}

// Size returns the local byte count the file had when we uploaded it, when the
// snapshot still describes this remote entry.
//
// Otherwise it is -1 (unknown): Feishu's listing omits size (E3). Recovering it
// would cost one Range request per file, which is 500 requests for a 500-file
// folder — research.md R6 explains why we accept -1 instead. rclone's
// sizeDiffers treats Size() < 0 as "not comparable" (verified with
// probes/e6_size_probe.go), so nothing breaks.
func (o *Object) Size() int64 {
	if _, size, ok := o.fs.stateFingerprint(o.remote, o.entry.Token); ok {
		return size
	}
	return -1
}

// Hash always returns "" apart from the None check: Feishu has no server-side
// content hash (E3).
func (o *Object) Hash(ctx context.Context, ty hash.Type) (string, error) {
	if ty == hash.None {
		return "", nil
	}
	return "", hash.ErrUnsupported
}

// ---------------------------------------------------------------- fs.Object

// SetModTime is unsupported: the API offers no way to set modified_time, which
// always reflects the upload moment.
func (o *Object) SetModTime(ctx context.Context, t time.Time) error {
	return fs.ErrorCantSetModTime
}

// Open streams the file content, honouring a byte range when rclone asks for
// one. Feishu answers ranged GETs with 206 + Content-Range (E3+).
//
// The body is passed through, not buffered: downloading a 1GB file must not need
// 1GB of memory, and buffering through a size-capped reader is what silently
// truncated every download above 8MB.
func (o *Object) Open(ctx context.Context, options ...fs.OpenOption) (io.ReadCloser, error) {
	rangeHeader := ""
	suffix := false
	var offset, limit int64 = 0, -1

	for _, option := range options {
		switch opt := option.(type) {
		case *fs.RangeOption:
			start, end := opt.Start, opt.End
			if start < 0 && end >= 0 {
				// Suffix range: last N bytes. Feishu accepts "bytes=-N".
				rangeHeader = fmt.Sprintf("bytes=-%d", end)
				suffix = true
				offset, limit = 0, end
				continue
			}
			if start < 0 {
				continue
			}
			if end >= 0 {
				rangeHeader = fmt.Sprintf("bytes=%d-%d", start, end)
				limit = end - start + 1
			} else {
				rangeHeader = fmt.Sprintf("bytes=%d-", start)
			}
			offset = start

		case *fs.SeekOption:
			if opt.Offset > 0 {
				rangeHeader = fmt.Sprintf("bytes=%d-", opt.Offset)
				offset = opt.Offset
			}
		}
	}

	res, err := o.fs.client.downloadStream(ctx, o.entry.Token, rangeHeader)
	if err != nil {
		return nil, err
	}
	body := res.Body

	// A server that ignores Range replies 200 with the whole file, so the window
	// has to be cut out here. When the reply was a 206 the body already holds
	// only the requested window and must be passed through untouched.
	if !res.Partial && rangeHeader != "" {
		if suffix {
			// Taking the last N bytes needs the total size, and streaming cannot
			// know it in advance. Returning the first N bytes instead would be
			// silently wrong, so refuse.
			_ = body.Close()
			return nil, fmt.Errorf(
				"feishu: %q: the server ignored the suffix range %q, so the last %d bytes "+
					"cannot be located while streaming",
				o.remote, rangeHeader, limit)
		}
		if offset > 0 {
			if n, err := io.CopyN(io.Discard, body, offset); err != nil || n < offset {
				_ = body.Close()
				return nil, fmt.Errorf("feishu: %q: seeking to offset %d failed after %d bytes: %w",
					o.remote, offset, n, err)
			}
		}
	}

	if limit >= 0 {
		return &limitedReadCloser{Reader: io.LimitReader(body, limit), closer: body}, nil
	}
	return body, nil
}

// limitedReadCloser caps how many bytes the caller can read while still closing
// the underlying stream.
type limitedReadCloser struct {
	io.Reader
	closer io.Closer
}

func (l *limitedReadCloser) Close() error { return l.closer.Close() }

// readAll downloads the whole object. Used by rename, which has no server-side
// equivalent on Feishu (the move API relocates but does not rename).
func (o *Object) readAll(ctx context.Context) ([]byte, error) {
	rc, err := o.Open(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	return io.ReadAll(rc)
}

// Update replaces this object's content.
//
// Feishu offers no in-place update: re-uploading a name always creates a NEW
// token while the old file stays in the folder (E1 + E2). So "replace" must be
// performed as two steps, and the order matters:
//
//  1. upload the new content  -> new token
//  2. delete this object's old token
//
// Uploading first means a failed upload leaves the old file intact. Deleting
// first would destroy data on failure, and the API has no trash to recover
// from (verified during M0). This mirrors data-model.md §3.
//
// rclone calls Update (rather than Put) whenever a destination already exists:
// checkPartial forces inplace when Features.Move is nil, and updateOrPut then
// routes to dst.Update.
func (o *Object) Update(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) error {
	remote := o.remote
	parentPath, _ := parentOf(remote)

	// Resolve the token to delete BEFORE uploading.
	//
	// Put() records the new token in the snapshot when it succeeds, so reading
	// "the previous token" afterwards would return the new one, make
	// oldToken == newToken, and silently skip the deletion — leaving a duplicate
	// behind. That is invisible in a listing because List collapses duplicate
	// names, so this bug survived a full round of manual verification.
	//
	// The snapshot is the better witness than the listing: pickRemoteEntry may
	// have collapsed two same-named files into whichever came first.
	oldToken := o.entry.Token
	if remembered, ok := o.fs.previousToken(remote); ok {
		oldToken = remembered
	}

	// Step 1: upload the replacement.
	newObj, err := o.fs.Put(ctx, in, src, options...)
	if err != nil {
		return fmt.Errorf("feishu: replace %q: upload failed, old file left untouched: %w", remote, err)
	}
	newToken := newObj.(*Object).entry.Token

	// Repair mode: we have no trustworthy history, so we cannot prove the token
	// below names our own previous version. Deleting would be irreversible.
	if !o.fs.deletionsAllowed() {
		fs.Infof(o, "state was rebuilt; leaving the previous version of %q in place", remote)
		o.entry = newObj.(*Object).entry
		return nil
	}

	// Step 2: drop the previous token, but only if it still exists.
	//
	// FR-035: re-verify before deleting. A stale token in our entry would
	// otherwise delete an unrelated file, and deletion is irreversible.
	if oldToken != "" && oldToken != newToken {
		if !o.fs.tokenStillPresent(ctx, parentPath, oldToken) {
			fs.Infof(o, "previous version (token %s) is gone; nothing to delete", oldToken)
		} else if err := o.fs.client.deleteFile(ctx, oldToken); err != nil {
			return fmt.Errorf(
				"feishu: replace %q: new version uploaded as token %s, "+
					"but deleting the previous version %s failed: %w",
				remote, newToken, oldToken, err)
		} else {
			fs.Infof(o, "replaced: uploaded token %s, removed previous token %s", newToken, oldToken)
			o.fs.resolver.invalidate(o.entry.ParentToken)
		}
	}

	o.entry = newObj.(*Object).entry
	return nil
}

// Remove deletes this file.
//
// Irreversible: the tenant token has no trash endpoint (verified in M0).
// Callers must have verified that o.entry.Token still exists (FR-035).
func (o *Object) Remove(ctx context.Context) error {
	if o.entry.Token == "" {
		return errors.New("feishu: cannot remove an object without a token")
	}
	// Repair mode: no trustworthy history, so no deletion. See state.go.
	if !o.fs.deletionsAllowed() {
		return ErrDeletionBlocked
	}
	if err := o.fs.client.deleteFile(ctx, o.entry.Token); err != nil {
		return err
	}
	o.fs.resolver.invalidate(o.entry.ParentToken)
	o.fs.forgetPath(o.remote)
	return nil
}
