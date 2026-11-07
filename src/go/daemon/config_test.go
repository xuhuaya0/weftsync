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

func cfgJob(t *testing.T, dir, name string, extra string) string {
	t.Helper()
	body := `
[job]
name = "` + name + `"
variant = "mirror"

[[pair]]
name = "docs"
[pair.local]
root = "` + filepath.ToSlash(filepath.Join(dir, "local")) + `"
[pair.remote]
backend = "feishu"
root = "team/docs"
` + extra
	path := filepath.Join(dir, name+".toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write job: %v", err)
	}
	return path
}

// GET must return everything the form needs, including which values are defaults.
func TestConfigGetReturnsEditableView(t *testing.T) {
	dir := t.TempDir()
	jobPath := cfgJob(t, dir, "nightly", "")

	d, err := New([]string{jobPath}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	api := &API{D: d}

	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/config?job=nightly", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	var v ConfigView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v.Name != "nightly" || v.File != jobPath {
		t.Errorf("name/file = %q/%q", v.Name, v.File)
	}
	if !v.Enabled {
		t.Error("a job without an explicit enabled flag must be enabled")
	}
	if v.Variant != job.VariantMirror {
		t.Errorf("variant = %q", v.Variant)
	}
	if len(v.Pairs) != 1 || v.Pairs[0].Name != "docs" {
		t.Fatalf("pairs = %+v", v.Pairs)
	}
	if v.Pairs[0].RemoteBackend != "feishu" || v.Pairs[0].RemoteRoot != "team/docs" {
		t.Errorf("pair remote = %+v", v.Pairs[0])
	}
	// max_delete was not written, so the shown value must be the inherited default
	// and flagged as such.
	if v.Sync.MaxDelete != job.DefaultMaxDelete || v.Sync.MaxDeleteSet {
		t.Errorf("max_delete = %d set=%v", v.Sync.MaxDelete, v.Sync.MaxDeleteSet)
	}
	if len(v.Variants) == 0 || len(v.Schedule.Modes) == 0 {
		t.Error("the UI needs the allowed values to render selects")
	}
}

// A save must apply to the very next run, not just to the file.
func TestConfigSaveReloadsJob(t *testing.T) {
	dir := t.TempDir()
	jobPath := cfgJob(t, dir, "nightly", "")

	d, err := New([]string{jobPath}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	api := &API{D: d}

	// Read, change the variant, write back.
	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/config?job=nightly", nil))
	var v ConfigView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	v.Variant = job.VariantUpdate

	body, _ := json.Marshal(v)
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/config?job=nightly", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	api.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("save failed: %d %s", rec.Code, rec.Body.String())
	}

	// The in-memory entry must reflect it immediately.
	d.mu.Lock()
	inMemory := d.entries[0].cfg.Job.Variant
	d.mu.Unlock()
	if inMemory != job.VariantUpdate {
		t.Errorf("in-memory variant = %q, want %q (save did not reload)", inMemory, job.VariantUpdate)
	}

	// And so must a fresh read of the file.
	reloaded, err := job.Load(jobPath)
	if err != nil {
		t.Fatalf("reload from disk: %v", err)
	}
	if reloaded.Job.Variant != job.VariantUpdate {
		t.Errorf("on-disk variant = %q", reloaded.Job.Variant)
	}
}

// Editing while a run is in progress would change the settings underneath it.
func TestConfigSaveRefusedWhileRunning(t *testing.T) {
	dir := t.TempDir()
	jobPath := cfgJob(t, dir, "busy", "")

	d, err := New([]string{jobPath}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	release := make(chan struct{})
	started := make(chan struct{})
	d.runJob = func(ctx context.Context, cfg *job.Config, w io.Writer) ([]job.PairResult, error) {
		close(started)
		<-release
		return nil, nil
	}
	if err := d.Trigger(context.Background(), "busy"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	<-started

	api := &API{D: d}
	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/config?job=busy", nil))
	var v ConfigView
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	v.Variant = job.VariantUpdate
	body, _ := json.Marshal(v)

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/config?job=busy", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	api.Routes().ServeHTTP(rec, req)

	if rec.Code == http.StatusOK {
		t.Error("saving while a run is in progress must be refused")
	}
	if !strings.Contains(rec.Body.String(), "running") {
		t.Errorf("the error should explain why\ngot: %s", rec.Body.String())
	}

	close(release)
	d.wg.Wait()
}

// An invalid submission must not reach the disk.
func TestConfigSaveRejectsInvalid(t *testing.T) {
	dir := t.TempDir()
	jobPath := cfgJob(t, dir, "nightly", "")
	before, _ := os.ReadFile(jobPath)

	d, err := New([]string{jobPath}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	api := &API{D: d}

	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/config?job=nightly", nil))
	var v ConfigView
	_ = json.Unmarshal(rec.Body.Bytes(), &v)

	// A mistyped variant is the classic way to break a working job.
	v.Variant = "mirorr"
	body, _ := json.Marshal(v)

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/config?job=nightly", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	api.Routes().ServeHTTP(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatal("an invalid config must be refused")
	}
	after, _ := os.ReadFile(jobPath)
	if string(after) != string(before) {
		t.Error("the file changed despite the rejection")
	}
}

// Unknown job names must be a clean 404, not a panic or a blank 200.
func TestConfigUnknownJob(t *testing.T) {
	dir := t.TempDir()
	d, err := New([]string{cfgJob(t, dir, "a", "")}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	api := &API{D: d}

	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/config?job=nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}

	// A missing query parameter is a client error, not "the only job".
	rec = httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// Turning a job off must be expressible and must survive the round trip.
func TestConfigSaveDisableAndEnable(t *testing.T) {
	dir := t.TempDir()
	jobPath := cfgJob(t, dir, "j", "")

	d, err := New([]string{jobPath}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	save := func(mutate func(*ConfigView)) {
		t.Helper()
		raw, _ := os.ReadFile(jobPath)
		cfg, err := job.Load(jobPath)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		v := ViewConfig(&entry{cfg: cfg, name: "j", file: jobPath}, raw)
		mutate(&v)
		if _, err := d.ApplyConfigView("j", v); err != nil {
			t.Fatalf("ApplyConfigView: %v", err)
		}
	}

	save(func(v *ConfigView) { v.Enabled = false })
	if st := d.Status()[0]; st.Enabled {
		t.Error("job should be disabled after saving enabled=false")
	}

	save(func(v *ConfigView) { v.Enabled = true })
	if st := d.Status()[0]; !st.Enabled {
		t.Error("job should be enabled again")
	}
}

// Changing the schedule must take effect without a restart.
func TestConfigSaveUpdatesSchedule(t *testing.T) {
	dir := t.TempDir()
	jobPath := cfgJob(t, dir, "j", "")

	d, err := New([]string{jobPath}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	raw, _ := os.ReadFile(jobPath)
	cfg, _ := job.Load(jobPath)
	v := ViewConfig(&entry{cfg: cfg, name: "j", file: jobPath}, raw)

	v.Schedule = ScheduleView{
		Mode: job.ScheduleInterval, Interval: "2h",
	}

	if _, err := d.ApplyConfigView("j", v); err != nil {
		t.Fatalf("ApplyConfigView: %v", err)
	}

	d.mu.Lock()
	sched := d.entries[0].schedule
	next := d.entries[0].nextRun
	d.mu.Unlock()

	if sched.Mode != job.ScheduleInterval || sched.Interval != 2*time.Hour {
		t.Errorf("schedule = %+v", sched)
	}
	if next.IsZero() {
		t.Error("next run should be computed after a schedule change")
	}
}

// The defaults section must show the job defaults, not one pair's override.
//
// EffectiveSync merges pair → job → built-in default, so resolving the job row
// through pair[0] displayed that pair's override as the default and made a save
// compare against the wrong value.
func TestConfigViewShowsJobDefaultsNotPairOverrides(t *testing.T) {
	dir := t.TempDir()
	jobPath := cfgJob(t, dir, "j", `
[pair.sync]
deletion = "versioning"
versioning_dir = "_paironly"
conflict = "keep-both"
`)

	if _, err := New([]string{jobPath}, io.Discard); err != nil {
		t.Fatalf("New: %v", err)
	}
	raw, _ := os.ReadFile(jobPath)
	cfg, _ := job.Load(jobPath)
	v := ViewConfig(&entry{cfg: cfg, name: "j", file: jobPath}, raw)

	if v.Sync.Deletion != job.DefaultDeletion {
		t.Errorf("job deletion = %q, want the built-in default %q",
			v.Sync.Deletion, job.DefaultDeletion)
	}
	if v.Sync.VersioningDir != job.DefaultVersioningDir {
		t.Errorf("job versioning_dir = %q, want %q", v.Sync.VersioningDir, job.DefaultVersioningDir)
	}
	if v.Sync.Conflict != job.DefaultConflict {
		t.Errorf("job conflict = %q, want %q", v.Sync.Conflict, job.DefaultConflict)
	}
	// The pair row must still show its own effective values.
	if v.Pairs[0].Sync.Deletion != job.DeletionVersioning ||
		v.Pairs[0].Sync.VersioningDir != "_paironly" ||
		v.Pairs[0].Sync.Conflict != job.ConflictKeepBoth {
		t.Errorf("pair sync = %+v", v.Pairs[0].Sync)
	}
}

// Adding a pair through the form must reach the file.
func TestConfigSaveAddsPair(t *testing.T) {
	dir := t.TempDir()
	jobPath := cfgJob(t, dir, "j", "")

	d, err := New([]string{jobPath}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	raw, _ := os.ReadFile(jobPath)
	cfg, _ := job.Load(jobPath)
	v := ViewConfig(&entry{cfg: cfg, name: "j", file: jobPath}, raw)

	v.Pairs = append(v.Pairs, PairView{
		Name:          "photos",
		LocalRoot:     filepath.ToSlash(filepath.Join(dir, "photos")),
		RemoteBackend: "feishu",
		RemoteRoot:    "backup/photos",
	})

	if _, err := d.ApplyConfigView("j", v); err != nil {
		t.Fatalf("ApplyConfigView: %v", err)
	}

	reloaded, err := job.Load(jobPath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(reloaded.Pair) != 2 {
		t.Fatalf("pairs = %d, want 2", len(reloaded.Pair))
	}
	if reloaded.Pair[1].Name != "photos" {
		t.Errorf("second pair = %+v", reloaded.Pair[1])
	}
}

// Every control the form offers must reach the file.
//
// This is the test that was missing: saving used to keep only conflict and
// max_delete at job level and nothing at all per pair, so choosing "versioning"
// in the UI looked saved and then deleted permanently on the next run.
func TestConfigSaveKeepsEverySyncField(t *testing.T) {
	dir := t.TempDir()
	jobPath := cfgJob(t, dir, "j", "")

	d, err := New([]string{jobPath}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	raw, _ := os.ReadFile(jobPath)
	cfg, _ := job.Load(jobPath)
	v := ViewConfig(&entry{cfg: cfg, name: "j", file: jobPath}, raw)

	v.Sync = SyncView{
		Deletion:         job.DeletionVersioning,
		VersioningDir:    "_keep",
		VersioningNaming: job.NamingTimestampFile,
		Conflict:         job.ConflictOverwrite,
		MaxDelete:        7,
		MaxDeleteSet:     true,
	}
	v.Pairs[0].Sync = SyncView{
		Deletion:         job.DeletionVersioning,
		VersioningDir:    "_pairkeep",
		VersioningNaming: job.NamingReplace,
		Conflict:         job.ConflictKeepBoth,
		MaxDelete:        3,
		MaxDeleteSet:     true,
	}

	if _, err := d.ApplyConfigView("j", v); err != nil {
		t.Fatalf("ApplyConfigView: %v", err)
	}

	reloaded, err := job.Load(jobPath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Job.Sync.Deletion != job.DeletionVersioning ||
		reloaded.Job.Sync.VersioningDir != "_keep" ||
		reloaded.Job.Sync.VersioningNaming != job.NamingTimestampFile ||
		reloaded.Job.Sync.Conflict != job.ConflictOverwrite ||
		reloaded.Job.Sync.MaxDelete == nil || *reloaded.Job.Sync.MaxDelete != 7 {
		t.Errorf("job sync = %+v", reloaded.Job.Sync)
	}
	p := reloaded.Pair[0]
	if p.Sync.Deletion != job.DeletionVersioning ||
		p.Sync.VersioningDir != "_pairkeep" ||
		p.Sync.VersioningNaming != job.NamingReplace ||
		p.Sync.Conflict != job.ConflictKeepBoth ||
		p.Sync.MaxDelete == nil || *p.Sync.MaxDelete != 3 {
		t.Errorf("pair sync = %+v", p.Sync)
	}
}

// A field the form does not render must survive a save.
//
// max_delete_size is settable by hand but has no control in the UI; rebuilding
// the config from the submission alone silently dropped it.
func TestConfigSaveKeepsFieldsTheFormCannotShow(t *testing.T) {
	dir := t.TempDir()
	jobPath := cfgJob(t, dir, "j", `
[job.sync]
max_delete_size = "2G"
`)

	d, err := New([]string{jobPath}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	raw, _ := os.ReadFile(jobPath)
	cfg, _ := job.Load(jobPath)
	v := ViewConfig(&entry{cfg: cfg, name: "j", file: jobPath}, raw)
	v.Variant = job.VariantUpdate

	if _, err := d.ApplyConfigView("j", v); err != nil {
		t.Fatalf("ApplyConfigView: %v", err)
	}

	reloaded, err := job.Load(jobPath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Job.Sync.MaxDeleteSize != "2G" {
		t.Errorf("max_delete_size = %q, want it preserved", reloaded.Job.Sync.MaxDeleteSize)
	}
}

// Saving the form back without touching anything must not materialise defaults.
//
// The form shows effective values, so a naive write-back turns "inherited" into
// "explicitly set to today's default" and the pair silently stops following the
// job default.
func TestConfigSaveWithoutChangesKeepsInheritance(t *testing.T) {
	dir := t.TempDir()
	jobPath := cfgJob(t, dir, "j", "")

	d, err := New([]string{jobPath}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	raw, _ := os.ReadFile(jobPath)
	cfg, _ := job.Load(jobPath)
	v := ViewConfig(&entry{cfg: cfg, name: "j", file: jobPath}, raw)

	// Nothing changed: submit exactly what was shown.
	if _, err := d.ApplyConfigView("j", v); err != nil {
		t.Fatalf("ApplyConfigView: %v", err)
	}

	reloaded, err := job.Load(jobPath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Job.Sync.MaxDelete != nil {
		t.Errorf("max_delete became explicit (%d); inheritance was lost",
			*reloaded.Job.Sync.MaxDelete)
	}
	if reloaded.Job.Sync.Deletion != "" || reloaded.Job.Sync.Conflict != "" {
		t.Errorf("job sync gained explicit defaults: %+v", reloaded.Job.Sync)
	}
	if reloaded.Pair[0].Sync.Deletion != "" || reloaded.Pair[0].Sync.MaxDelete != nil {
		t.Errorf("pair sync gained explicit defaults: %+v", reloaded.Pair[0].Sync)
	}
}
