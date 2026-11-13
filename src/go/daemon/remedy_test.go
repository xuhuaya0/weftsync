package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weftsync/job"
	"weftsync/remedy"
)

// ownRemedy is a job-level error that can explain itself, standing in for the
// backend errors that do (package remedy is tested separately).
type ownRemedy struct {
	text string
}

func (e *ownRemedy) Error() string { return e.text }
func (e *ownRemedy) Remedy() remedy.Remedy {
	return remedy.Remedy{
		Kind:     "test-kind",
		Fields:   map[string]string{"path": "a/b"},
		Commands: []remedy.Command{{Label: "inspect", Run: "weftsync backend dedupe \"r:\""}},
	}
}

func newOneJob(t *testing.T) *Daemon {
	t.Helper()
	dir := t.TempDir()
	d, err := New([]string{writeJobFile(t, dir, "a", simpleJob("a", ""))}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return d
}

// A failure that can explain itself must reach the console with its commands
// intact, because the console is where the user is: telling them to read the log
// for a command means they will not run it.
func TestFailedRunCarriesItsRemedy(t *testing.T) {
	d := newOneJob(t)
	d.runJob = func(ctx context.Context, cfg *job.Config, w io.Writer, opt job.Options) ([]job.PairResult, error) {
		// Wrapped and joined, the way a real run reports: one error per pair.
		return nil, errors.Join(
			errors.New("pair q: unrelated"),
			&ownRemedy{text: "replace failed but content is fine"},
		)
	}

	if err := d.Trigger(context.Background(), "a"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	d.wg.Wait()

	st := d.Status()[0]
	if st.LastRemedy == nil {
		t.Fatalf("no remedy on the status; the console can only print English: %q", st.LastError)
	}
	if st.LastRemedy.Kind != "test-kind" {
		t.Errorf("kind = %q", st.LastRemedy.Kind)
	}
	if got := st.LastRemedy.Fields["path"]; got != "a/b" {
		t.Errorf("fields lost: %+v", st.LastRemedy.Fields)
	}
	if len(st.LastRemedy.Commands) != 1 || st.LastRemedy.Commands[0].Run == "" {
		t.Errorf("commands lost: %+v", st.LastRemedy.Commands)
	}
	if !strings.Contains(st.LastError, "content is fine") {
		t.Errorf("the plain text should still be there for the log: %q", st.LastError)
	}
}

// A later success must retire the explanation. A block that stays after the
// problem is gone teaches the user to ignore it.
func TestSuccessfulRunClearsRemedy(t *testing.T) {
	d := newOneJob(t)
	d.runJob = func(ctx context.Context, cfg *job.Config, w io.Writer, opt job.Options) ([]job.PairResult, error) {
		return nil, &ownRemedy{text: "boom"}
	}
	if err := d.Trigger(context.Background(), "a"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	d.wg.Wait()
	if d.Status()[0].LastRemedy == nil {
		t.Fatal("precondition: the failed run should have left a remedy")
	}

	d.runJob = func(ctx context.Context, cfg *job.Config, w io.Writer, opt job.Options) ([]job.PairResult, error) {
		return nil, nil
	}
	if err := d.Trigger(context.Background(), "a"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	d.wg.Wait()

	st := d.Status()[0]
	if st.LastRemedy != nil {
		t.Errorf("remedy outlived a successful run: %+v", st.LastRemedy)
	}
	if st.LastError != "" {
		t.Errorf("error text outlived a successful run: %q", st.LastError)
	}
}

// An ordinary failure carries no remedy, and must not be given a fake one: the
// console falls back to showing the raw text, which is honest.
func TestPlainFailureHasNoRemedy(t *testing.T) {
	d := newOneJob(t)
	d.runJob = func(ctx context.Context, cfg *job.Config, w io.Writer, opt job.Options) ([]job.PairResult, error) {
		return nil, errors.New("some other problem")
	}
	if err := d.Trigger(context.Background(), "a"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	d.wg.Wait()

	st := d.Status()[0]
	if st.LastRemedy != nil {
		t.Errorf("invented a remedy: %+v", st.LastRemedy)
	}
	if !strings.Contains(st.LastError, "some other problem") {
		t.Errorf("error lost: %q", st.LastError)
	}
}

// The console reads /api/status, so what matters is the JSON, not the struct.
func TestStatusJSONCarriesRemedy(t *testing.T) {
	d := newOneJob(t)
	d.runJob = func(ctx context.Context, cfg *job.Config, w io.Writer, opt job.Options) ([]job.PairResult, error) {
		return nil, &ownRemedy{text: "boom"}
	}
	if err := d.Trigger(context.Background(), "a"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	d.wg.Wait()

	api := &API{D: d}
	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))

	// Decode rather than grep: the wire names are the contract, and a test that
	// only matched text would still pass if the nesting moved.
	var v struct {
		Jobs []struct {
			LastRemedy *remedy.Remedy `json:"last_remedy"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode: %v\nbody: %s", err, rec.Body.String())
	}
	if len(v.Jobs) != 1 || v.Jobs[0].LastRemedy == nil {
		t.Fatalf("last_remedy did not survive JSON: %s", rec.Body.String())
	}
	r := v.Jobs[0].LastRemedy
	if r.Kind != "test-kind" {
		t.Errorf("kind = %q", r.Kind)
	}
	if len(r.Commands) != 1 || !strings.HasPrefix(r.Commands[0].Run, "weftsync backend dedupe") {
		t.Errorf("commands = %+v", r.Commands)
	}
	if r.Commands[0].Label != "inspect" {
		t.Errorf("label = %q; the console picks its wording from it", r.Commands[0].Label)
	}
}
