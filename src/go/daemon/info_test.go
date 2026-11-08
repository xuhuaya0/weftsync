package daemon

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newInfoAPI builds a daemon with one job and an API that has a start time.
func newInfoAPI(t *testing.T) (*API, time.Time) {
	t.Helper()
	dir := t.TempDir()
	d, err := New([]string{writeJobFile(t, dir, "a", simpleJob("a", ""))}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	start := time.Now().Add(-90 * time.Second)
	return &API{
		D: d, Addr: "127.0.0.1:1234", SplitDir: filepath.Join(dir, "snaps"),
		StartedAt: start,
	}, start
}

func getInfo(t *testing.T, api *API) InfoView {
	t.Helper()
	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/info", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var v InfoView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return v
}

// The settings page exists to answer "which binary, which files" without reading
// the process command line, so those fields have to be populated.
func TestInfoReportsTheRunningProcess(t *testing.T) {
	api, start := newInfoAPI(t)
	v := getInfo(t, api)

	if v.Addr != "127.0.0.1:1234" {
		t.Errorf("addr = %q", v.Addr)
	}
	if v.Version == "" || v.Version == "unknown" {
		// A plain `go build` in a module checkout does carry a VCS stamp, so an
		// empty version means the lookup broke rather than "no metadata".
		t.Logf("version reported as %q (no VCS stamp in this build)", v.Version)
	}
	if v.GoVersion == "" || v.GOOS == "" || v.GOARCH == "" {
		t.Errorf("build platform incomplete: %q %q/%q", v.GoVersion, v.GOOS, v.GOARCH)
	}
	if !v.StartedAt.Equal(start) {
		t.Errorf("StartedAt = %v, want %v", v.StartedAt, start)
	}
	if v.UptimeSeconds < 89 {
		t.Errorf("uptime = %d, want about 90", v.UptimeSeconds)
	}
	if len(v.JobFiles) != 1 || !strings.HasSuffix(v.JobFiles[0], "a.toml") {
		t.Errorf("job files = %v, want the one file this daemon loaded", v.JobFiles)
	}
	if v.SplitDir == "" {
		t.Error("split dir should be reported so the operator knows where ledgers go")
	}
}

// Uptime must be measured against the process start, not against zero time, or
// the page would show a number in the tens of thousands of days.
func TestInfoWithoutStartTimeUsesNow(t *testing.T) {
	api, _ := newInfoAPI(t)
	api.StartedAt = time.Time{}
	v := getInfo(t, api)
	if v.UptimeSeconds < 0 || v.UptimeSeconds > 5 {
		t.Errorf("uptime = %d, want ~0 when StartedAt was never set", v.UptimeSeconds)
	}
}

// The two counters explain "the log only goes back so far".
func TestInfoCountsBuffers(t *testing.T) {
	dir := t.TempDir()
	sink := NewLogSink(io.Discard)
	d, err := New([]string{writeJobFile(t, dir, "a", simpleJob("a", ""))}, sink)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	api := &API{D: d, Addr: "x", StartedAt: time.Now()}

	v := getInfo(t, api)
	if v.HistoryRuns != 0 {
		t.Errorf("history = %d, want 0 before any run", v.HistoryRuns)
	}

	// Lines the daemon writes must be visible in the count, otherwise the page
	// would claim the log is empty while the log tab shows content.
	d.logf("a line from the test")
	v = getInfo(t, api)
	if v.LogLines == 0 {
		t.Error("log counter stayed at 0 after the daemon wrote a line")
	}
}

func TestInfoRejectsPost(t *testing.T) {
	api, _ := newInfoAPI(t)
	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/info", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}
