package daemon

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"weftsync/job"
)

// twoWayJob writes a job file whose pair is variant = "two-way" with the given
// extra [pair.sync] body.
func twoWayJob(t *testing.T, dir, extra string) string {
	t.Helper()
	body := `
[job]
name = "tw"

[[pair]]
name = "tw"
variant = "two-way"
[pair.local]
root = "` + filepath.ToSlash(filepath.Join(dir, "local")) + `"
[pair.remote]
backend = "feishu"
root = "team/docs"
` + extra
	path := filepath.Join(dir, "tw.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write job: %v", err)
	}
	return path
}

// The job-level variant select must be able to represent two-way.
//
// Without it the form falls back to the first option ("update"), submits that,
// and the pair then resolves to a one-way variant while its switches are still
// on - the save fails with a 400 about switches the user never touched.
// Measured before the fix.
func TestVariantListOffersTwoWay(t *testing.T) {
	dir := t.TempDir()
	path := twoWayJob(t, dir, "")
	d, err := New([]string{path}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	view := getConfig(t, &API{D: d}, "tw")

	var hasTwoWay, hasCustom bool
	for _, v := range view.Variants {
		switch v {
		case job.VariantTwoWay:
			hasTwoWay = true
		case job.VariantCustom:
			hasCustom = true
		}
	}
	if !hasTwoWay {
		t.Errorf("variants = %v, want two-way among them; the select cannot "+
			"represent the job otherwise", view.Variants)
	}
	if hasCustom {
		t.Error("custom is offered but not implemented; picking it produces a file " +
			"the loader rejects")
	}

	// Saving without changing anything must round-trip: this is what failed.
	putConfig(t, &API{D: d}, "tw", view)
	back, err := job.Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if v, _ := back.Pair[0].EffectiveVariant(back); v != job.VariantTwoWay {
		t.Errorf("variant after a no-op save = %q, want two-way", v)
	}
}

// getConfig and putConfig exercise the real handler, which is what the form uses.
func getConfig(t *testing.T, api *API, name string) ConfigView {
	t.Helper()
	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/config?job="+name, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/config: %d %s", rec.Code, rec.Body.String())
	}
	var v ConfigView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return v
}

func putConfig(t *testing.T, api *API, name string, v ConfigView) {
	t.Helper()
	body, _ := json.Marshal(v)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/config?job="+name, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	api.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/config: %d %s", rec.Code, rec.Body.String())
	}
}

// The three propagation switches must be visible in the configuration view,
// otherwise the only way to change them is hand-editing TOML - which is exactly
// the thing the console exists to avoid.
func TestTwoWaySwitchesAreInTheConfigView(t *testing.T) {
	dir := t.TempDir()
	path := twoWayJob(t, dir, `
[pair.sync]
propagate_local_delete = true
pull_new_files = true
max_pull_size = "250M"
`)
	d, err := New([]string{path}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got := getConfig(t, &API{D: d}, "tw").Pairs[0].Sync

	if !got.PropagateLocalDelete {
		t.Error("propagate_local_delete is not reported")
	}
	if got.PropagateRemoteDelete {
		t.Error("propagate_remote_delete should be false")
	}
	if !got.PullNewFiles {
		t.Error("pull_new_files is not reported")
	}
	if got.MaxPullSize != "250M" {
		t.Errorf("max_pull_size = %q, want the text the user wrote", got.MaxPullSize)
	}
}

// Toggling one switch must not disturb the others.
func TestTwoWaySwitchesSaveIndependently(t *testing.T) {
	dir := t.TempDir()
	path := twoWayJob(t, dir, `
[pair.sync]
propagate_local_delete = true
`)
	d, err := New([]string{path}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	api := &API{D: d}

	view := getConfig(t, api, "tw")
	view.Pairs[0].Sync.PropagateRemoteDelete = true
	view.Pairs[0].Sync.MaxPullSize = "10M"
	putConfig(t, api, "tw", view)

	reloaded, err := job.Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	sync := reloaded.Pair[0].Sync
	if !sync.PropagateLocalDelete {
		t.Error("the untouched switch was lost")
	}
	if !sync.PropagateRemoteDelete {
		t.Error("the toggled switch was not saved")
	}
	if sync.PullNewFiles {
		t.Error("a switch nobody touched turned on")
	}
	if sync.MaxPullSize != "10M" {
		t.Errorf("max_pull_size = %q", sync.MaxPullSize)
	}
}

// Moving a pair off two-way must not leave the switches behind: the job file
// would then fail to load with "only apply to two-way", which looks like the
// console broke the file.
func TestSwitchingVariantAwayClearsTheSwitches(t *testing.T) {
	dir := t.TempDir()
	path := twoWayJob(t, dir, `
[pair.sync]
propagate_remote_delete = true
pull_new_files = true
`)
	d, err := New([]string{path}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	api := &API{D: d}

	view := getConfig(t, api, "tw")
	// The form renders no switches for a non-two-way pair, so it submits false
	// for all three - which is what the UI's collectSync does.
	view.Variant = "update"
	view.Pairs[0].Variant = ""
	view.Pairs[0].Sync.PropagateLocalDelete = false
	view.Pairs[0].Sync.PropagateRemoteDelete = false
	view.Pairs[0].Sync.PullNewFiles = false
	putConfig(t, api, "tw", view)

	reloaded, err := job.Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Pair[0].Sync.PropagateRemoteDelete || reloaded.Pair[0].Sync.PullNewFiles {
		t.Error("the switches survived the variant change")
	}
}
