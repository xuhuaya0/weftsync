package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// shrinkBackoff makes the retry loop fast enough to test. Restored on cleanup so
// the next test in the package is not affected.
func shrinkBackoff(t *testing.T) {
	t.Helper()
	prev := blockRetryBaseWait
	blockRetryBaseWait = time.Millisecond
	t.Cleanup(func() { blockRetryBaseWait = prev })
}

// retryServer answers prepare/finish normally and lets a test decide what each
// upload_part call does.
func retryServer(t *testing.T, blockSize int64, blockNum int, part func(call int32) (code int, done bool)) (*client, *int32) {
	t.Helper()
	var calls int32
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/files/upload_prepare"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"upload_id": "up", "block_size": blockSize, "block_num": blockNum,
				},
			})
		case strings.HasSuffix(r.URL.Path, "/files/upload_part"):
			n := atomic.AddInt32(&calls, 1)
			code, done := part(n)
			if done {
				_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "msg": "transient"})
		case strings.HasSuffix(r.URL.Path, "/files/upload_finish"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "data": map[string]any{"file_token": "tok"},
			})
		default:
			t.Errorf("unexpected call %s", r.URL.Path)
		}
	})
	return c, &calls
}

// A transient failure on one block must recover in place. Without this the only
// recovery is rclone restarting the whole file, which for a 1GB upload means
// re-sending everything.
func TestBlockRetryRecoversTransientFailure(t *testing.T) {
	shrinkBackoff(t)
	const blockSize = 1024
	// The first two attempt(s) of the FIRST block fail; the third succeeds, and
	// every later block succeeds on its first try.
	c, calls := retryServer(t, blockSize, 2, func(n int32) (int, bool) {
		return CodeRetryable, n > 2
	})
	f := &Fs{name: "test", client: c}

	_, err := f.putChunked(context.Background(), bytes.NewReader(content(blockSize*2)),
		"big.bin", "big.bin", "parent", blockSize*2)
	if err != nil {
		t.Fatalf("a retryable block failure should be retried, got: %v", err)
	}
	// 2 failed attempts + 1 success for block 0, then 1 for block 1.
	if got := atomic.LoadInt32(calls); got != 4 {
		t.Errorf("upload_part calls = %d, want 4 (2 failures, 1 retry, 1 next block)", got)
	}
}

// A permission error is not going to succeed on the fourth attempt; retrying it
// burns the daily request budget and delays the message the user needs.
func TestBlockRetryStopsOnPermanentError(t *testing.T) {
	shrinkBackoff(t)
	const blockSize = 1024
	c, calls := retryServer(t, blockSize, 1, func(n int32) (int, bool) {
		return CodeForbidden, false
	})
	f := &Fs{name: "test", client: c}

	_, err := f.putChunked(context.Background(), bytes.NewReader(content(blockSize)),
		"big.bin", "big.bin", "parent", blockSize)
	if err == nil {
		t.Fatal("a permanent error must fail the upload")
	}
	// client.do does not retry a permission error either, so exactly one call.
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Errorf("upload_part calls = %d, want 1 (no retry for a permanent error)", got)
	}
	if !strings.Contains(err.Error(), "block 1/1") {
		t.Errorf("the error should name the block\ngot: %v", err)
	}
}

// When the budget runs out the message has to say so: "failed" alone hides the
// fact that the same block was tried four times.
func TestBlockRetryReportsExhaustion(t *testing.T) {
	shrinkBackoff(t)
	const blockSize = 1024
	c, calls := retryServer(t, blockSize, 1, func(n int32) (int, bool) {
		return CodeRetryable, false
	})
	f := &Fs{name: "test", client: c}

	_, err := f.putChunked(context.Background(), bytes.NewReader(content(blockSize)),
		"big.bin", "big.bin", "parent", blockSize)
	if err == nil {
		t.Fatal("expected exhaustion to fail the upload")
	}
	// client.do already retries maxAttempts times per call, and we make
	// blockRetries calls, so the server sees the product of the two.
	want := int32(maxAttempts * blockRetries)
	if got := atomic.LoadInt32(calls); got != want {
		t.Errorf("upload_part calls = %d, want %d", got, want)
	}
	if !strings.Contains(err.Error(), "after 4 attempt(s)") {
		t.Errorf("the error should report the attempt count\ngot: %v", err)
	}
	if !strings.Contains(err.Error(), "24 hours") {
		t.Errorf("the error should explain what happens to the unfinished upload\ngot: %v", err)
	}
}

// A stop request during the backoff must not wait out the timer.
func TestBlockRetryHonoursCancellation(t *testing.T) {
	prev := blockRetryBaseWait
	blockRetryBaseWait = 30 * time.Second
	t.Cleanup(func() { blockRetryBaseWait = prev })

	const blockSize = 1024
	c, _ := retryServer(t, blockSize, 1, func(n int32) (int, bool) {
		return CodeRetryable, false
	})
	f := &Fs{name: "test", client: c}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// Long enough for client.do's own retries to finish, short enough that
		// the test fails if the sleep is not interruptible.
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	done := make(chan error, 1)
	go func() {
		_, err := f.putChunked(ctx, bytes.NewReader(content(blockSize)),
			"big.bin", "big.bin", "parent", blockSize)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a cancelled upload must report an error")
		}
		if !strings.Contains(err.Error(), "stopped") &&
			!strings.Contains(err.Error(), "context canceled") {
			t.Errorf("the error should mention the cancellation\ngot: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the upload ignored the cancelled context and slept out the backoff")
	}
}
