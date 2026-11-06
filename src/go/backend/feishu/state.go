package feishu

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/rclone/rclone/fs"
	"weftsync/state"
)

// This file wires the state snapshot into the backend.
//
// Why the state file is needed at all: Feishu reports no size and its
// modified_time is the upload moment (PLAN.md §3). Comparing the local mtime
// against that value therefore always says "different", so every run would
// re-upload every file. Instead we remember what the local file looked like
// when we uploaded it, and present *that* fingerprint to rclone through
// Object.ModTime / Object.Size. rclone's normal comparison then skips unchanged
// files without any change to its engine.
//
// The token check is what makes this safe: a remembered fingerprint is only
// trusted while the remote entry still carries the token we recorded. If
// someone replaced or removed the remote file, the token no longer matches and
// we fall back to the real (upload-time) modified_time, which forces a fresh
// upload.

// loadState reads the snapshot, or prepares an empty one.
//
// A missing file means "first run": an empty snapshot is correct.
// A corrupt file means "we have no trustworthy baseline": we enter read-only
// repair mode, which forbids every deletion this run
// (contracts/state-file-format.md §5).
func (f *Fs) loadState() {
	f.stPath = f.opt.StateFile
	if f.stPath == "" {
		return // stateless mode: works, but re-uploads everything each run
	}

	pairID := state.PairIDFromPath(f.stPath)

	st, err := state.Load(f.stPath)
	if err != nil {
		// Never abort the run: the user's files still need to get across.
		f.repair = true
		f.repairReason = err.Error()
		f.st = state.New(pairID)
		f.st.SetPath(f.stPath)
		f.storable = true
		f.warnRepair()
		return
	}
	if st == nil {
		// First run.
		st = state.New(pairID)
		st.SetPath(f.stPath)
	}
	f.st = st
	f.storable = true
}

// warnRepair prints the block specified by contracts/state-file-format.md §5.
func (f *Fs) warnRepair() {
	fmt.Fprintf(os.Stderr,
		"WARNING: the sync state at %s could not be used (%s)\n"+
			"         No deletion will be performed during this run.\n"+
			"         Without a trustworthy baseline every local file is uploaded again,\n"+
			"         so duplicate copies may appear in Feishu.\n"+
			"         weftsync will NOT remove those duplicates by itself, because it\n"+
			"         cannot prove they are its own. Check the folder in Feishu and\n"+
			"         delete the extras there; the next run resumes normal incremental sync.\n",
		f.stPath, f.repairReason)
}

// stateFingerprint returns the remembered local fingerprint for remote, but
// only when the remote entry still carries the token we recorded.
func (f *Fs) stateFingerprint(remote, token string) (mtime int64, size int64, ok bool) {
	if f.st == nil || token == "" {
		return 0, 0, false
	}
	f.stateMu.Lock()
	defer f.stateMu.Unlock()

	e, found := f.st.Lookup(remote)
	if !found || e.Rebuilt {
		return 0, 0, false
	}
	if e.RemoteToken != token {
		// The remote copy is not the one we uploaded; trust nothing.
		return 0, 0, false
	}
	return e.LocalMTime, e.LocalSize, true
}

// previousToken returns the token we recorded for our last upload of remote, if
// the snapshot still describes a real upload.
func (f *Fs) previousToken(remote string) (string, bool) {
	if f.st == nil {
		return "", false
	}
	f.stateMu.Lock()
	defer f.stateMu.Unlock()

	e, found := f.st.Lookup(remote)
	if !found || e.Rebuilt || e.RemoteToken == "" {
		return "", false
	}
	return e.RemoteToken, true
}

// recordUpload remembers a successful upload. Failures are reported but never
// fail the operation: the file is already on the remote, and the only cost of a
// lost record is one redundant upload next run.
func (f *Fs) recordUpload(ctx context.Context, remote string, src fs.ObjectInfo, size int64, token string) {
	if f.st == nil {
		return
	}

	var mtime int64
	if t := src.ModTime(ctx); !t.IsZero() {
		// Whole seconds: Feishu's precision is 1s, and storing sub-second values
		// would make the next comparison fail for no reason.
		mtime = t.Unix()
	} else {
		fs.Debugf(f, "no local modification time for %q; it will be re-uploaded next run", remote)
		return
	}

	f.stateMu.Lock()
	f.st.Put(remote, state.Entry{
		LocalMTime:  mtime,
		LocalSize:   size,
		RemoteToken: token,
		SyncedAt:    time.Now().Unix(),
	})
	f.stateMu.Unlock()

	f.flushState()
}

// forgetPath drops a path from the snapshot.
func (f *Fs) forgetPath(remote string) {
	if f.st == nil {
		return
	}
	f.stateMu.Lock()
	f.st.Delete(remote)
	f.stateMu.Unlock()
	f.flushState()
}

// flushState persists the snapshot.
//
// Serialised: two goroutines writing the same .tmp file at once made the rename
// fail on Windows ("the file is being used by another process"), which lost the
// record and forced a redundant re-upload.
func (f *Fs) flushState() {
	if f.st == nil || !f.storable {
		return
	}
	f.stateMu.Lock()
	defer f.stateMu.Unlock()

	if err := state.Save(f.st); err != nil {
		fmt.Fprintf(os.Stderr,
			"WARNING: could not save the sync state to %s: %v\n"+
				"         The next run will re-upload files it cannot account for;\n"+
				"         no remote file is at risk.\n", f.stPath, err)
	}
}

// deletionsAllowed reports whether this run may delete remote files.
//
// Repair mode forbids all deletions: without a trustworthy snapshot we cannot
// tell "an old version of ours" from "someone else's file", and deletion in
// Feishu cannot be undone.
func (f *Fs) deletionsAllowed() bool {
	return !f.repair
}

// ErrDeletionBlocked is returned when a delete is attempted in repair mode.
var ErrDeletionBlocked = errors.New(
	"feishu: refusing to delete because the sync state was rebuilt this run;\n" +
		"no remote file will be removed. Fix or remove the state file and run again")
