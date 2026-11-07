package feishu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rclone/rclone/fs/config/obscure"
)

// Why the stored app_secret needs candidates rather than a rule is documented in
// secret.go. These tests pin down the three cases that matter.

// rclone's own tooling writes an obscured secret; it must be usable.
func TestSecretCandidatesRevealsObscured(t *testing.T) {
	secret := strings.Repeat("ab12CD34", 4) // 32 alphanumeric chars: a real secret's shape
	enc, err := obscure.Obscure(secret)
	if err != nil {
		t.Fatalf("Obscure: %v", err)
	}
	if enc == secret {
		t.Fatal("Obscure returned its input, so this test proves nothing")
	}

	got := secretCandidates(enc)
	if len(got) != 2 {
		t.Fatalf("candidates = %d, want 2 (revealed, then as-stored)", len(got))
	}
	if got[0] != secret {
		t.Errorf("the first candidate is not the revealed secret (len %d, want %d)",
			len(got[0]), len(secret))
	}
	if got[1] != enc {
		t.Error("the as-stored form should be kept as a fallback")
	}
}

// A hand-written plaintext secret must be used as-is, with no doomed probe.
//
// This is the case where Reveal "succeeds" and returns noise: the value is valid
// base64url and long enough for the 16-byte minimum, so the error path never
// fires.
func TestSecretCandidatesKeepsPlaintext(t *testing.T) {
	secret := strings.Repeat("ab12CD34", 4)

	got := secretCandidates(secret)
	if len(got) != 1 || got[0] != secret {
		t.Errorf("candidates = %q, want just the plaintext value", got)
	}
}

func TestSecretCandidatesEmpty(t *testing.T) {
	if got := secretCandidates(""); got != nil {
		t.Errorf("empty secret produced %q, want nil", got)
	}
}

// A hand-written secret of an unusual shape must still work: Reveal may fail on it
// (good) or succeed with noise (harmless, the retry covers it).
func TestSecretCandidatesAcceptsOddPlaintext(t *testing.T) {
	for _, s := range []string{"x", "short", "has spaces and symbols!@#$%^&*()"} {
		got := secretCandidates(s)
		if len(got) == 0 || got[len(got)-1] != s {
			t.Errorf("%q: the as-stored form must always be among the candidates, got %q", s, got)
		}
	}
}

// The option must stay IsPassword: without the flag rclone's own tooling writes
// the secret in plaintext, which is the thing this whole mechanism works around.
func TestAppSecretIsDeclaredAsPassword(t *testing.T) {
	for _, opt := range backendOptions() {
		if opt.Name != "app_secret" {
			continue
		}
		if !opt.IsPassword {
			t.Error("app_secret must keep IsPassword = true")
		}
		return
	}
	t.Fatal("app_secret option not found")
}

// When the first secret is rejected, the alternate must be tried and must become
// the one used from then on.
//
// This is the end-to-end proof of the fix: an obscured secret presented to a
// server that only accepts the plaintext form has to end up authenticated, and the
// second call must not probe again.
func TestTokenSourceRetriesWithAlternateSecret(t *testing.T) {
	const wantSecret = "the-real-secret"

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["app_secret"] != wantSecret {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 10014, "msg": "app secret invalid",
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0, "tenant_access_token": "t-ok", "expire": 7200,
		})
	}))
	defer srv.Close()

	// Primary is wrong, alternate is right - exactly the "obscured secret read as
	// plaintext" situation.
	ts := newTokenSource("id", "wrong-form", srv.URL, srv.Client())
	ts.altSecret = wantSecret

	token, err := ts.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if token != "t-ok" {
		t.Errorf("token = %q", token)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("requests = %d, want 2 (rejected, then accepted)", got)
	}
	if ts.altSecret != "" {
		t.Error("the alternate should be cleared once it wins")
	}
	if ts.appSecret != wantSecret {
		t.Error("the winning secret should replace the primary, so refreshes do not probe again")
	}

	// A later refresh must not probe. Total requests is the honest measure: one
	// more for the refresh, and no fourth attempt at the rejected form.
	ts.mu.Lock()
	ts.token = ""
	ts.mu.Unlock()
	if _, err := ts.Token(context.Background()); err != nil {
		t.Fatalf("second Token: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("requests = %d after a refresh, want 3 (the rejected form must not be retried)", got)
	}
}

// Both forms failing must produce one clear error, not a retry loop.
func TestTokenSourceBothFormsRejected(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 10014, "msg": "app secret invalid"})
	}))
	defer srv.Close()

	ts := newTokenSource("id", "first", srv.URL, srv.Client())
	ts.altSecret = "second"

	_, err := ts.Token(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("requests = %d, want exactly 2 (one per form)", got)
	}
	if !strings.Contains(err.Error(), "10014") {
		t.Errorf("the error should carry the API code, got: %v", err)
	}
	// The message must say both forms were tried, because that is what tells the
	// user the secret itself is wrong rather than the file's encoding.
	if !strings.Contains(err.Error(), "both") {
		t.Errorf("the error should mention that both forms were tried, got: %v", err)
	}
}

// With no alternate there is exactly one attempt.
func TestTokenSourceSingleAttemptWithoutAlternate(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 10014, "msg": "nope"})
	}))
	defer srv.Close()

	ts := newTokenSource("id", "only", srv.URL, srv.Client())
	if _, err := ts.Token(context.Background()); err == nil {
		t.Fatal("expected an error")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("requests = %d, want 1", got)
	}
}

// A network failure must not be retried with the other secret: the network is
// broken, and a second identical failure only doubles the delay.
func TestTokenSourceNoRetryOnTransportError(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("cannot hijack")
			return
		}
		conn, _, err := hj.Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
	defer srv.Close()

	ts := newTokenSource("id", "first", srv.URL, srv.Client())
	ts.altSecret = "second"

	if _, err := ts.Token(context.Background()); err == nil {
		t.Fatal("expected an error")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("requests = %d, want 1 (a transport failure is not a credential problem)", got)
	}
}
