package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// T011: token acquisition, caching, proactive refresh, invalidate.
func TestTokenAcquireAndCache(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json; charset=utf-8" {
			t.Errorf("content-type = %q", got)
		}
		var req map[string]string
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if req["app_id"] != "id" || req["app_secret"] != "secret" {
			t.Errorf("credentials not forwarded: %v", req)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0, "msg": "ok",
			"tenant_access_token": "t-abc",
			"expire":              7200,
		})
	}))
	defer srv.Close()

	ts := newTokenSource("id", "secret", srv.URL, srv.Client())

	tok, err := ts.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok != "t-abc" {
		t.Fatalf("token = %q, want t-abc", tok)
	}

	// Second call must reuse the cache: no extra HTTP request.
	if _, err := ts.Token(context.Background()); err != nil {
		t.Fatalf("Token (cached): %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("refresh calls = %d, want 1 (cache should be reused)", got)
	}
}

// FR-003: refresh proactively when fewer than 30 minutes remain.
func TestTokenRefreshesBeforeExpiry(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code":                0,
			"tenant_access_token": fmt.Sprintf("t-%d", n),
			"expire":              7200,
		})
	}))
	defer srv.Close()

	ts := newTokenSource("id", "secret", srv.URL, srv.Client())

	// Controlled clock so the refresh boundary is deterministic.
	base := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	now := base
	ts.now = func() time.Time { return now }

	first, err := ts.Token(context.Background())
	if err != nil {
		t.Fatalf("first Token: %v", err)
	}

	// Token lives 120 minutes. At 80 minutes in, 40 remain: more than the
	// 30-minute margin, so the cache must be reused.
	now = base.Add(80 * time.Minute)
	second, err := ts.Token(context.Background())
	if err != nil {
		t.Fatalf("second Token: %v", err)
	}
	if second != first {
		t.Errorf("token changed too early: %q -> %q", first, second)
	}

	// At 100 minutes in only 20 remain, which is inside the margin: refresh.
	now = base.Add(100 * time.Minute)
	third, err := ts.Token(context.Background())
	if err != nil {
		t.Fatalf("third Token: %v", err)
	}
	if third == first {
		t.Errorf("token was not refreshed with <30m remaining (still %q)", third)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("refresh calls = %d, want 2", got)
	}
}

// T010: Invalidate discards the cached value.
func TestTokenInvalidate(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code":                0,
			"tenant_access_token": fmt.Sprintf("t-%d", n),
			"expire":              7200,
		})
	}))
	defer srv.Close()

	ts := newTokenSource("id", "secret", srv.URL, srv.Client())

	a, _ := ts.Token(context.Background())
	ts.Invalidate()
	b, err := ts.Token(context.Background())
	if err != nil {
		t.Fatalf("Token after Invalidate: %v", err)
	}
	if a == b {
		t.Errorf("token not refreshed after Invalidate: %q", b)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("refresh calls = %d, want 2", got)
	}
}

// A business-level error must surface, and must not echo the body verbatim
// (the body may contain a partially valid token).
func TestTokenErrorIsActionable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 10003,
			"msg":  "invalid param",
		})
	}))
	defer srv.Close()

	ts := newTokenSource("bad", "bad", srv.URL, srv.Client())
	_, err := ts.Token(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()
	for _, want := range []string{"10003", "app_id", "app_secret"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error should mention %q, got: %s", want, msg)
		}
	}
}

// A transport-level failure must be reported, not swallowed.
func TestTokenNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // now unreachable

	ts := newTokenSource("id", "secret", url, &http.Client{Timeout: 200 * time.Millisecond})
	if _, err := ts.Token(context.Background()); err == nil {
		t.Fatal("expected a network error")
	}
}
