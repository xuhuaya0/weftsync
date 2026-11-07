package job

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rclone/rclone/fs/accounting"
)

// A run's counters must land in its own accounting group.
//
// This is the whole point of the group: with two jobs running at once, the global
// counters report the sum of both, so the UI would show one job's progress for the
// other and "current file" could name a file from the other job. The assertion is
// therefore two-sided: this group went up, the global counters did not.
func TestTrackerIsolatesCounters(t *testing.T) {
	cfg, local, _ := localPair(t, VariantUpdate, "")
	for i := 0; i < 3; i++ {
		writeFile(t, filepath.Join(local, "f"+string(rune('0'+i))+".txt"), "0123456789")
	}

	global := accounting.GlobalStats()
	global.ResetCounters()

	tracker := NewTracker("weftsync:test-isolate")
	other := NewTracker("weftsync:test-other")
	tracker.Reset()
	other.Reset()

	if _, err := Run(context.Background(), cfg, io.Discard,
		Options{StatsGroup: tracker.Group()}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := tracker.Snapshot()
	if got.Transfers != 3 {
		t.Errorf("transfers = %d, want 3 (the counters did not follow the group)",
			got.Transfers)
	}
	if got.Bytes <= 0 {
		t.Errorf("bytes = %d, want the transferred size", got.Bytes)
	}
	if n := other.Snapshot().Transfers; n != 0 {
		t.Errorf("another group saw %d transfers; groups are not isolated", n)
	}
	if n := global.GetTransfers(); n != 0 {
		t.Errorf("global counters saw %d transfers; the run ignored its group", n)
	}
}

// The speed must be non-zero while bytes are moving.
//
// rclone's own "speed" only counts finished transfers, so a pass with several
// concurrent uploads reports 0 B/s for its entire duration - precisely when the
// user is watching. The rate is therefore computed from the pass start time, and
// this test pins that down.
func TestSnapshotReportsSpeedWhileTransferring(t *testing.T) {
	tracker := NewTracker("weftsync:test-speed")
	tracker.Reset()
	tracker.stats.Bytes(4 << 20)
	tracker.stats.SetTransferQueue(0, 8<<20)

	// With the pass only just started there is nothing to average, so the value
	// comes straight from rclone. It is 0 here, which is the defect: a pass with
	// unfinished transfers has no library-side speed at all.
	if got := tracker.Snapshot(); got.Speed != 0 {
		t.Logf("rclone already reports %v B/s; the fallback is still used below", got.Speed)
	}

	// Now pretend the pass has been running for a couple of seconds.
	tracker.mu.Lock()
	tracker.resetAt = time.Now().Add(-2 * time.Second)
	tracker.mu.Unlock()

	got := tracker.Snapshot()
	if got.Speed <= 0 {
		t.Errorf("speed = %v, want a positive rate while bytes are moving", got.Speed)
	}
	if got.Bytes <= 0 {
		t.Fatalf("bytes = %d, want the transferred amount", got.Bytes)
	}
	// The ETA has to follow from that rate, otherwise the UI shows a speed with no
	// estimate next to it.
	if got.ETA == nil {
		t.Error("ETA should be derivable from bytes, total and speed")
	}
}

// A second run must not start from the first run's totals.
func TestTrackerResetClearsCounters(t *testing.T) {
	tracker := NewTracker("weftsync:test-reset")
	tracker.Reset()
	tracker.stats.Bytes(4096)

	if tracker.Snapshot().Bytes == 0 {
		t.Fatal("the byte counter did not move, so the reset assertion proves nothing")
	}

	tracker.Reset()
	if got := tracker.Snapshot(); got.Bytes != 0 || got.Transfers != 0 {
		t.Errorf("counters survived a reset: %+v", got)
	}
}

// NewTracker called twice with the same name must not create a second group.
//
// rclone has no exported way to delete a group, so every extra group is a leaked
// average-speed goroutine for the life of the process. It also has to be the same
// group, otherwise the transfers and the reader would count into different ones.
func TestNewTrackerReusesGroup(t *testing.T) {
	a := NewTracker("weftsync:test-reuse")
	b := NewTracker("weftsync:test-reuse")
	if a.stats != b.stats {
		t.Error("the same group name produced two StatsInfo values")
	}
}

// A nil tracker is usable everywhere, so callers never need to branch on "is there
// a progress reader".
func TestNilTrackerIsSafe(t *testing.T) {
	var empty *Tracker
	if got := empty.Group(); got != "" {
		t.Errorf("nil tracker group = %q, want empty", got)
	}
	empty.Reset()
	if !empty.Snapshot().Empty() {
		t.Error("a nil tracker must report empty progress")
	}
}

// A cancelled context must reach the engine, and a stop must not be reported as a
// failure.
//
// Note what this test does NOT assert: that Run returns context.Canceled. Measured
// on a local pair, rclone can swallow a cancelled context and return nil, so
// classifying a stop by the returned error would label a stopped run "success" or,
// worse, a stopped run as a failure depending on the backend. The daemon therefore
// classifies on its own context (see Daemon.execute), and this test pins down only
// the part that is guaranteed: nothing is written to the destination.
func TestCancelledRunWritesNothing(t *testing.T) {
	cfg, local, remote := localPair(t, VariantUpdate, "")
	writeFile(t, filepath.Join(local, "a.txt"), "x")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// The error is deliberately ignored: see the comment above.
	_, _ = Run(ctx, cfg, io.Discard, Options{})

	entries, err := os.ReadDir(remote)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("a cancelled run copied %d item(s) to the destination", len(entries))
	}
}

// The daemon decides "stopped" from its own context, so that classification has to
// survive an engine that reports nothing. This covers the check itself.
func TestCancelledContextIsDetectable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	if errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("a live context must not look cancelled")
	}
	cancel()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("ctx.Err() = %v, want context.Canceled", ctx.Err())
	}
}
