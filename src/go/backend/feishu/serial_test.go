package feishu

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Only one request may be in flight at a time.
//
// Feishu's drive endpoints say so explicitly:
//
//	"This API doesn't allow to concurrently call, and supports up to 5 queries
//	 per second (QPS)" - drive/v1/files/:file_token, DELETE
//
// A rate limiter does not express that. It paces when requests START and says
// nothing about how many are still waiting, so with rclone's default 8 checkers
// eight deletes could overlap. Measured 2026-11-18 against a real tenant, deleting
// the same 40 files twice:
//
//	default concurrency (8 workers)   71s, 4 files failed with 1061001
//	--checkers 1 (one at a time)      89s, 0 failures
//
// and the same thing hit a real mirror run: 68 planned deletions, 63 done, 5
// failed after every retry was spent.
//
// This test drives the client through the real HTTP path and counts overlapping
// requests at the server.
func TestRequestsNeverOverlap(t *testing.T) {
	var inFlight atomic.Int32
	var maxSeen atomic.Int32
	var total atomic.Int32

	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		n := inFlight.Add(1)
		for {
			cur := maxSeen.Load()
			if n <= cur || maxSeen.CompareAndSwap(cur, n) {
				break
			}
		}
		total.Add(1)
		// Long enough that an unserialised client would overlap: without the lock
		// several goroutines would be inside this sleep at once.
		time.Sleep(30 * time.Millisecond)
		inFlight.Add(-1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"files":[],"has_more":false}}`))
	})
	// A generous rate so the limiter cannot mask an overlap: 1000 QPS means it
	// lets these calls through as fast as they arrive.
	c.withLimits(1000, 0)

	const n = 6
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.listFolder(context.Background(), "folder"); err != nil {
				t.Errorf("listFolder: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := total.Load(); got != n {
		t.Fatalf("server saw %d requests, want %d", got, n)
	}
	if got := maxSeen.Load(); got != 1 {
		t.Errorf("%d requests were in flight at the same time; Feishu rejects that "+
			"with 1061001 and the fix is client.serial", got)
	}
}

// The serialisation must not depend on the rate limiter being installed: a pair
// with qps = 0 has no limiter at all, and the concurrency rule still applies.
func TestRequestsNeverOverlapWithoutLimiter(t *testing.T) {
	var inFlight atomic.Int32
	var maxSeen atomic.Int32

	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		n := inFlight.Add(1)
		for {
			cur := maxSeen.Load()
			if n <= cur || maxSeen.CompareAndSwap(cur, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		inFlight.Add(-1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"files":[],"has_more":false}}`))
	})
	c.withLimits(0, 0) // no limiter at all

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.listFolder(context.Background(), "folder"); err != nil {
				t.Errorf("listFolder: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := maxSeen.Load(); got != 1 {
		t.Errorf("%d requests overlapped with qps=0; the serialisation must not "+
			"live in the limiter", got)
	}
}
