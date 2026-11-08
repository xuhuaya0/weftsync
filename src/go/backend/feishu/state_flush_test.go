package feishu

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"weftsync/state"
)

// newTestFs builds the smallest Fs that can persist a snapshot.
//
// The real newFs needs credentials and a network round trip, but everything
// flushState touches is these four fields.
func newTestFs(t *testing.T) (*Fs, string) {
	t.Helper()
	// Mirror loadState exactly: the pair id comes from the path, and the filename
	// has to agree with it or state.Load refuses the file.
	path := filepath.Join(t.TempDir(), "test-pair.state.json")
	st := state.New(state.PairIDFromPath(path))
	st.SetPath(path)
	return &Fs{st: st, stPath: path, storable: true}, path
}

// readEntries loads the snapshot back from disk.
func readEntries(t *testing.T, path string) map[string]state.Entry {
	t.Helper()
	f, err := state.Load(path)
	if err != nil {
		t.Fatalf("load %s: %v", path, err)
	}
	return f.Entries
}

// withFlushInterval sets the throttle for one test.
func withFlushInterval(t *testing.T, d time.Duration) {
	t.Helper()
	old := stateFlushInterval
	stateFlushInterval = d
	t.Cleanup(func() { stateFlushInterval = old })
}

// TestFlushStateThrottlesButKeepsThePendingFlag is the regression test for a bug
// that would have cost real uploads.
//
// The first version of flushStateLocked returned early when the throttle had not
// elapsed *and stPending was false*, without setting stPending. The record was
// then neither written nor remembered, so FlushState had nothing to write and the
// file was simply lost - on a fresh Fs the very first throttled save hits this
// path, because stLastFlush starts zero.
func TestFlushStateThrottlesButKeepsThePendingFlag(t *testing.T) {
	withFlushInterval(t, time.Hour)
	f, path := newTestFs(t)

	// First save goes through: stLastFlush is zero, so far more than an hour has
	// "elapsed".
	f.st.Put("first.txt", state.Entry{LocalMTime: 1, LocalSize: 2, RemoteToken: "tok1"})
	f.flushState()

	if _, ok := readEntries(t, path)["first.txt"]; !ok {
		t.Fatalf("the first flush did not write anything")
	}

	// Second save is inside the throttle window: it must not write...
	f.st.Put("second.txt", state.Entry{LocalMTime: 3, LocalSize: 4, RemoteToken: "tok2"})
	f.flushState()

	if _, ok := readEntries(t, path)["second.txt"]; ok {
		t.Fatalf("the throttle did not hold: second.txt was written anyway")
	}

	// ...but it must remember that a write is owed.
	if !f.stPending {
		t.Fatalf("the throttled save did not set stPending; " +
			"FlushState would have nothing to write and second.txt would be lost")
	}

	// The end-of-run flush ignores the throttle and writes it.
	f.FlushState()

	if _, ok := readEntries(t, path)["second.txt"]; !ok {
		t.Fatalf("FlushState did not write the pending record")
	}
	if f.stPending {
		t.Fatalf("FlushState left stPending set after a successful save")
	}
}

// TestFlushStateIsNoOpWhenNothingPending guards the other direction: the
// end-of-run flush must not rewrite the file for no reason, because that write is
// exactly the one that can fail on a folder OneDrive is watching.
func TestFlushStateIsNoOpWhenNothingPending(t *testing.T) {
	withFlushInterval(t, time.Hour)
	f, path := newTestFs(t)

	f.st.Put("a.txt", state.Entry{LocalMTime: 1, LocalSize: 2, RemoteToken: "tok"})
	f.flushState()

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat after the first flush: %v", err)
	}

	// Age the file so a rewrite would be visible even on a coarse clock.
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	f.FlushState()

	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !after.ModTime().Equal(old) {
		t.Fatalf("FlushState rewrote the file with nothing pending")
	}
}

// TestFlushStateKeepsPendingWhenSaveFails checks that a failed save is retried
// later instead of being forgotten for the rest of the run.
//
// The failure is produced by making the parent directory a file, which no
// operating system will let us write into.
func TestFlushStateKeepsPendingWhenSaveFails(t *testing.T) {
	withFlushInterval(t, time.Hour)

	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	path := filepath.Join(blocker, "test-pair.state.json") // parent is a file: unwritable

	st := state.New("test-pair")
	st.SetPath(path)
	f := &Fs{st: st, stPath: path, storable: true}

	f.st.Put("a.txt", state.Entry{LocalMTime: 1, LocalSize: 2, RemoteToken: "tok"})

	// The run's own flush tries first and fails; the record must survive as
	// pending rather than being dropped.
	f.flushState()
	if !f.stPending {
		t.Fatalf("a failed save did not set stPending; the record would be lost")
	}

	// The end-of-run flush retries and fails again: still pending, still not lost.
	f.FlushState()
	if !f.stPending {
		t.Fatalf("a failed save cleared stPending; the record would never be retried")
	}
}

// TestFlushStateWithoutSnapshotIsHarmless covers the no-state_file configuration.
func TestFlushStateWithoutSnapshotIsHarmless(t *testing.T) {
	for _, f := range []*Fs{
		{},                        // no snapshot at all
		{st: state.New("p")},      // snapshot present but not storable
		{storable: true},          // storable but no snapshot
	} {
		f.flushState()
		f.FlushState()
	}
}
