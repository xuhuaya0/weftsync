package feishu

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestSystemicLatchStopsCallingTheServer is the reason this mechanism exists.
//
// A real migration is 74,313 files against a 10,000-upload daily quota. Once the
// quota is gone, every remaining file fails; without a latch each one still costs
// three HTTP round trips and three seconds of backoff, and rclone then repeats the
// whole sync three more times. The arithmetic came out at roughly ten days of
// doing nothing but failing.
//
// So: after systemicStrikes consecutive calls exhaust their retry budget, the
// client stops touching the network at all and says why.
func TestSystemicLatchStopsCallingTheServer(t *testing.T) {
	c, hits := testClient(t, jsonBody(t, map[string]any{
		"code": CodeRetryable, "msg": "frequency limit",
	}))

	var hookCalls int32
	c.onSystemic = func() { atomic.AddInt32(&hookCalls, 1) }

	call := func() error {
		_, err := c.do(context.Background(), request{
			where: "files.upload_all", method: http.MethodPost, path: "/x",
		})
		return err
	}

	// The first call is the only one that reaches the server more than once.
	for i := 0; i < systemicStrikes; i++ {
		if err := call(); err == nil {
			t.Fatalf("call %d: expected a failure", i+1)
		}
	}

	wantHits := int32(maxAttempts * systemicStrikes)
	if got := atomic.LoadInt32(hits); got != wantHits {
		t.Fatalf("HTTP requests = %d, want %d (maxAttempts x systemicStrikes)", got, wantHits)
	}
	if got := atomic.LoadInt32(&hookCalls); got != 1 {
		t.Fatalf("abort hook ran %d times, want exactly 1", got)
	}

	// From now on: no network, no waiting, just a clear error.
	for i := 0; i < 5; i++ {
		err := call()
		if !errors.Is(err, ErrSystemicFailure) {
			t.Fatalf("latched call %d returned %v, want ErrSystemicFailure", i+1, err)
		}
	}
	if got := atomic.LoadInt32(hits); got != wantHits {
		t.Fatalf("the latched client still made %d extra HTTP requests", got-wantHits)
	}
	if got := atomic.LoadInt32(&hookCalls); got != 1 {
		t.Fatalf("the abort hook ran %d times, want exactly 1", got)
	}
}

// TestSystemicLatchKeepsTheCause checks the report names what actually happened,
// not just that something did.
func TestSystemicLatchKeepsTheCause(t *testing.T) {
	c, _ := testClient(t, jsonBody(t, map[string]any{
		"code": CodeRetryable, "msg": "frequency limit",
	}))
	for i := 0; i < systemicStrikes; i++ {
		_, _ = c.do(context.Background(), request{where: "files.upload_all", path: "/x"})
	}

	cause := c.systemicError()
	if cause == nil {
		t.Fatal("systemicError is nil after the latch tripped")
	}
	if !strings.Contains(cause.Error(), "1061045") {
		t.Errorf("the cause should keep Feishu's code, got: %v", cause)
	}
}

// TestSuccessResetsTheStrikeCount guards against a false positive: a run that is
// merely slow, not dead, must not be aborted.
//
// Two exhausted calls, one success, two more exhausted calls is a working
// connection with occasional rejections - the exact situation our own limiter
// exists to create.
func TestSuccessResetsTheStrikeCount(t *testing.T) {
	var block int32 = 1 // 1 = fail, 0 = succeed
	c, hits := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.LoadInt32(&block) == 1 {
			_, _ = w.Write([]byte(`{"code":1061045,"msg":"frequency limit"}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
	})

	call := func() error {
		_, err := c.do(context.Background(), request{where: "files.list", path: "/x"})
		return err
	}

	// Two strikes, then success (resets), then two more strikes: never three in
	// a row, so the client must still be usable. Each call is made separately -
	// writing `call() == nil || call() == nil` would short-circuit and only make
	// one of them.
	for i := 0; i < 2; i++ {
		if call() == nil {
			t.Fatalf("call %d: expected a failure", i+1)
		}
	}
	atomic.StoreInt32(&block, 0)
	if err := call(); err != nil {
		t.Fatalf("expected success after unblocking, got %v", err)
	}
	atomic.StoreInt32(&block, 1)
	for i := 0; i < 2; i++ {
		if call() == nil {
			t.Fatalf("call %d after the reset: expected a failure", i+2)
		}
	}

	if c.systemic.Load() {
		t.Fatal("the latch tripped even though a success reset the count")
	}
	before := atomic.LoadInt32(hits)

	// Still alive: the next call goes to the network as usual.
	atomic.StoreInt32(&block, 0)
	if err := call(); err != nil {
		t.Fatalf("a latched-free client must still work, got %v", err)
	}
	if atomic.LoadInt32(hits) <= before {
		t.Fatal("the client stopped calling the server without tripping the latch")
	}
}

// TestSystemicDoesNotCountPerFileFailures makes sure a handful of individually
// broken files cannot abort a healthy run.
//
// Permission and "not found" failures are non-retryable, so they fail once and
// never reach noteExhausted. If they did count, three files with odd names would
// stop a 74,313-file migration.
func TestSystemicDoesNotCountPerFileFailures(t *testing.T) {
	c, _ := testClient(t, jsonBody(t, map[string]any{
		"code": CodeForbidden, "msg": "no permission",
	}))

	for i := 0; i < 10; i++ {
		_, err := c.do(context.Background(), request{where: "files.upload_all", path: "/x"})
		if err == nil {
			t.Fatal("expected the permission error to fail")
		}
	}
	if c.systemic.Load() {
		t.Fatal("non-retryable per-file failures must not trip the systemic latch")
	}
}

// TestSystemicLatchIsSafeUnderConcurrency covers the real transfer path: rclone
// uploads several files at once, so the latch is set from several goroutines.
func TestSystemicLatchIsSafeUnderConcurrency(t *testing.T) {
	c, _ := testClient(t, jsonBody(t, map[string]any{
		"code": CodeRetryable, "msg": "frequency limit",
	}))

	var hookCalls int32
	c.onSystemic = func() { atomic.AddInt32(&hookCalls, 1) }

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 3; j++ {
				_, _ = c.do(context.Background(), request{where: "files.upload_all", path: "/x"})
			}
		}()
	}
	wg.Wait()

	if !c.systemic.Load() {
		t.Fatal("the latch never tripped")
	}
	// Only the CompareAndSwap winner may run the hook, or a cancelled context
	// would be cancelled several times over.
	time.Sleep(10 * time.Millisecond)
	if got := atomic.LoadInt32(&hookCalls); got != 1 {
		t.Fatalf("abort hook ran %d times under concurrency, want exactly 1", got)
	}
}

// TestSystemicDownloadStops covers the streaming path, which has its own retry
// loop because it must not buffer whole files.
func TestSystemicDownloadStops(t *testing.T) {
	c, hits := testClient(t, jsonBody(t, map[string]any{
		"code": CodeRetryable, "msg": "frequency limit",
	}))

	for i := 0; i < systemicStrikes; i++ {
		if _, err := c.downloadStream(context.Background(), "tok", ""); err == nil {
			t.Fatalf("download %d: expected a failure", i+1)
		}
	}
	before := atomic.LoadInt32(hits)
	if _, err := c.downloadStream(context.Background(), "tok", ""); !errors.Is(err, ErrSystemicFailure) {
		t.Fatalf("want ErrSystemicFailure after the latch, got %v", err)
	}
	if atomic.LoadInt32(hits) != before {
		t.Fatal("a latched download still hit the network")
	}
}
