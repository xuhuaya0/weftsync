package daemon

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"weftsync/job"
)

func resolveURL(jobName, pair, path, action string) string {
	q := url.Values{}
	q.Set("job", jobName)
	q.Set("path", path)
	q.Set("action", action)
	if pair != "" {
		q.Set("pair", pair)
	}
	return "/api/conflict/resolve?" + q.Encode()
}

// Every parameter is required, and a wrong action is a client error rather than a
// silent no-op.
func TestConflictResolveRejectsBadRequests(t *testing.T) {
	_, d := planJob(t, "r")
	api := &API{D: d}

	cases := []struct {
		name string
		url  string
		want int
	}{
		{"no path", "/api/conflict/resolve?job=r&action=push", http.StatusBadRequest},
		{"no action", "/api/conflict/resolve?job=r&path=a.txt", http.StatusBadRequest},
		{"bad action", "/api/conflict/resolve?job=r&path=a.txt&action=delete",
			http.StatusBadRequest},
		{"no job", "/api/conflict/resolve?path=a.txt&action=push", http.StatusBadRequest},
		{"unknown job", "/api/conflict/resolve?job=nope&path=a.txt&action=push",
			http.StatusNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, c.url, nil))
			if rec.Code != c.want {
				t.Errorf("status = %d, want %d\nbody=%s", rec.Code, c.want, rec.Body.String())
			}
		})
	}

	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/api/conflict/resolve", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", rec.Code)
	}
}

// Editing the snapshot underneath a run in progress would apply the change to a
// run that already decided what to do.
func TestConflictResolveRefusedWhileRunning(t *testing.T) {
	_, d := planJob(t, "busy")

	release := make(chan struct{})
	started := make(chan struct{})
	d.runJob = func(ctx context.Context, cfg *job.Config, w io.Writer, opt job.Options) ([]job.PairResult, error) {
		close(started)
		<-release
		return nil, nil
	}
	if err := d.Trigger(context.Background(), "busy"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	<-started

	rec := httptest.NewRecorder()
	(&API{D: d}).Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		resolveURL("busy", "", "a.txt", job.ActionPush), nil))
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}

	close(release)
	waitIdle(t, d, "busy")
}

// A backend that cannot resolve must surface as a clear failure, not as success.
//
// The test job points at the local backend, which has no resolve command - which
// is exactly the situation a user hits with a remote type that does not support
// per-path resolution.
func TestConflictResolveUnsupportedBackendIs422(t *testing.T) {
	_, d := planJob(t, "r")

	rec := httptest.NewRecorder()
	(&API{D: d}).Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		resolveURL("r", "p", "a.txt", job.ActionPush), nil))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422\nbody=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error == "" {
		t.Error("the response needs to say why")
	}
}

// Paths that escape the pair must never reach the filesystem.
func TestConflictResolveRefusesEscapingPath(t *testing.T) {
	_, d := planJob(t, "r")

	for _, p := range []string{
		"../../etc/passwd", "/etc/passwd", "C:/Windows/win.ini",
	} {
		rec := httptest.NewRecorder()
		(&API{D: d}).Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
			resolveURL("r", "p", p, job.ActionPull), nil))
		if rec.Code == http.StatusOK {
			t.Errorf("path %q was accepted", p)
		}
	}
}

// ConfigOf hands out a copy, so a caller acting outside the lock cannot race with
// a reload that swaps the config.
func TestConfigOfReturnsCopy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "j.toml")
	body := `
[job]
name = "j"

[[pair]]
name = "p"
[pair.local]
root = "` + filepath.ToSlash(filepath.Join(dir, "local")) + `"
[pair.remote]
backend = "feishu"
root = "team/docs"
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	d, err := New([]string{path}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	cfg, err := d.ConfigOf("j")
	if err != nil {
		t.Fatalf("ConfigOf: %v", err)
	}
	cfg.Pair[0].Name = "mutated"

	live := d.Status()
	_ = live
	d.mu.Lock()
	actual := d.entries[0].cfg.Pair[0].Name
	d.mu.Unlock()
	if actual != "p" {
		t.Errorf("the live config was mutated through the returned copy: %q", actual)
	}

	if _, err := d.ConfigOf("nope"); err != ErrNoSuchJob {
		t.Errorf("unknown job error = %v, want ErrNoSuchJob", err)
	}
}
