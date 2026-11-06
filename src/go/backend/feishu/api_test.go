package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testClient wires a client to an httptest server that also serves tokens.
//
// Every request is counted so tests can assert on retry behaviour.
func testClient(t *testing.T, handler http.HandlerFunc) (*client, *int32) {
	t.Helper()

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/tenant_access_token/internal") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "tenant_access_token": "t-test", "expire": 7200,
			})
			return
		}
		atomic.AddInt32(&hits, 1)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	hc := srv.Client()
	hc.Timeout = 5 * time.Second
	ts := newTokenSource("id", "secret", srv.URL, hc)
	c := newClient(srv.URL, ts, hc)
	// Never sleep in tests; record nothing.
	c.sleep = func(time.Duration) {}
	c.rand01 = func() float64 { return 0.5 }
	return c, &hits
}

func jsonBody(t *testing.T, v any) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
}

// E1: a missing-scope response becomes MissingScopeError naming the scopes.
func TestClientMissingScopeError(t *testing.T) {
	c, _ := testClient(t, jsonBody(t, map[string]any{
		"code": CodeMissingScope,
		"msg":  "Access denied. One of the following scopes is required: [drive:drive, space:folder:create]",
	}))

	_, err := c.do(context.Background(), request{
		where: "files.create_folder", method: http.MethodPost, path: "/x",
	})
	if err == nil {
		t.Fatal("expected an error")
	}

	var scopeErr *MissingScopeError
	if !errors.As(err, &scopeErr) {
		t.Fatalf("error type = %T, want *MissingScopeError", err)
	}
	if len(scopeErr.Scopes) != 2 {
		t.Fatalf("scopes = %v, want 2 entries", scopeErr.Scopes)
	}
	msg := err.Error()
	for _, want := range []string{"drive:drive", "space:folder:create", "open.feishu.cn/app"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q\ngot: %s", want, msg)
		}
	}
}

// E5: 1061045 is retried and eventually gives up, reporting the attempt count.
func TestClientRetriesRetryableCode(t *testing.T) {
	c, hits := testClient(t, jsonBody(t, map[string]any{
		"code": CodeRetryable, "msg": "please retry",
	}))

	_, err := c.do(context.Background(), request{
		where: "files.list", method: http.MethodGet, path: "/x",
	})
	if err == nil {
		t.Fatal("expected an error after exhausting retries")
	}
	if got := atomic.LoadInt32(hits); got != maxAttempts {
		t.Errorf("attempts = %d, want %d", got, maxAttempts)
	}
	if !strings.Contains(err.Error(), "giving up after") {
		t.Errorf("error should mention giving up\ngot: %v", err)
	}
}

// A non-retryable code must fail on the first attempt.
func TestClientDoesNotRetryPermissionError(t *testing.T) {
	c, hits := testClient(t, jsonBody(t, map[string]any{
		"code": CodeForbidden, "msg": "forbidden",
	}))

	_, err := c.do(context.Background(), request{
		where: "files.list", method: http.MethodGet, path: "/x",
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := atomic.LoadInt32(hits); got != 1 {
		t.Errorf("attempts = %d, want 1 (must not retry a permission error)", got)
	}
}

// http 401 → refresh token once and retry, then succeed.
func TestClientRefreshesTokenOn401(t *testing.T) {
	var calls int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/tenant_access_token/internal") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "tenant_access_token": "t-test", "expire": 7200,
			})
			return
		}
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":99991663,"msg":"token invalid"}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"files":[]}}`))
	}))
	defer srv.Close()

	hc := srv.Client()
	ts := newTokenSource("id", "secret", srv.URL, hc)
	c := newClient(srv.URL, ts, hc)
	c.sleep = func(time.Duration) {}

	res, err := c.do(context.Background(), request{
		where: "files.list", method: http.MethodGet, path: "/x",
	})
	if err != nil {
		t.Fatalf("expected success after token refresh, got: %v", err)
	}
	if res == nil {
		t.Fatal("nil result")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("attempts = %d, want 2", got)
	}
}

// 5xx is retryable.
func TestClientRetries5xx(t *testing.T) {
	var calls int32
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"files":[]}}`))
	})

	if _, err := c.do(context.Background(), request{
		where: "files.list", method: http.MethodGet, path: "/x",
	}); err != nil {
		t.Fatalf("expected success on third attempt, got: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("attempts = %d, want 3", got)
	}
}

// E8: an unknown code surfaces the number and the log id.
func TestClientUnknownCodeIsInformative(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":88888,"msg":"weird","error":{"log_id":"log-xyz"}}`))
	})

	_, err := c.do(context.Background(), request{
		where: "files.list", method: http.MethodGet, path: "/x",
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()
	for _, want := range []string{"88888", "weird", "log-xyz"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q\ngot: %s", want, msg)
		}
	}
}

// E9: no transport error message may leak the bearer token.
func TestClientDoesNotLeakToken(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("not json"))
	})

	_, err := c.do(context.Background(), request{
		where: "files.list", method: http.MethodGet, path: "/x",
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "t-test") {
		t.Errorf("error leaks the bearer token: %v", err)
	}
}

func TestScopesFromMessage(t *testing.T) {
	got := scopesFromMessage("required: [drive:drive, space:folder:create]")
	if len(got) != 2 || got[0] != "drive:drive" || got[1] != "space:folder:create" {
		t.Errorf("scopesFromMessage = %v", got)
	}
	if got := scopesFromMessage("no brackets here"); got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestJitterStaysWithinBounds(t *testing.T) {
	c := &client{rand01: func() float64 { return 0 }}
	if got := c.jitter(time.Second); got != 800*time.Millisecond {
		t.Errorf("jitter(min) = %v, want 800ms", got)
	}
	c.rand01 = func() float64 { return 0.999 }
	if got := c.jitter(time.Second); got > maxBackoff || got < 1199*time.Millisecond {
		t.Errorf("jitter(max) = %v, want ~1.2s", got)
	}
	// Must never exceed the cap.
	if got := c.jitter(time.Hour); got > maxBackoff {
		t.Errorf("jitter cap = %v, want <= %v", got, maxBackoff)
	}
}
