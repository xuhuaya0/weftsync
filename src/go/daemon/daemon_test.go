package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"weftsync/job"
)

func writeJobFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name+".toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write job file: %v", err)
	}
	return path
}

func simpleJob(name string, schedule string) string {
	return `
[job]
name = "` + name + `"
variant = "update"
` + schedule + `

[[pair]]
name = "p"
[pair.local]
root = "` + filepath.ToSlash(os.TempDir()) + `"
[pair.remote]
root = "feishu:docs"
`
}

// A job with no schedule must never fire on its own; "manual" is the safe
// default for something that can delete cloud files.
func TestNoScheduleNeverFires(t *testing.T) {
	dir := t.TempDir()
	d, err := New([]string{writeJobFile(t, dir, "a", simpleJob("a", ""))}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	st := d.Status()
	if len(st) != 1 {
		t.Fatalf("got %d jobs", len(st))
	}
	if st[0].NextRun != nil {
		t.Errorf("next run = %v, want none", st[0].NextRun)
	}
	if st[0].Schedule != "manual" {
		t.Errorf("schedule = %q, want manual", st[0].Schedule)
	}
}

// interval with align_to_start must phase-lock to start_at, not drift from
// whenever the daemon happened to start.
func TestAlignToStartPhasesInterval(t *testing.T) {
	dir := t.TempDir()
	body := simpleJob("a", `
[job.schedule]
mode = "interval"
interval = "2h"
start_at = "08:00"
align_to_start = true
`)
	d, err := New([]string{writeJobFile(t, dir, "a", body)}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sched := d.entries[0].schedule

	// 09:15 -> next slot is 10:00 (08:00 + 2h).
	now := time.Date(2026, 11, 7, 9, 15, 0, 0, time.Local)
	next := sched.NextAfter(now)
	want := time.Date(2026, 11, 7, 10, 0, 0, 0, time.Local)
	if !next.Equal(want) {
		t.Errorf("NextAfter(09:15) = %v, want %v", next, want)
	}

	// Before the first slot of the day -> that slot.
	early := time.Date(2026, 11, 7, 6, 0, 0, 0, time.Local)
	if got := sched.NextAfter(early); !got.Equal(time.Date(2026, 11, 7, 8, 0, 0, 0, time.Local)) {
		t.Errorf("NextAfter(06:00) = %v, want 08:00", got)
	}
}

// Without align_to_start the interval simply counts from now.
func TestIntervalWithoutAlignment(t *testing.T) {
	dir := t.TempDir()
	body := simpleJob("a", `
[job.schedule]
mode = "interval"
interval = "90m"
`)
	d, err := New([]string{writeJobFile(t, dir, "a", body)}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	now := time.Date(2026, 11, 7, 9, 15, 0, 0, time.Local)
	got := d.entries[0].schedule.NextAfter(now)
	if want := now.Add(90 * time.Minute); !got.Equal(want) {
		t.Errorf("NextAfter = %v, want %v", got, want)
	}
}

// daily_time fires once a day, rolling to tomorrow when today's slot has passed.
func TestDailyTimeRollsOver(t *testing.T) {
	dir := t.TempDir()
	body := simpleJob("a", `
[job.schedule]
mode = "daily_time"
daily_time = "02:00"
`)
	d, err := New([]string{writeJobFile(t, dir, "a", body)}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sched := d.entries[0].schedule

	after := time.Date(2026, 11, 7, 15, 0, 0, 0, time.Local)
	if got := sched.NextAfter(after); !got.Equal(time.Date(2026, 11, 8, 2, 0, 0, 0, time.Local)) {
		t.Errorf("NextAfter(15:00) = %v, want tomorrow 02:00", got)
	}
	before := time.Date(2026, 11, 7, 1, 0, 0, 0, time.Local)
	if got := sched.NextAfter(before); !got.Equal(time.Date(2026, 11, 7, 2, 0, 0, 0, time.Local)) {
		t.Errorf("NextAfter(01:00) = %v, want today 02:00", got)
	}
}

func TestCronSchedule(t *testing.T) {
	dir := t.TempDir()
	body := simpleJob("a", `
[job.schedule]
mode = "cron"
cron = "0 2 * * *"
`)
	d, err := New([]string{writeJobFile(t, dir, "a", body)}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	now := time.Date(2026, 11, 7, 15, 0, 0, 0, time.Local)
	got := d.computeNext(d.entries[0].schedule, now)
	if !got.Equal(time.Date(2026, 11, 8, 2, 0, 0, 0, time.Local)) {
		t.Errorf("next = %v, want tomorrow 02:00", got)
	}
}

// Bad schedules must be rejected at load time, not silently ignored.
func TestScheduleValidation(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"interval without duration", `
[job.schedule]
mode = "interval"
`, "needs schedule.interval"},
		{"interval too short", `
[job.schedule]
mode = "interval"
interval = "10s"
`, "minimum is 1m"},
		{"align without start_at", `
[job.schedule]
mode = "interval"
interval = "1h"
align_to_start = true
`, "needs schedule.start_at"},
		{"daily without time", `
[job.schedule]
mode = "daily_time"
`, "needs schedule.daily_time"},
		{"bad daily time", `
[job.schedule]
mode = "daily_time"
daily_time = "25:00"
`, "hour must be 0-23"},
		{"cron without expression", `
[job.schedule]
mode = "cron"
`, "needs schedule.cron"},
		{"bad cron", `
[job.schedule]
mode = "cron"
cron = "not a cron"
`, "schedule.cron"},
		{"unknown mode", `
[job.schedule]
mode = "whenever"
`, "unknown schedule.mode"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			_, err := New([]string{writeJobFile(t, dir, "a", simpleJob("a", c.body))}, io.Discard)
			if err == nil {
				t.Fatalf("expected an error containing %q", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error should mention %q\ngot: %v", c.want, err)
			}
		})
	}
}

// A run in progress must not be started again; the overlap attempt is counted.
func TestNoOverlappingRuns(t *testing.T) {
	dir := t.TempDir()
	d, err := New([]string{writeJobFile(t, dir, "a", simpleJob("a", ""))}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	release := make(chan struct{})
	var starts int32
	d.runJob = func(ctx context.Context, cfg *job.Config, w io.Writer, opt job.Options) ([]job.PairResult, error) {
		atomic.AddInt32(&starts, 1)
		<-release
		return nil, nil
	}

	ctx := context.Background()
	if err := d.Trigger(ctx, "a"); err != nil {
		t.Fatalf("first Trigger: %v", err)
	}

	// Wait until the run has actually begun, otherwise the second Trigger may
	// race ahead of the goroutine and be accepted legitimately.
	deadline := time.Now().Add(5 * time.Second)
	for atomic.LoadInt32(&starts) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if atomic.LoadInt32(&starts) == 0 {
		t.Fatal("the run never started")
	}

	err = d.Trigger(ctx, "a")
	if err == nil {
		t.Fatal("a second run must be refused while the first is in progress")
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Errorf("error should say it is already running\ngot: %v", err)
	}

	close(release)
	d.wg.Wait()

	if got := atomic.LoadInt32(&starts); got != 1 {
		t.Errorf("the job body ran %d times, want 1", got)
	}
}

// Status must reflect running and idle, since the UI is built on it.
func TestStatusReflectsRunningState(t *testing.T) {
	dir := t.TempDir()
	d, err := New([]string{writeJobFile(t, dir, "a", simpleJob("a", ""))}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if st := d.Status()[0]; st.State != StateIdle {
		t.Errorf("initial state = %q, want idle", st.State)
	}

	release := make(chan struct{})
	started := make(chan struct{})
	d.runJob = func(ctx context.Context, cfg *job.Config, w io.Writer, opt job.Options) ([]job.PairResult, error) {
		close(started)
		<-release
		return []job.PairResult{{Pair: "p", Actual: job.PassResult{Transferred: 2}}}, nil
	}

	if err := d.Trigger(context.Background(), "a"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	<-started
	if st := d.Status()[0]; st.State != StateRunning {
		t.Errorf("state while running = %q", st.State)
	}

	close(release)
	d.wg.Wait()

	st := d.Status()[0]
	if st.State != StateIdle {
		t.Errorf("state after run = %q, want idle", st.State)
	}
	if st.LastRun == nil || st.LastOK == nil || !*st.LastOK {
		t.Errorf("last run not recorded: %+v", st)
	}
	if len(st.LastResult) != 1 || st.LastResult[0].Actual.Transferred != 2 {
		t.Errorf("last result = %+v", st.LastResult)
	}
}

// Regression for a real defect: Trigger used the caller's context, so an API
// request that returned immediately cancelled the run it had just started. On the
// live tenant the run died 26ms in with "context canceled" while the API had
// already answered 200.
func TestRunSurvivesItsRequester(t *testing.T) {
	dir := t.TempDir()
	d, err := New([]string{writeJobFile(t, dir, "a", simpleJob("a", ""))}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var cancelled int32
	var completed int32
	release := make(chan struct{})
	started := make(chan struct{})

	d.runJob = func(ctx context.Context, cfg *job.Config, w io.Writer, opt job.Options) ([]job.PairResult, error) {
		close(started)
		go func() {
			// Watch the context the run was given for the whole run duration.
			<-ctx.Done()
			atomic.AddInt32(&cancelled, 1)
		}()
		<-release
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		atomic.AddInt32(&completed, 1)
		return nil, nil
	}

	// A short-lived context, exactly like an HTTP request's.
	reqCtx, cancelReq := context.WithCancel(context.Background())
	if err := d.Trigger(reqCtx, "a"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	<-started
	cancelReq() // the "request" finishes

	// Give the cancellation a chance to propagate if it were going to.
	time.Sleep(50 * time.Millisecond)

	close(release)
	d.wg.Wait()

	if atomic.LoadInt32(&cancelled) != 0 {
		t.Error("the run's context was cancelled when the requester returned")
	}
	if atomic.LoadInt32(&completed) != 1 {
		t.Errorf("the run did not complete (completed=%d)", completed)
	}
	if st := d.Status()[0]; st.LastOK == nil || !*st.LastOK {
		t.Errorf("run should be recorded as successful: %+v", st)
	}
}

// Stop must end the daemon lifetime, which cancels any run still going.
func TestStopCancelsRuns(t *testing.T) {
	dir := t.TempDir()
	d, err := New([]string{writeJobFile(t, dir, "a", simpleJob("a", ""))}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	d.Start(ctx)
	defer cancel()

	started := make(chan struct{})
	d.runJob = func(runCtx context.Context, cfg *job.Config, w io.Writer, opt job.Options) ([]job.PairResult, error) {
		close(started)
		<-runCtx.Done()
		return nil, runCtx.Err()
	}
	if err := d.Trigger(context.Background(), "a"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	<-started

	done := make(chan struct{})
	go func() {
		d.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return; the run was not cancelled")
	}
}

// A failed run must be visible, including the message.
func TestFailedRunIsRecorded(t *testing.T) {
	dir := t.TempDir()
	d, err := New([]string{writeJobFile(t, dir, "a", simpleJob("a", ""))}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d.runJob = func(ctx context.Context, cfg *job.Config, w io.Writer, opt job.Options) ([]job.PairResult, error) {
		return nil, errors.New("boom: nothing was deleted")
	}

	if err := d.Trigger(context.Background(), "a"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	d.wg.Wait()

	st := d.Status()[0]
	if st.LastOK == nil || *st.LastOK {
		t.Errorf("last run should be marked failed: %+v", st)
	}
	if !strings.Contains(st.LastError, "boom") {
		t.Errorf("error not recorded: %q", st.LastError)
	}
}

// A disabled job must not be triggerable.
func TestDisabledJobCannotRun(t *testing.T) {
	dir := t.TempDir()
	body := `
[job]
name = "a"
enabled = false

[[pair]]
name = "p"
[pair.local]
root = "` + filepath.ToSlash(os.TempDir()) + `"
[pair.remote]
root = "feishu:docs"
`
	d, err := New([]string{writeJobFile(t, dir, "a", body)}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if st := d.Status()[0]; st.State != StateDisabled {
		t.Errorf("state = %q, want disabled", st.State)
	}
	if err := d.Trigger(context.Background(), "a"); err == nil {
		t.Fatal("a disabled job must not run")
	}
}

// Two job files with the same name would make the API ambiguous.
func TestDuplicateJobNamesRejected(t *testing.T) {
	dir := t.TempDir()
	a := writeJobFile(t, dir, "a", simpleJob("same", ""))
	b := writeJobFile(t, dir, "b", simpleJob("same", ""))
	if _, err := New([]string{a, b}, io.Discard); err == nil {
		t.Fatal("duplicate job names must be rejected")
	}
}

// The API must expose status and accept a run request.
func TestAPIStatusAndRun(t *testing.T) {
	dir := t.TempDir()
	d, err := New([]string{writeJobFile(t, dir, "a", simpleJob("a", ""))}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var ran int32
	release := make(chan struct{})
	d.runJob = func(ctx context.Context, cfg *job.Config, w io.Writer, opt job.Options) ([]job.PairResult, error) {
		atomic.AddInt32(&ran, 1)
		<-release
		return nil, nil
	}
	api := &API{D: d}

	// Status.
	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d", rec.Code)
	}
	var body struct {
		Jobs []JobStatus `json:"jobs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Jobs) != 1 || body.Jobs[0].Name != "a" {
		t.Errorf("jobs = %+v", body.Jobs)
	}

	// Run.
	rec = httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/run?job=a", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("run status = %d body=%s", rec.Code, rec.Body.String())
	}

	deadline := time.Now().Add(5 * time.Second)
	for atomic.LoadInt32(&ran) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	close(release)
	d.wg.Wait()

	// GET on /api/run must be refused: starting a sync is not a safe operation.
	rec = httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/run", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /api/run = %d, want 405", rec.Code)
	}
}

// The API must refuse to bind anywhere but loopback.
func TestListenRefusesNonLoopback(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:5572", "192.168.1.10:5572", ":5572"} {
		t.Run(addr, func(t *testing.T) {
			_, _, err := Listen(addr, &API{D: &Daemon{}})
			if err == nil {
				t.Fatalf("binding %q must be refused", addr)
			}
			if !strings.Contains(err.Error(), "loopback") {
				t.Errorf("error should explain why\ngot: %v", err)
			}
		})
	}
}

// Binding is the single-instance guard: the second daemon fails with a message
// that names the likely cause.
func TestSecondDaemonCannotBind(t *testing.T) {
	d := &Daemon{now: time.Now}
	srv1, ln1, err := Listen("127.0.0.1:0", &API{D: d})
	if err != nil {
		t.Fatalf("first Listen: %v", err)
	}
	defer func() { _ = srv1.Close() }()

	addr := ln1.Addr().String()
	if _, _, err := Listen(addr, &API{D: d}); err == nil {
		t.Fatal("a second daemon must not be able to bind the same address")
	}
}
