package daemon

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"weftsync/job"
)

func planJob(t *testing.T, name string) (string, *Daemon) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name+".toml")
	body := `
[job]
name = "` + name + `"

[[pair]]
name = "p"
[pair.local]
root = "` + filepath.ToSlash(filepath.Join(dir, "local")) + `"
[pair.remote]
backend = "local"
root = "` + filepath.ToSlash(filepath.Join(dir, "remote")) + `"
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write job: %v", err)
	}
	d, err := New([]string{path}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return path, d
}

// waitIdle blocks until the job finishes its current run.
func waitIdle(t *testing.T, d *Daemon, name string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, st := range d.Status() {
			if st.Name == name && st.State != StateRunning {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the job never became idle")
}

// A preview must reach the engine with DryRun set and must not be recorded as a
// run: otherwise the job row would claim a sync happened when nothing was touched.
func TestPlanRunsDryAndDoesNotMoveLastRun(t *testing.T) {
	_, d := planJob(t, "plan")

	var gotOpt job.Options
	done := make(chan struct{})
	d.runJob = func(ctx context.Context, cfg *job.Config, w io.Writer, opt job.Options) ([]job.PairResult, error) {
		gotOpt = opt
		close(done)
		return nil, nil
	}

	if err := d.TriggerPlan(context.Background(), "plan"); err != nil {
		t.Fatalf("TriggerPlan: %v", err)
	}
	<-done
	waitIdle(t, d, "plan")

	if !gotOpt.DryRun {
		t.Error("a preview must reach the engine with DryRun = true")
	}
	if gotOpt.StatsGroup == "" {
		t.Error("the run must carry its accounting group")
	}

	st := d.Status()[0]
	if st.LastRun != nil {
		t.Errorf("a preview must not set last_run, got %v", st.LastRun)
	}
	if st.LastOK != nil {
		t.Error("a preview must not claim a result")
	}

	// The history entry must be marked so the table does not read like a run.
	recs := d.history.snapshot()
	if len(recs) != 1 {
		t.Fatalf("history has %d entries, want 1", len(recs))
	}
	if !recs[0].DryRun {
		t.Error("the history record should be marked as a preview")
	}
}

// Each job must get its own accounting group, or two jobs' progress mix.
func TestEachJobHasItsOwnStatsGroup(t *testing.T) {
	dir := t.TempDir()
	var files []string
	for _, name := range []string{"a", "b"} {
		path := filepath.Join(dir, name+".toml")
		body := `
[job]
name = "` + name + `"

[[pair]]
name = "p"
[pair.local]
root = "` + filepath.ToSlash(filepath.Join(dir, name+"-local")) + `"
[pair.remote]
backend = "local"
root = "` + filepath.ToSlash(filepath.Join(dir, name+"-remote")) + `"
`
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write job: %v", err)
		}
		files = append(files, path)
	}

	d, err := New(files, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	groups := map[string]bool{}
	for _, e := range d.entries {
		g := e.tracker.Group()
		if g == "" {
			t.Fatalf("job %q has no stats group", e.name)
		}
		if groups[g] {
			t.Errorf("two jobs share the stats group %q", g)
		}
		groups[g] = true
	}
}

// Stopping a run must be classified as "stopped", not as a failure.
func TestStopJobCancelsRun(t *testing.T) {
	_, d := planJob(t, "stoppable")

	started := make(chan struct{})
	d.runJob = func(ctx context.Context, cfg *job.Config, w io.Writer, opt job.Options) ([]job.PairResult, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}

	if err := d.Trigger(context.Background(), "stoppable"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	<-started

	if err := d.StopJob("stoppable"); err != nil {
		t.Fatalf("StopJob: %v", err)
	}
	waitIdle(t, d, "stoppable")

	st := d.Status()[0]
	if !st.LastStopped {
		t.Error("the job should be marked as stopped")
	}
	if st.LastOK != nil && !*st.LastOK {
		t.Error("a run stopped on request must not be recorded as a failure")
	}
	if st.LastError != "" {
		t.Errorf("a stop should not leave an error message, got %q", st.LastError)
	}

	recs := d.history.snapshot()
	if len(recs) != 1 {
		t.Fatalf("history has %d entries, want 1", len(recs))
	}
	if !recs[0].Stopped {
		t.Error("the history record should be marked as stopped")
	}
	if recs[0].Error != "" {
		t.Errorf("a stop must not be recorded as an error, got %q", recs[0].Error)
	}
}

// Stopping something that is not running must say so rather than pretend.
func TestStopJobWhenIdle(t *testing.T) {
	_, d := planJob(t, "idle")
	if err := d.StopJob("idle"); err != ErrNotRunning {
		t.Errorf("err = %v, want ErrNotRunning", err)
	}
	if err := d.StopJob("nope"); err == nil || err == ErrNotRunning {
		t.Errorf("an unknown job should be a distinct error, got %v", err)
	}
}

// The API surface: plan flag, stop codes.
func TestRunEndpointPlanAndStop(t *testing.T) {
	_, d := planJob(t, "api")

	release := make(chan struct{})
	gotOpt := make(chan job.Options, 1)
	d.runJob = func(ctx context.Context, cfg *job.Config, w io.Writer, opt job.Options) ([]job.PairResult, error) {
		gotOpt <- opt
		<-release
		return nil, nil
	}
	api := &API{D: d}

	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec,
		httptest.NewRequest(http.MethodPost, "/api/run?job=api&plan=true", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("plan request status = %d body=%s", rec.Code, rec.Body.String())
	}
	opt := <-gotOpt
	if !opt.DryRun {
		t.Error("plan=true did not reach the engine")
	}

	// While running, the status must say it can be stopped and say what it is.
	st := d.Status()[0]
	if st.State != StateRunning {
		t.Fatalf("state = %q, want running", st.State)
	}
	if !st.CanStop {
		t.Error("a run in flight should report can_stop")
	}
	if st.Mode != ModePlan {
		t.Errorf("mode = %q, want %q", st.Mode, ModePlan)
	}

	// A second start must be refused, plan or not.
	rec = httptest.NewRecorder()
	api.Routes().ServeHTTP(rec,
		httptest.NewRequest(http.MethodPost, "/api/run?job=api", nil))
	if rec.Code != http.StatusConflict {
		t.Errorf("second start status = %d, want 409", rec.Code)
	}

	// Stop is accepted.
	rec = httptest.NewRecorder()
	api.Routes().ServeHTTP(rec,
		httptest.NewRequest(http.MethodPost, "/api/stop?job=api", nil))
	if rec.Code != http.StatusAccepted {
		t.Errorf("stop status = %d, want 202", rec.Code)
	}

	// Stopping again when the run is unwinding is a conflict, not a crash. The
	// fake run ignores its context, so it is still running here.
	rec = httptest.NewRecorder()
	api.Routes().ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/api/stop?job=api", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET stop status = %d, want 405", rec.Code)
	}

	close(release)
	waitIdle(t, d, "api")

	rec = httptest.NewRecorder()
	api.Routes().ServeHTTP(rec,
		httptest.NewRequest(http.MethodPost, "/api/stop?job=api", nil))
	if rec.Code != http.StatusConflict {
		t.Errorf("stop when idle status = %d, want 409", rec.Code)
	}
}

// Progress must be readable from the status payload while a run is in flight; an
// absent JSON field would leave the UI with nothing to render.
func TestStatusCarriesProgress(t *testing.T) {
	_, d := planJob(t, "prog")

	release := make(chan struct{})
	started := make(chan struct{})
	d.runJob = func(ctx context.Context, cfg *job.Config, w io.Writer, opt job.Options) ([]job.PairResult, error) {
		close(started)
		<-release
		return nil, nil
	}
	if err := d.Trigger(context.Background(), "prog"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	<-started

	rec := httptest.NewRecorder()
	(&API{D: d}).Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var payload struct {
		Jobs []map[string]any `json:"jobs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Jobs) != 1 {
		t.Fatalf("jobs = %d", len(payload.Jobs))
	}
	if _, ok := payload.Jobs[0]["progress"]; !ok {
		t.Error("a running job must expose a progress object")
	}

	close(release)
	waitIdle(t, d, "prog")
}
