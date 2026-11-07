package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/rclone/rclone/fs"
)

// moveNode moves a file or folder to another folder.
//
// Verified by probing the live tenant (.scratch/probe_move.py):
//   - moving a FILE completes synchronously and the response carries no task_id
//   - moving a FOLDER is asynchronous and returns a task_id we do not wait for
//   - the node keeps its token across a move, so recorded tokens stay valid
//   - the endpoint is limited to 20 calls per MINUTE, far below the 5 QPS of the
//     other endpoints, which is why callers must not fan out
//
// kind is "file" or "folder"; Feishu rejects a mismatch with the real type.
func (c *client) moveNode(ctx context.Context, token, kind, destFolderToken string) (taskID string, err error) {
	const where = "files.move"

	body, err := json.Marshal(struct {
		Type        string `json:"type"`
		FolderToken string `json:"folder_token"`
	}{Type: kind, FolderToken: destFolderToken})
	if err != nil {
		return "", fmt.Errorf("%s: encode request: %w", where, err)
	}

	res, err := c.do(ctx, request{
		where:       where,
		method:      http.MethodPost,
		path:        "/open-apis/drive/v1/files/" + url.PathEscape(token) + "/move",
		body:        body,
		contentType: "application/json; charset=utf-8",
		// Move has its own, much tighter budget (20/minute), so it must not share
		// the general limiter's allowance.
		limiter: c.moveLimiter,
	})
	if err != nil {
		return "", err
	}

	var payload struct {
		TaskID string `json:"task_id"`
	}
	if len(bytes.TrimSpace(res.Data)) > 0 {
		// A folder move answers with a task id; a file move answers {}.
		if err := json.Unmarshal(res.Data, &payload); err != nil {
			return "", fmt.Errorf("%s: decode: %w", where, err)
		}
	}
	return payload.TaskID, nil
}

// Move renames or relocates an object within the same remote, server side.
//
// Why this matters beyond the obvious: rclone's --backup-dir (the mechanism
// behind deletion=versioning) moves old versions aside. Without a server-side
// Move it would download and re-upload every file it retires, which for a cloud
// destination means paying the full transfer cost twice. The probe confirmed the
// token survives the move, so the incremental snapshot stays valid.
//
// Path frames are the trap here: `remote` is relative to THIS Fs's root, while
// src.Remote() is relative to the source Fs's root, and rclone routinely builds
// the two with different roots (moveto "a/x" "a/sub/x" gives the destination Fs
// root "sub"). Comparing those two strings directly made every such move look
// like a no-op, so the object never moved; the caller still reported success.
// Both sides are therefore expanded to absolute paths before being compared.
func (f *Fs) Move(ctx context.Context, src fs.Object, remote string) (fs.Object, error) {
	srcObj, ok := src.(*Object)
	if !ok {
		return nil, fmt.Errorf("feishu: cannot move a %T", src)
	}

	dest := normalizePath(remote)
	if dest == "" {
		return nil, fmt.Errorf("feishu: move requires a destination path")
	}

	srcRoot := ""
	if srcObj.fs != nil {
		srcRoot = srcObj.fs.root
	}
	srcAbs := joinPath(srcRoot, srcObj.remote)
	dstAbs := joinPath(f.root, dest)

	if srcAbs == dstAbs {
		return srcObj, nil // already there
	}

	srcParentAbs, srcName := parentOf(srcAbs)
	dstParentRel, dstName := parentOf(dest)

	needRelocate := joinPath(f.root, dstParentRel) != srcParentAbs
	needRename := srcName != dstName

	dstParentToken, err := f.resolver.resolveDir(ctx, dstParentRel)
	if err != nil {
		if !isNotFound(err) {
			return nil, fmt.Errorf("feishu: move %q to %q: %w", srcAbs, dstAbs, err)
		}
		if err := f.Mkdir(ctx, dstParentRel); err != nil {
			return nil, fmt.Errorf("feishu: create destination folder for %q: %w", dstAbs, err)
		}
		if dstParentToken, err = f.resolver.resolveDir(ctx, dstParentRel); err != nil {
			return nil, fmt.Errorf("feishu: move %q to %q: %w", srcAbs, dstAbs, err)
		}
	}

	token := srcObj.entry.Token
	if token == "" {
		return nil, fmt.Errorf("feishu: cannot move %q: it has no token", srcObj.remote)
	}

	if needRelocate {
		if _, err := f.client.moveNode(ctx, token, "file", dstParentToken); err != nil {
			return nil, fmt.Errorf("feishu: move %q to %q: %w", srcAbs, dstAbs, err)
		}
		f.resolver.invalidate(srcObj.entry.ParentToken)
		f.resolver.invalidate(dstParentToken)
	}

	if needRename {
		// Feishu has no rename call: re-upload under the new name, then drop the
		// original. Upload first so a failure leaves the data reachable.
		content, err := srcObj.readAll(ctx)
		if err != nil {
			return nil, fmt.Errorf("feishu: rename %q to %q: %w", srcAbs, dstAbs, err)
		}
		newToken, err := f.client.uploadAll(ctx, dstParentToken, dstName, content)
		if err != nil {
			return nil, fmt.Errorf("feishu: rename %q to %q: upload failed: %w", srcAbs, dstAbs, err)
		}
		// The token now lives under dstParentRel in both cases: either it was
		// relocated there, or the parents were already the same.
		if f.deletionsAllowed() && f.tokenStillPresent(ctx, dstParentRel, token) {
			if err := f.client.deleteFile(ctx, token); err != nil {
				return nil, fmt.Errorf(
					"feishu: rename %q to %q: new copy uploaded as %s but the "+
						"original %s could not be removed: %w",
					srcObj.remote, dest, newToken, token, err)
			}
		}
		f.resolver.invalidate(dstParentToken)

		newObj := &Object{fs: f, remote: dest, entry: remoteEntry{
			Name:        dstName,
			Token:       newToken,
			ParentToken: dstParentToken,
			ModTime:     srcObj.entry.ModTime,
			CreatedTime: srcObj.entry.CreatedTime,
		}}
		// recordMoved both drops the old path and records the new one; do not
		// call forgetPath first or there would be nothing left to carry over.
		f.recordMoved(ctx, srcObj, dest, newObj)
		return newObj, nil
	}

	// Pure relocation: same name, different folder. The token is unchanged
	// (verified), so the snapshot entry only needs its path moved.
	moved := &Object{fs: f, remote: dest, entry: srcObj.entry}
	moved.entry.ParentToken = dstParentToken
	f.recordMoved(ctx, srcObj, dest, moved)
	return moved, nil
}

// recordMoved carries the snapshot entry over to the new path.
//
// Without this the next run would see the old path as "changed" and the new path
// as "new", re-uploading a file that only moved.
func (f *Fs) recordMoved(ctx context.Context, from *Object, dest string, to *Object) {
	if f.st == nil {
		return
	}
	f.stateMu.Lock()
	e, ok := f.st.Lookup(from.remote)
	f.st.Delete(from.remote)
	if ok {
		e.Path = dest
		e.RemoteToken = to.entry.Token
		f.st.Entries[dest] = e
	}
	f.stateMu.Unlock()
	f.flushState()
}
