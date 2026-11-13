package daemon

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"weftsync/job"
)

// A request that asks for a preview must never start a write.
//
// Measured on 2026-11-13: a client sent /api/run?job=demo&mode=plan. handleRun
// only looked at `plan=`, so `mode` was ignored, the run started for real, and it
// had uploaded 14 files before it was stopped. The parameter was documented
// nowhere, which is exactly why it must not be ignored: "measure only" silently
// becoming "write to the cloud" is the one mistake this endpoint must not make.
func TestRunModeParameterIsHonoured(t *testing.T) {
	for _, c := range []struct {
		query string
		plan  bool
	}{
		{"", false},
		{"&plan=true", true},
		{"&plan=0", false},
		{"&mode=run", false},
		{"&mode=plan", true},
		{"&mode=preview", true},
		{"&mode=dry-run", true},
		{"&mode=PLAN", true},       // case must not matter
		{"&mode=%20plan%20", true}, // nor surrounding space (percent-encoded)
	} {
		t.Run("query="+c.query, func(t *testing.T) {
			d := newOneJob(t)
			var realRuns, previews int32
			release := make(chan struct{})
			d.runJob = func(ctx context.Context, cfg *job.Config, w io.Writer, opt job.Options) ([]job.PairResult, error) {
				if opt.DryRun {
					atomic.AddInt32(&previews, 1)
				} else {
					atomic.AddInt32(&realRuns, 1)
				}
				close(release)
				return nil, nil
			}
			api := &API{D: d}

			rec := httptest.NewRecorder()
			api.Routes().ServeHTTP(rec,
				httptest.NewRequest(http.MethodPost, "/api/run?job=a"+c.query, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
			}
			<-release
			d.wg.Wait()

			gotRuns, gotPreviews := atomic.LoadInt32(&realRuns), atomic.LoadInt32(&previews)
			if c.plan && gotRuns != 0 {
				t.Errorf("a preview request started %d real run(s)", gotRuns)
			}
			if c.plan && gotPreviews != 1 {
				t.Errorf("previews = %d, want 1", gotPreviews)
			}
			if !c.plan && (gotRuns != 1 || gotPreviews != 0) {
				t.Errorf("runs/previews = %d/%d, want 1/0", gotRuns, gotPreviews)
			}
		})
	}
}

// A value we do not understand must be refused, not defaulted: defaulting to
// "run" is how a typo becomes an upload.
func TestRunRejectsUnknownMode(t *testing.T) {
	for _, query := range []string{
		"&mode=plna",     // typo
		"&mode=preview2", // near miss
		"&mode=1",        // a bare number is not a mode
		"&plan=maybe",    // a boolean that is neither
		"&plan=ture",     // typo
	} {
		t.Run("query="+query, func(t *testing.T) {
			d := newOneJob(t)
			var calls int32
			d.runJob = func(ctx context.Context, cfg *job.Config, w io.Writer, opt job.Options) ([]job.PairResult, error) {
				atomic.AddInt32(&calls, 1)
				return nil, nil
			}
			api := &API{D: d}

			rec := httptest.NewRecorder()
			api.Routes().ServeHTTP(rec,
				httptest.NewRequest(http.MethodPost, "/api/run?job=a"+query, nil))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
			}
			time.Sleep(50 * time.Millisecond)
			if n := atomic.LoadInt32(&calls); n != 0 {
				t.Errorf("a rejected request still started %d run(s)", n)
			}
			// The message has to say nothing happened, or the caller cannot tell
			// whether to worry.
			if body := rec.Body.String(); !strings.Contains(body, "nothing was started") {
				t.Errorf("error does not say nothing was started: %s", body)
			}
		})
	}
}
