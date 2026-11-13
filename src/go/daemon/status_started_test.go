package daemon

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// statusOnce calls /api/status and decodes only the fields this file cares about.
func statusOnce(t *testing.T, api *API) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var v map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return v
}

// The page compares `started` against the value it was loaded with and reloads
// when it changes. Two things therefore have to hold, and the second one is the
// dangerous one:
//
//  1. the field exists, because a missing field silently disables the feature;
//  2. it is STABLE within one process. If it changed between polls the page would
//     reload itself every three seconds - an infinite reload loop, which is a far
//     worse bug than the stale page this was added to fix.
func TestStatusStartedIsStable(t *testing.T) {
	api, start := newInfoAPI(t)

	first := statusOnce(t, api)
	raw, ok := first["started"].(string)
	if !ok || raw == "" {
		t.Fatalf("started missing from /api/status: %v", first)
	}
	got, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		t.Fatalf("started is not RFC3339: %q (%v)", raw, err)
	}
	if !got.Equal(start) {
		t.Errorf("started = %v, want the API's StartedAt %v", got, start)
	}

	// Ten more polls: identical every time, to the nanosecond.
	for i := 0; i < 10; i++ {
		if again := statusOnce(t, api)["started"]; again != raw {
			t.Fatalf("started changed between polls: %q -> %q", raw, again)
		}
	}
}

// A daemon started without an explicit time still needs one stable answer;
// otherwise every poll would look like a restart.
func TestStatusStartedDefaultsOnce(t *testing.T) {
	dir := t.TempDir()
	d, err := New([]string{writeJobFile(t, dir, "a", simpleJob("a", ""))}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	api := &API{D: d, Addr: "127.0.0.1:1"} // StartedAt deliberately zero

	first := statusOnce(t, api)["started"]
	if first == nil || first == "" {
		t.Fatal("started missing when StartedAt was never set")
	}
	time.Sleep(5 * time.Millisecond)
	if again := statusOnce(t, api)["started"]; again != first {
		t.Errorf("started changed after the default was taken: %v -> %v", first, again)
	}
	if api.StartedAt.IsZero() {
		t.Error("the default should have been recorded on the API, not invented per request")
	}
}
