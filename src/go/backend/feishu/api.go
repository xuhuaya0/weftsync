package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Retry policy (contracts/error-codes.md §2).
const (
	maxAttempts     = 3
	initialBackoff  = 1 * time.Second
	maxBackoff      = 10 * time.Second
	defaultTimeout  = 60 * time.Second
	maxResponseSize = 8 << 20 // 8MB, generous for JSON listings
)

// client is a thin Feishu Open API transport.
//
// Responsibilities: attach the bearer token, decode the envelope, translate
// Feishu codes into typed errors, and retry only what is safe to retry.
type client struct {
	baseURL string
	ts      *tokenSource
	http    *http.Client

	// sleep is injectable so tests do not actually wait.
	sleep func(time.Duration)
	// rand01 provides jitter in [0,1).
	rand01 func() float64
}

func newClient(baseURL string, ts *tokenSource, hc *http.Client) *client {
	if hc == nil {
		hc = &http.Client{Timeout: defaultTimeout}
	}
	return &client{
		baseURL: strings.TrimRight(baseURL, "/"),
		ts:      ts,
		http:    hc,
		sleep:   time.Sleep,
		rand01:  rand.Float64,
	}
}

// envelope is the common Feishu response shell.
type envelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// request describes one API call.
type request struct {
	where       string // e.g. "files.list", used in error messages
	method      string
	path        string
	query       url.Values
	body        []byte
	contentType string
	// rawResponse makes the transport return the response body untouched
	// instead of decoding the JSON envelope (used for downloads).
	rawResponse bool
	// extraHeaders are added after the auth header.
	extraHeaders map[string]string
}

// result carries either a decoded data payload, or raw bytes for downloads.
type result struct {
	Data   json.RawMessage
	Body   []byte
	Header http.Header
	Status int
}

// do executes the request with retries.
//
// Retry rules (contracts/error-codes.md §2):
//   - retryable business codes and 5xx/429/network errors: exponential backoff
//   - token invalid: refresh + retry once, no backoff loop
//   - everything else: fail immediately
func (c *client) do(ctx context.Context, r request) (*result, error) {
	var (
		lastErr      error
		backoff      = initialBackoff
		tokenRetried bool
	)

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		res, err := c.once(ctx, r)
		if err == nil {
			return res, nil
		}
		lastErr = err

		switch classify(err) {
		case classTokenInvalid:
			// Refresh and retry immediately; only once, to avoid two live
			// tokens ping-ponging (FR-003 rationale).
			if tokenRetried {
				return nil, err
			}
			tokenRetried = true
			c.ts.Invalidate()
			continue

		case classRetryable:
			if attempt == maxAttempts {
				return nil, fmt.Errorf("%s: giving up after %d attempts: %w", r.where, attempt, err)
			}
			c.sleep(c.jitter(backoff))
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue

		default:
			return nil, err
		}
	}

	return nil, lastErr
}

// jitter applies +/-20% to d.
func (c *client) jitter(d time.Duration) time.Duration {
	factor := 0.8 + 0.4*c.rand01() // [0.8, 1.2)
	out := time.Duration(float64(d) * factor)
	if out > maxBackoff {
		out = maxBackoff
	}
	return out
}

// once performs a single HTTP round trip and translates the outcome.
func (c *client) once(ctx context.Context, r request) (*result, error) {
	token, err := c.ts.Token(ctx)
	if err != nil {
		return nil, err
	}

	u := c.baseURL + r.path
	if len(r.query) > 0 {
		u += "?" + r.query.Encode()
	}

	var bodyReader io.Reader
	if r.body != nil {
		bodyReader = bytes.NewReader(r.body)
	}

	req, err := http.NewRequestWithContext(ctx, r.method, u, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("%s: build request: %w", r.where, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if r.contentType != "" {
		req.Header.Set("Content-Type", r.contentType)
	}
	for k, v := range r.extraHeaders {
		req.Header.Set(k, v)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		// Transport failures are retryable: connection resets are common on
		// the network this project runs on.
		return nil, &retryableTransportError{where: r.where, err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, &retryableTransportError{where: r.where, err: fmt.Errorf("read body: %w", err)}
	}

	// Downloads return the file bytes directly; errors still come as JSON.
	if r.rawResponse && resp.StatusCode >= 200 && resp.StatusCode < 300 &&
		!strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		return &result{Body: raw, Header: resp.Header, Status: resp.StatusCode}, nil
	}

	// Try to decode the envelope. Non-JSON bodies on error paths are unusual
	// but must not be silently dropped.
	var env envelope
	jsonErr := json.Unmarshal(raw, &env)
	if jsonErr != nil {
		if resp.StatusCode >= 200 && resp.StatusCode < 300 && r.rawResponse {
			return &result{Body: raw, Header: resp.Header, Status: resp.StatusCode}, nil
		}
		return nil, c.httpStatusError(r.where, resp.StatusCode, raw, jsonErr)
	}

	if env.Code == 0 && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return &result{Data: env.Data, Header: resp.Header, Status: resp.StatusCode}, nil
	}

	return nil, c.businessError(r.where, env.Code, env.Msg, raw, resp.StatusCode)
}

// businessError maps a Feishu code to a typed error.
//
// Only codes listed in codeTable are turned into APIError. Everything else
// keeps its Feishu log id in an UncategorizedError, because guessing how to
// handle an unknown code could silently retry a permanent failure.
func (c *client) businessError(where string, code int, msg string, raw []byte, status int) error {
	if code == CodeMissingScope {
		return &MissingScopeError{
			Scopes: scopesFromMessage(msg),
			AppID:  c.ts.appID,
			Where:  where,
			RawMsg: msg,
		}
	}
	if _, known := codeTable[code]; known {
		return &APIError{Code: code, Msg: msg, Where: where}
	}
	return &UncategorizedError{
		Code:  code,
		Msg:   msg,
		Where: where,
		LogID: logIDFromBody(raw),
	}
}

// httpStatusError converts a non-JSON/unexpected HTTP failure.
func (c *client) httpStatusError(where string, status int, raw []byte, cause error) error {
	switch {
	case status == http.StatusUnauthorized:
		return &APIError{Code: CodeTokenInvalid, Msg: "http 401", Where: where}
	case status == http.StatusForbidden:
		return &APIError{Code: CodeForbidden, Msg: "http 403", Where: where}
	case status == http.StatusTooManyRequests || status >= 500:
		return &retryableTransportError{
			where: where,
			err:   fmt.Errorf("http %d", status),
		}
	default:
		return &UncategorizedError{
			Code:    status,
			Msg:     fmt.Sprintf("non-JSON response (first bytes: %q)", firstBytes(raw, 120)),
			Where:   where,
			Wrapped: cause,
		}
	}
}

// retryableTransportError signals a transient failure below the API layer.
type retryableTransportError struct {
	where string
	err   error
}

func (e *retryableTransportError) Error() string {
	return fmt.Sprintf("feishu %s: transient failure: %v", e.where, e.err)
}

func (e *retryableTransportError) Unwrap() error { return e.err }

// scopesFromMessage extracts the scope names Feishu lists in the error text,
// e.g. "required: [drive:drive, space:folder:create]".
func scopesFromMessage(msg string) []string {
	open := strings.Index(msg, "[")
	end := strings.Index(msg, "]")
	if open < 0 || end <= open {
		return nil
	}
	inner := msg[open+1 : end]
	var out []string
	for _, part := range strings.Split(inner, ",") {
		s := strings.TrimSpace(part)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// logIDFromBody digs out error.log_id when present.
func logIDFromBody(raw []byte) string {
	var probe struct {
		Error struct {
			LogID string `json:"log_id"`
		} `json:"error"`
		LogID string `json:"log_id"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return ""
	}
	if probe.Error.LogID != "" {
		return probe.Error.LogID
	}
	return probe.LogID
}

func firstBytes(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n])
	}
	return string(b)
}
