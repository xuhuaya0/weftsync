package daemon

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"weftsync/job"
)

// staleFlag reads plan.stale straight out of /api/status.
//
// Read through the JSON rather than the struct: the console only ever sees the
// payload, and a field that exists in Go but not in the payload is exactly the
// bug this guards against.
func staleFlag(t *testing.T, d *Daemon) bool {
	t.Helper()
	rec := httptest.NewRecorder()
	(&API{D: d}).Routes().ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Jobs []struct {
			Plan *struct {
				Stale bool `json:"stale"`
			} `json:"plan"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Jobs) != 1 || payload.Jobs[0].Plan == nil {
		t.Fatalf("no plan in the status payload: %s", rec.Body.String())
	}
	return payload.Jobs[0].Plan.Stale
}

// A plan says "what a run would do now". After a run has moved both sides that
// sentence is no longer true, and the console must be able to tell.
//
// Measured on a real pair (2026-11-17): the user synced, and the result card kept
// showing the table measured before the run, so the only difference between "just
// compared" and "compared fifty files ago" was nothing at all. Their question was
// 「点完同步之后不应该自动显示新的比较结果吗？…怎么还是显示的跟之前一样？」
func TestRunMarksThePreviousPlanStale(t *testing.T) {
	_, d := planJob(t, "stale")

	var sawPreview bool
	d.runJob = func(_ context.Context, _ *job.Config, _ io.Writer, opt job.Options) ([]job.PairResult, error) {
		sawPreview = opt.DryRun
		return nil, nil
	}

	if err := d.TriggerPlan(context.Background(), "stale"); err != nil {
		t.Fatalf("TriggerPlan: %v", err)
	}
	waitIdle(t, d, "stale")
	if !sawPreview {
		t.Fatal("the first call must have been a preview")
	}
	if staleFlag(t, d) {
		t.Fatal("a freshly measured plan must not be marked stale")
	}

	if err := d.Trigger(context.Background(), "stale"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	waitIdle(t, d, "stale")
	if !staleFlag(t, d) {
		t.Error("a run must mark the preview it invalidated")
	}

	// Measuring again must clear it: a new comparison is current by definition,
	// and a mark that never clears would leave 按此执行 disabled forever.
	if err := d.TriggerPlan(context.Background(), "stale"); err != nil {
		t.Fatalf("TriggerPlan: %v", err)
	}
	waitIdle(t, d, "stale")
	if staleFlag(t, d) {
		t.Error("a new preview must not inherit the stale mark")
	}
}

// A preview on its own must never mark anything: measuring changes nothing, so
// running two comparisons in a row has to leave the second one current.
func TestPreviewDoesNotStaleItself(t *testing.T) {
	_, d := planJob(t, "twice")
	d.runJob = func(_ context.Context, _ *job.Config, _ io.Writer, _ job.Options) ([]job.PairResult, error) {
		return nil, nil
	}

	for i := 0; i < 2; i++ {
		if err := d.TriggerPlan(context.Background(), "twice"); err != nil {
			t.Fatalf("TriggerPlan %d: %v", i, err)
		}
		waitIdle(t, d, "twice")
		if staleFlag(t, d) {
			t.Fatalf("preview %d marked its own result stale", i)
		}
	}
}
