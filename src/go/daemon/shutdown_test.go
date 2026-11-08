package daemon

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"weftsync/job"
)

// A shutdown waits for running jobs. Without a way to say which ones, that wait
// is indistinguishable from a hang - which is exactly what the command line
// prints this list for.
func TestRunningJobsNamesWhatShutdownWaitsFor(t *testing.T) {
	dir := t.TempDir()
	d, err := New([]string{
		writeJobFile(t, dir, "a", simpleJob("a", "")),
		writeJobFile(t, dir, "b", simpleJob("b", "")),
	}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := d.RunningJobs(); len(got) != 0 {
		t.Fatalf("idle daemon reported %v as running", got)
	}

	release := make(chan struct{})
	started := make(chan struct{}, 1)
	d.runJob = func(ctx context.Context, cfg *job.Config, w io.Writer, opt job.Options) ([]job.PairResult, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		return nil, nil
	}

	if err := d.Trigger(context.Background(), "a"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the run never started")
	}

	got := d.RunningJobs()
	if len(got) != 1 || got[0] != "a" {
		t.Errorf("RunningJobs = %v, want [a]", got)
	}

	close(release)
	d.Stop()
	if got := d.RunningJobs(); len(got) != 0 {
		t.Errorf("after shutdown RunningJobs = %v, want empty", got)
	}
}

// The message the command line builds must read as a sentence, not a dump.
func TestRunningJobsIsUsableInAMessage(t *testing.T) {
	dir := t.TempDir()
	d, err := New([]string{
		writeJobFile(t, dir, "photos", simpleJob("photos", "")),
		writeJobFile(t, dir, "docs", simpleJob("docs", "")),
	}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	block := make(chan struct{})
	d.runJob = func(ctx context.Context, cfg *job.Config, w io.Writer, opt job.Options) ([]job.PairResult, error) {
		<-block
		return nil, nil
	}
	for _, name := range []string{"photos", "docs"} {
		if err := d.Trigger(context.Background(), name); err != nil {
			t.Fatalf("Trigger %s: %v", name, err)
		}
	}
	// Both must be running before the list is meaningful.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(d.RunningJobs()) < 2 {
		time.Sleep(2 * time.Millisecond)
	}

	msg := strings.Join(d.RunningJobs(), ", ")
	if !strings.Contains(msg, "photos") || !strings.Contains(msg, "docs") {
		// Map iteration order is random, so only membership matters.
		t.Errorf("joined message = %q, want both job names", msg)
	}

	close(block)
	d.Stop()
}
