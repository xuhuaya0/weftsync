package feishu

import (
	"bytes"
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

func (o *Object) Fs() fs.Info                { return o.fs }
func (o *Object) Remote() string             { return o.remote }
func (o *Object) String() string             { return o.remote }
func (o *Object) Storable() bool             { return true }
func (o *Object) ModTime(ctx context.Context) time.Time {
	return time.Unix(o.entry.ModTime, 0)
}

// Size is unknown (-1).
//
// Feishu's listing omits size (E3). Recovering it would cost one Range request
// per file, which is 500 requests for a 500-file folder — research.md R6
// explains why we accept -1 instead and let the state file drive comparisons.
// rclone's sizeDiffers treats Size() < 0 as "not comparable" (verified with
// probes/e6_size_probe.go), so nothing breaks.
func (o *Object) Size() int64 { return -1 }

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
func (o *Object) Open(ctx context.Context, options ...fs.OpenOption) (io.ReadCloser, error) {
	rangeHeader := ""
	var offset, limit int64 = 0, -1

	for _, option := range options {
		switch opt := option.(type) {
		case *fs.RangeOption:
			start, end := opt.Start, opt.End
			if start < 0 && end >= 0 {
				// Suffix range: last N bytes. Feishu accepts "bytes=-N".
				rangeHeader = fmt.Sprintf("bytes=-%d", end)
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

	res, err := o.fs.client.download(ctx, o.entry.Token, rangeHeader)
	if err != nil {
		return nil, err
	}

	body := res.Body
	if limit >= 0 && int64(len(body)) > limit {
		body = body[:limit]
	}
	if offset > 0 && rangeHeader == "" && int64(len(body)) > offset {
		// Server ignored the range and sent the whole file.
		body = body[offset:]
	}

	return io.NopCloser(bytes.NewReader(body)), nil
}

// Update replaces this object's content.
//
// Feishu offers no in-place update: re-uploading a name always creates a NEW
// token while the old file stays in the folder (E1 + E2). So "replace" must be
// performed as two steps, and the order matters:
//
//	1. upload the new content  -> new token
//	2. delete this object's old token
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

	// Step 1: upload the replacement.
	newObj, err := o.fs.Put(ctx, in, src, options...)
	if err != nil {
		return fmt.Errorf("feishu: replace %q: upload failed, old file left untouched: %w", remote, err)
	}
	newToken := newObj.(*Object).entry.Token

	// Step 2: drop the previous token, but only if it still exists.
	//
	// FR-035: re-verify before deleting. A stale token in our entry would
	// otherwise delete an unrelated file, and deletion is irreversible.
	oldToken := o.entry.Token
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
	if err := o.fs.client.deleteFile(ctx, o.entry.Token); err != nil {
		return err
	}
	o.fs.resolver.invalidate(o.entry.ParentToken)
	return nil
}
