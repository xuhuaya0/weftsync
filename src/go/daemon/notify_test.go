package daemon

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"weftsync/job"
)

// notifier records what would have been shown as a desktop balloon.
type notifier struct {
	mu    sync.Mutex
	calls []string
}

func (n *notifier) fn(title, text string) {
	n.mu.Lock()
	n.calls = append(n.calls, title+" | "+text)
	n.mu.Unlock()
}

func (n *notifier) all() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.calls...)
}

// runOnce primes a daemon whose job returns the given results and error, runs it,
// and hands back what reached the notifier.
func runOnce(t *testing.T, results []job.PairResult, runErr error, plan bool) []string {
	t.Helper()
	dir := t.TempDir()
	d, err := New([]string{writeJobFile(t, dir, "a", simpleJob("a", ""))}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	n := &notifier{}
	d.SetNotifier(n.fn)

	done := make(chan struct{})
	d.runJob = func(ctx context.Context, cfg *job.Config, w io.Writer, opt job.Options) ([]job.PairResult, error) {
		close(done)
		return results, runErr
	}

	if plan {
		if err := d.TriggerPlan(context.Background(), "a"); err != nil {
			t.Fatalf("TriggerPlan: %v", err)
		}
	} else {
		if err := d.Trigger(context.Background(), "a"); err != nil {
			t.Fatalf("Trigger: %v", err)
		}
	}
	<-done
	// Stop waits for the run goroutine, which is what guarantees the notifier -
	// raised at the end of execute - has already run. Waiting on the status flag
	// alone would let the assertion race it.
	d.Stop()
	return n.all()
}

// A scheduled run happens unattended, so a failure has to reach the user.
func TestFailureRaisesNotification(t *testing.T) {
	calls := runOnce(t, nil, errors.New("boom"), false)
	if len(calls) != 1 {
		t.Fatalf("notifications = %v, want exactly one", calls)
	}
	if !strings.Contains(calls[0], "失败") || !strings.Contains(calls[0], "boom") {
		t.Errorf("notification should name the failure, got %q", calls[0])
	}
}

// A guard hit is not an error but it does need a human, and it deletes nothing.
func TestGuardHitRaisesNotification(t *testing.T) {
	calls := runOnce(t, []job.PairResult{{Pair: "p", GuardHit: true}}, nil, false)
	if len(calls) != 1 {
		t.Fatalf("notifications = %v, want exactly one", calls)
	}
	if !strings.Contains(calls[0], "拦下") || !strings.Contains(calls[0], "什么都没有删") {
		t.Errorf("notification should say the run was stopped and nothing was deleted, got %q", calls[0])
	}
}

func TestConflictsRaiseNotification(t *testing.T) {
	results := []job.PairResult{{Pair: "p", Conflicts: []job.Conflict{{}, {}, {}}}}
	calls := runOnce(t, results, nil, false)
	if len(calls) != 1 {
		t.Fatalf("notifications = %v, want exactly one", calls)
	}
	if !strings.Contains(calls[0], "3") || !strings.Contains(calls[0], "冲突") {
		t.Errorf("notification should count the conflicts, got %q", calls[0])
	}
}

// The one that must stay quiet: a routine success would train the user to
// dismiss the notifications that matter.
func TestSuccessIsSilent(t *testing.T) {
	calls := runOnce(t, []job.PairResult{{Pair: "p"}}, nil, false)
	if len(calls) != 0 {
		t.Errorf("a successful run must not notify, got %v", calls)
	}
}

// A preview was just asked for by a user looking at the screen.
func TestPreviewIsSilent(t *testing.T) {
	calls := runOnce(t, nil, errors.New("boom"), true)
	if len(calls) != 0 {
		t.Errorf("a preview must not notify, got %v", calls)
	}
}

// No tray means no notifier, and the daemon must not care.
func TestNoNotifierIsFine(t *testing.T) {
	dir := t.TempDir()
	d, err := New([]string{writeJobFile(t, dir, "a", simpleJob("a", ""))}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d.runJob = func(ctx context.Context, cfg *job.Config, w io.Writer, opt job.Options) ([]job.PairResult, error) {
		return nil, errors.New("boom")
	}
	if err := d.Trigger(context.Background(), "a"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	d.Stop()
}
