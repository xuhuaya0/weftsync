package feishu

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"weftsync/state"
)

// Per-conflict resolution.
//
// Only the state file is touched here. Nothing is uploaded, moved or deleted:
// "resolve this one conflict" should not be able to change the cloud as a side
// effect of a click, because a wrong click cannot be undone (Feishu has no trash).
//
// Two actions, and they mean exactly this:
//
//	forget   drop the snapshot record, so the next run uploads the local file over
//	         whatever is at that path. "The local copy wins."
//	adopt    record the PATH's current remote token together with the local
//	         fingerprint, so the next run treats the path as settled. This is only
//	         honest when the local file really does match the remote one, which is
//	         why the job layer downloads first and calls this afterwards. Recording
//	         an arbitrary fingerprint here would make the snapshot claim an upload
//	         that never happened.

// conflictResolveReport is the command result.
type conflictResolveReport struct {
	// Action echoes what was requested: "forget" or "adopt".
	Action string `json:"action"`
	// Path is the request path, normalised.
	Path string `json:"path"`
	// Before describes the record that was there, for the log.
	Before string `json:"before,omitempty"`
	// After describes the record that is there now.
	After string `json:"after,omitempty"`
	// Changed is false when there was nothing to do.
	Changed bool `json:"changed"`
	// Summary is a one-line outcome for humans.
	Summary string `json:"summary"`
}

// resolveCommand implements `backend resolve`.
//
// The file path comes from -o path=, NOT from the first positional argument. That
// argument is resolved by rclone as an Fs, so `backend resolve remote:a.txt` is
// rejected with "is a file not a directory" - the CLI can only name a directory
// there. Passing the path as an option works from the CLI and from the job layer,
// where the Fs is already open and the path is relative to it.
func (f *Fs) resolveCommand(ctx context.Context, arg []string, opt map[string]string) (*conflictResolveReport, error) {
	action := opt["action"]
	if action == "" {
		return nil, errors.New("resolve: -o action is required (forget or adopt)")
	}
	if action != "forget" && action != "adopt" {
		return nil, fmt.Errorf("resolve: unknown action %q (expected forget or adopt)", action)
	}

	rel := normaliseRelPath(opt["path"])
	if rel == "" {
		// An empty path would mean "the root", and editing the snapshot for the
		// root would silently change the meaning of every entry below it.
		return nil, errors.New("resolve: -o path=<file path relative to the root> is required")
	}

	// Refusals first, and they are refusals rather than quiet no-ops: the caller
	// believes it changed something, and telling it otherwise is the only way it
	// can report the truth to the user.
	if f.st == nil {
		return nil, errors.New(
			"resolve: this remote has no state_file, so there is no snapshot to edit")
	}
	if f.repair {
		return nil, errors.New(
			"resolve: the snapshot was rebuilt this run, so editing it would record " +
				"something that was never observed. Run a successful sync first")
	}

	rep := &conflictResolveReport{Action: action, Path: rel}

	f.stateMu.Lock()
	prev, had := f.st.Lookup(rel)
	f.stateMu.Unlock()
	if had {
		rep.Before = describeEntry(prev)
	}

	switch action {
	case "forget":
		if !had {
			rep.Summary = "the snapshot had no record for this path, so the next run will " +
				"already upload the local file there; nothing was changed"
			return rep, nil
		}
		f.forgetPath(rel)
		rep.Changed = true
		rep.Summary = "dropped the snapshot record; the next run will upload the local " +
			"file over the remote one"

	case "adopt":
		token := opt["token"]
		mtime, err := strconv.ParseInt(opt["mtime"], 10, 64)
		if err != nil || mtime <= 0 {
			return nil, errors.New("resolve: --mtime (unix seconds) is required for adopt")
		}
		size, err := strconv.ParseInt(opt["size"], 10, 64)
		if err != nil || size < 0 {
			return nil, errors.New("resolve: --size is required for adopt")
		}
		if token == "" {
			return nil, errors.New("resolve: --token is required for adopt")
		}

		// The token must be one the folder actually carries. Adopting a token
		// that is not there would make the next run believe the path is settled
		// while the real remote entry stays different - the conflict would come
		// straight back, with a snapshot that now disagrees with reality.
		remote, _, err := f.scanRemote(ctx, "")
		if err != nil {
			return nil, err
		}
		if !containsToken(remote[rel], token) {
			return nil, fmt.Errorf(
				"resolve: token %s is not at %q any more; re-read the conflict list and try again",
				token, rel)
		}

		f.stateMu.Lock()
		f.st.Put(rel, state.Entry{
			LocalMTime:  mtime,
			LocalSize:   size,
			RemoteToken: token,
			SyncedAt:    nowUnix(),
		})
		f.stateMu.Unlock()
		f.flushState()

		rep.Changed = true
		rep.After = describeEntry(state.Entry{
			LocalMTime: mtime, LocalSize: size, RemoteToken: token,
		})
		rep.Summary = "recorded the current remote file as the baseline for this path"
	}

	return rep, nil
}

// normaliseRelPath turns the option value into a clean path relative to the Fs
// root.
func normaliseRelPath(p string) string {
	return joinPath("", p)
}

func containsToken(tokens []string, want string) bool {
	for _, t := range tokens {
		if t == want {
			return true
		}
	}
	return false
}

func describeEntry(e state.Entry) string {
	return fmt.Sprintf("token=%s mtime=%d size=%d", e.RemoteToken, e.LocalMTime, e.LocalSize)
}

// nowUnix is a variable so tests can pin the clock.
var nowUnix = func() int64 { return time.Now().Unix() }
