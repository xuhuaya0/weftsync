package feishu

import (
	"context"
	"errors"
	"fmt"
)

// Rewriting state paths after a directory split.
//
// A split moves files on both sides, so every recorded path changes. The state maps
// a remote path to the local fingerprint that produced it; if the keys are not
// moved with the files, the next run finds no record for them, treats each as new
// and uploads it. On Feishu that creates a duplicate rather than overwriting, so a
// "tidy up" would leave two copies of everything.
//
// This lives in the backend rather than in the split code because the backend owns
// the snapshot: it holds it in memory for as long as its Fs is cached, and rclone's
// Fs cache has no per-entry eviction. A caller editing the file directly would be
// overwritten by the next flush from that in-memory copy, which would also restore
// the old paths. Doing it here updates both, in that order.

// repathReport is the command result.
type repathReport struct {
	// Migrated counts entries whose path changed.
	Migrated int `json:"migrated"`
	// Missing lists source paths that had no record, which is normal for files the
	// state never recorded (a pair that has not synced them yet).
	Missing []string `json:"missing,omitempty"`
	// Summary is a one-line outcome for humans.
	Summary string `json:"summary"`
}

// repathCommand implements `backend repath`.
//
// Arguments are alternating from/to paths: `repath r: a b c d` moves a->b and c->d.
// Pairs of separate arguments rather than one joined string, because a separator
// character cannot be chosen safely - a path may contain any of them - and a NUL
// cannot be passed through a command line at all (measured: the two paths arrive
// concatenated, because the shell drops the NUL).
//
// Only the state is changed: no file is uploaded, moved or deleted.
func (f *Fs) repathCommand(ctx context.Context, arg []string) (*repathReport, error) {
	if len(arg) == 0 {
		return nil, errors.New("repath: from/to path pairs are required")
	}
	if len(arg)%2 != 0 {
		return nil, fmt.Errorf(
			"repath: expected an even number of paths (from/to pairs), got %d", len(arg))
	}
	if f.st == nil {
		// Stateless pair: it re-uploads everything each run anyway, so there is
		// nothing that could go stale.
		return &repathReport{Summary: "this remote has no state_file, so there is nothing to update"}, nil
	}
	if f.repair {
		// The snapshot was just rebuilt, which means it is already known to be
		// untrustworthy. Rewriting it would mix a guess with an edit.
		return nil, errors.New(
			"repath: the snapshot was rebuilt this run, so rewriting its paths would " +
				"record something that was never observed. Run a successful sync first")
	}

	rep := &repathReport{}
	f.stateMu.Lock()
	for i := 0; i < len(arg); i += 2 {
		from, to := arg[i], arg[i+1]
		if from == "" || to == "" {
			f.stateMu.Unlock()
			return nil, fmt.Errorf("repath: empty path in pair %d", i/2)
		}
		entry, found := f.st.Lookup(from)
		if !found {
			rep.Missing = append(rep.Missing, from)
			continue
		}
		f.st.Delete(from)
		entry.Path = to
		f.st.Put(to, entry)
		rep.Migrated++
	}
	f.stateMu.Unlock()

	// Flush outside the lock: flushState takes it itself, and holding it across a
	// disk write would stall every other operation on this Fs.
	if rep.Migrated > 0 {
		f.flushState()
	}

	rep.Summary = fmt.Sprintf("moved %d state entr(ies) to their new paths", rep.Migrated)
	if len(rep.Missing) > 0 {
		rep.Summary += fmt.Sprintf("; %d had no record", len(rep.Missing))
	}
	return rep, nil
}
