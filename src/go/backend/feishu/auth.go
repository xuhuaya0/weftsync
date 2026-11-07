package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rclone/rclone/fs"
)

// refreshMargin is how long before expiry a token is proactively refreshed.
// Feishu documents that calling the endpoint with less than 30 minutes left
// returns a NEW token while the old one stays valid, so refreshing early is
// cheap and avoids mid-request expiry.
const refreshMargin = 30 * time.Minute

// tokenSource caches a tenant_access_token and refreshes it on demand.
//
// FR-002: cache and reuse. FR-003: refresh before expiry; a failed refresh must
// surface as an error rather than silently reusing a stale token.
type tokenSource struct {
	appID     string
	appSecret string
	baseURL   string
	http      *http.Client
	now       func() time.Time // injectable for tests

	// altSecret is the other interpretation of the stored secret (see secret.go).
	// It is tried once when the primary is rejected, and the winner replaces
	// appSecret so later refreshes do not re-probe.
	//
	// Set after construction rather than passed in, to keep the constructor's
	// signature (and every existing call site) unchanged.
	altSecret string

	mu      sync.Mutex
	token   string
	expires time.Time
}

func newTokenSource(appID, appSecret, baseURL string, hc *http.Client) *tokenSource {
	return &tokenSource{
		appID:     appID,
		appSecret: appSecret,
		baseURL:   strings.TrimRight(baseURL, "/"),
		http:      hc,
		now:       time.Now,
	}
}

type tokenResponse struct {
	Code              int    `json:"code"`
	Msg               string `json:"msg"`
	TenantAccessToken string `json:"tenant_access_token"`
	Expire            int    `json:"expire"` // seconds
}

// Token returns a valid tenant_access_token, refreshing it when needed.
func (ts *tokenSource) Token(ctx context.Context) (string, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	if ts.token != "" && ts.now().Add(refreshMargin).Before(ts.expires) {
		return ts.token, nil
	}

	tr, err := ts.fetch(ctx, ts.appSecret)
	if err != nil {
		return "", err
	}

	// The secret may have been read in the wrong form: rclone's tooling stores it
	// obscured while a hand-written config holds plaintext, and no structural test
	// separates them (secret.go). Retry once with the other interpretation rather
	// than guessing. Nothing else is retried here - a network error or a bad
	// app_id would fail the same way twice.
	if tr.Code != 0 && ts.altSecret != "" && ts.altSecret != ts.appSecret {
		alt, altErr := ts.fetch(ctx, ts.altSecret)
		if altErr == nil && alt.Code == 0 {
			fs.Debugf(nil, "feishu: the stored app_secret was obscured; using the revealed form")
			// Remember the winner: tokens are refreshed periodically and there is
			// no reason to probe again every time.
			ts.appSecret, ts.altSecret = ts.altSecret, ""
			tr = alt
		}
	}

	if tr.Code != 0 {
		// Never echo the body: it may contain a partially valid token.
		hint := "Check app_id / app_secret, and that the app still exists and is published"
		if ts.altSecret != "" {
			hint += " (both the plaintext and the obscured form of app_secret were tried)"
		}
		return "", fmt.Errorf(
			"feishu auth: could not obtain tenant_access_token (code=%d msg=%s). %s",
			tr.Code, tr.Msg, hint)
	}
	if tr.TenantAccessToken == "" {
		return "", fmt.Errorf("feishu auth: response contained an empty tenant_access_token")
	}

	expire := tr.Expire
	if expire <= 0 {
		expire = 7200 // documented default
	}

	ts.token = tr.TenantAccessToken
	ts.expires = ts.now().Add(time.Duration(expire) * time.Second)
	return ts.token, nil
}

// fetch performs one token request with the given secret.
//
// A non-zero code is returned as a successful call with Code set: the caller
// decides whether that is worth retrying with a different secret.
func (ts *tokenSource) fetch(ctx context.Context, secret string) (tokenResponse, error) {
	body, err := json.Marshal(map[string]string{
		"app_id":     ts.appID,
		"app_secret": secret,
	})
	if err != nil {
		return tokenResponse{}, fmt.Errorf("feishu auth: encode request: %w", err)
	}

	url := ts.baseURL + "/open-apis/auth/v3/tenant_access_token/internal"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("feishu auth: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := ts.http.Do(req)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("feishu auth: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("feishu auth: read response: %w", err)
	}

	var tr tokenResponse
	if err := json.Unmarshal(raw, &tr); err != nil {
		return tokenResponse{}, fmt.Errorf("feishu auth: decode response (http %d): %w",
			resp.StatusCode, err)
	}
	return tr, nil
}

// Invalidate forces the next Token call to fetch a fresh token.
//
// Called when the API answers with CodeTokenInvalid. Deliberately does not keep
// a grace window: after an explicit rejection the cached value is worthless.
func (ts *tokenSource) Invalidate() {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.token = ""
	ts.expires = time.Time{}
}
