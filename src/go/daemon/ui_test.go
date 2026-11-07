package daemon

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"weftsync/job"
)

// The UI must always be present. A blank page is the least diagnosable failure
// a user can hit, so the asset being embedded is asserted, not assumed.
func TestUIAssetIsEmbedded(t *testing.T) {
	page, err := uiFS.ReadFile("ui/index.html")
	if err != nil {
		t.Fatalf("embedded UI missing: %v", err)
	}
	if len(page) < 500 {
		t.Fatalf("embedded UI is suspiciously small (%d bytes)", len(page))
	}
	for _, want := range []string{"<!DOCTYPE html>", "/api/status", "/api/run", "/api/history", "/api/log"} {
		if !strings.Contains(string(page), want) {
			t.Errorf("UI should reference %q", want)
		}
	}
}

// GET / must serve the page, not JSON and not 404.
func TestRootServesUI(t *testing.T) {
	api := &API{D: &Daemon{now: time.Now}}
	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store (the page polls its own API)", cc)
	}
	if !strings.Contains(rec.Body.String(), "<html") {
		t.Error("body is not HTML")
	}
}

// POST / is not a thing.
func TestRootRejectsPost(t *testing.T) {
	api := &API{D: &Daemon{now: time.Now}}
	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST / = %d, want 405", rec.Code)
	}
}

// History and log endpoints must return well-formed JSON even when empty, since
// the page parses them unconditionally.
func TestHistoryAndLogEndpoints(t *testing.T) {
	d := &Daemon{now: time.Now}
	api := &API{D: d}

	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/history", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("history = %d", rec.Code)
	}
	var h struct {
		Runs []RunRecord `json:"runs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &h); err != nil {
		t.Fatalf("decode history: %v", err)
	}

	rec = httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/log", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("log = %d", rec.Code)
	}
	var l struct {
		Lines []LogLine `json:"lines"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &l); err != nil {
		t.Fatalf("decode log: %v", err)
	}
}

// A completed run must appear in history with its totals, because the UI's whole
// "what happened" section reads from there.
func TestRunIsRecordedInHistory(t *testing.T) {
	dir := t.TempDir()
	d, err := New([]string{writeJobFile(t, dir, "a", simpleJob("a", ""))}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d.runJob = func(ctx context.Context, cfg *job.Config, w io.Writer) ([]job.PairResult, error) {
		return []job.PairResult{{
			Pair:      "p",
			Actual:    job.PassResult{Transferred: 4, Deleted: 1},
			Conflicts: []job.Conflict{{Path: "x", Kind: "remote-deleted"}},
		}}, nil
	}

	if err := d.Trigger(context.Background(), "a"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	d.wg.Wait()

	runs := d.history.snapshot()
	if len(runs) != 1 {
		t.Fatalf("history has %d runs, want 1", len(runs))
	}
	r := runs[0]
	if r.Job != "a" || !r.OK {
		t.Errorf("record = %+v", r)
	}
	if r.Transferred != 4 || r.Deleted != 1 {
		t.Errorf("totals = %d/%d, want 4/1", r.Transferred, r.Deleted)
	}
	if r.Conflicts != 1 {
		t.Errorf("conflicts = %d, want 1", r.Conflicts)
	}
	if r.Ended.Before(r.Started) {
		t.Errorf("ended before started: %v -> %v", r.Started, r.Ended)
	}
}

// A failed run keeps its message so the UI can show why.
func TestFailedRunRecordedWithMessage(t *testing.T) {
	dir := t.TempDir()
	d, err := New([]string{writeJobFile(t, dir, "a", simpleJob("a", ""))}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d.runJob = func(ctx context.Context, cfg *job.Config, w io.Writer) ([]job.PairResult, error) {
		return nil, context.DeadlineExceeded
	}
	if err := d.Trigger(context.Background(), "a"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	d.wg.Wait()

	r := d.history.snapshot()[0]
	if r.OK {
		t.Error("a failed run must not be recorded as OK")
	}
	if r.Error == "" {
		t.Error("the error message must be kept")
	}
}

// The log sink must both forward to the original writer and retain lines, which
// is what makes "run it with --log-file" and the UI coexist.
func TestLogSinkForwardsAndRetains(t *testing.T) {
	var fwd strings.Builder
	sink := NewLogSink(&fwd).(*logSink)

	if _, err := sink.Write([]byte("first line\nsecond")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := sink.Write([]byte(" line\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	if got := fwd.String(); got != "first line\nsecond line\n" {
		t.Errorf("forwarded = %q", got)
	}
	lines := sink.Recent()
	if len(lines) != 2 {
		t.Fatalf("retained %d lines, want 2: %+v", len(lines), lines)
	}
	if lines[0].Text != "first line" || lines[1].Text != "second line" {
		t.Errorf("partial lines were not joined correctly: %+v", lines)
	}
}

// The ring must bound memory: an unattended daemon runs for weeks.
func TestRingBounded(t *testing.T) {
	r := newRing[int](3)
	for i := 0; i < 10; i++ {
		r.add(i)
	}
	got := r.snapshot()
	if len(got) != 3 {
		t.Fatalf("kept %d items, want 3", len(got))
	}
	// Oldest first: the last three added are 7, 8, 9.
	if got[0] != 7 || got[1] != 8 || got[2] != 9 {
		t.Errorf("contents = %v, want [7 8 9]", got)
	}
}

var _ = os.Getpid
var _ = filepath.Join
