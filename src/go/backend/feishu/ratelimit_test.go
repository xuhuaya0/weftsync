package feishu

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Requests must be spaced out rather than sent freely and retried afterwards.
// This is the point of the limiter: Feishu rejects bursts with 1061045, and a
// retry loop burns the daily 10000-request budget for nothing.
func TestLimiterSpacesRequests(t *testing.T) {
	var mu sync.Mutex
	var stamps []time.Time

	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		stamps = append(stamps, time.Now())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"files":[],"has_more":false}}`))
	})
	// 20 QPS means at least 50ms between calls; use a generous bound so the test
	// is not flaky on a loaded machine.
	c.withLimits(20, 0)

	const n = 4
	for i := 0; i < n; i++ {
		if _, err := c.listFolder(context.Background(), "folder"); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(stamps) != n {
		t.Fatalf("server saw %d requests, want %d", len(stamps), n)
	}
	for i := 1; i < len(stamps); i++ {
		gap := stamps[i].Sub(stamps[i-1])
		if gap < 30*time.Millisecond {
			t.Errorf("requests %d and %d were only %v apart; the limiter is not pacing", i-1, i, gap)
		}
	}
}

// A burst of concurrent callers must still be paced: rclone runs transfers in
// parallel, so "one caller at a time" is not enough.
func TestLimiterPacesConcurrentCallers(t *testing.T) {
	var count int32

	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&count, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"files":[],"has_more":false}}`))
	})
	c.withLimits(50, 0) // 20ms apart

	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.listFolder(context.Background(), "folder"); err != nil {
				t.Errorf("listFolder: %v", err)
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	if got := atomic.LoadInt32(&count); got != 5 {
		t.Errorf("server saw %d requests, want 5", got)
	}
	// 5 requests at 50 QPS need ~80ms of spacing; anything under 40ms means the
	// limiters did not serialise anything.
	if elapsed < 40*time.Millisecond {
		t.Errorf("5 requests completed in %v; they were not paced", elapsed)
	}
}

// The move endpoint has its own much tighter budget and must not consume the
// general allowance (nor be consumed by it).
func TestMoveUsesItsOwnLimiter(t *testing.T) {
	var mu sync.Mutex
	var moveStamps []time.Time

	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path[len(r.URL.Path)-5:] == "/move" {
			mu.Lock()
			moveStamps = append(moveStamps, time.Now())
			mu.Unlock()
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"files":[],"has_more":false}}`))
	})

	// Ordinary calls are unlimited here; moves are capped at 600/minute, i.e.
	// 100ms apart, so the spacing seen can only come from the move limiter.
	c.withLimits(0, 600)

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		// Interleave ordinary calls: they must not borrow from the move budget.
		// Assert via the server's own count instead of a metadata call, which
		// would need a real folder token.
		if err := c.deleteFile(ctx, "filler"); err != nil {
			t.Fatalf("deleteFile: %v", err)
		}
		if _, err := c.moveNode(ctx, "file-token", "file", "dest"); err != nil {
			t.Fatalf("moveNode: %v", err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(moveStamps) != 3 {
		t.Fatalf("server saw %d moves, want 3", len(moveStamps))
	}
	for i := 1; i < len(moveStamps); i++ {
		gap := moveStamps[i].Sub(moveStamps[i-1])
		if gap < 50*time.Millisecond {
			t.Errorf("moves %d and %d were %v apart; the move limiter is not pacing", i-1, i, gap)
		}
	}
}

// Zero disables the limiter, which is what the tests above rely on and what the
// option promises.
func TestZeroQPSDisablesLimiting(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {})
	c.withLimits(0, 0)
	if c.limiter != nil {
		t.Error("qps=0 should leave no limiter")
	}
	if c.moveLimiter != nil {
		t.Error("move_per_minute=0 should leave no limiter")
	}

	// wait on a nil limiter must be a no-op rather than a panic.
	if err := wait(context.Background(), nil); err != nil {
		t.Errorf("wait(nil) = %v", err)
	}
}

// The default must be the documented 5 QPS, not "off": a backend that hammers
// Feishu out of the box is a bug waiting for a large folder to happen.
func TestDefaultLimitsComeFromOptions(t *testing.T) {
	f := &Fs{}
	_ = f
	// The option defaults live in the RegInfo; assert the numbers the code uses.
	if got := (&Options{}); got.QPS != 0 {
		t.Errorf("QPS should be zero when unset in the struct; the default is applied by configstruct")
	}

	// And the constants documented in the help text.
	if defaultQPS != 5.0 {
		t.Errorf("defaultQPS = %v, want 5", defaultQPS)
	}
	if defaultMovePerMinute != 20 {
		t.Errorf("defaultMovePerMinute = %d, want 20", defaultMovePerMinute)
	}
}

var _ = json.Marshal
