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
	"strings"
	"testing"

	"weftsync/job"

	// The split needs a real backend to move files on the "remote" side. The local
	// backend keeps that off the network; only main.go imports backends in
	// production, so a test binary has to bring its own.
	_ "github.com/rclone/rclone/backend/local"
)

// splitJob builds a job whose local root holds n files, with a 1400 limit.
func splitJob(t *testing.T, name string, n int) (*Daemon, *API, string) {
	return splitJobWithSync(t, name, n, "\n[pair.sync]\nmax_nodes_per_dir = 1400\n")
}

// splitJobWithSync builds the same job but with a caller-chosen [pair.sync] body.
//
// The sync body is the only way to give this job a limit: its destination is a
// local directory, and a local disk declares no per-folder limit at all.
func splitJobWithSync(t *testing.T, name string, n int, syncBody string) (*Daemon, *API, string) {
	t.Helper()
	dir := t.TempDir()
	local := filepath.Join(dir, "local")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for i := 0; i < n; i++ {
		p := filepath.Join(local, "f"+string(rune('a'+i%26))+".txt")
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	path := filepath.Join(dir, "job.toml")
	// ":local:<path>" rather than backend = "local": the latter would look for a
	// remote named "local" in rclone.conf, which a test process does not have.
	// The on-the-fly form builds the backend from the path, which is exactly what
	// the pair needs here.
	//
	// No pair options either: a bare remote path cannot carry them (the job layer
	// refuses that combination). The undo ledger location is set through
	// API.SplitDir instead, which also keeps this test out of the real user profile.
	//
	// max_nodes_per_dir is explicit because a local destination has no per-folder
	// limit of its own: the wizard asks the backend, a local disk answers "none",
	// and nothing would ever be reported as over a limit. Setting it here exercises
	// the same path a user with a non-Feishu backend would use.
	body := `
[job]
name = "` + name + `"

[[pair]]
name = "p"
[pair.local]
root = "` + filepath.ToSlash(local) + `"
[pair.remote]
root = ":local:` + filepath.ToSlash(filepath.Join(dir, "remote")) + `"
` + syncBody
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write job: %v", err)
	}
	d, err := New([]string{path}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// A temp directory for the ledgers, so a test never writes into the real user
	// profile. Silently doing so is how an earlier version of this file left
	// dozens of ledgers in %APPDATA% and broke its own assertions.
	return d, &API{D: d, SplitDir: t.TempDir()}, local
}

func splitURL(path string, q url.Values) string {
	if len(q) == 0 {
		return path
	}
	return path + "?" + q.Encode()
}

// The plan endpoint must not change anything: it is what a user looks at before
// deciding.
func TestSplitPlanIsReadOnly(t *testing.T) {
	_, api, local := splitJob(t, "s", 5)

	q := url.Values{}
	q.Set("job", "s")
	q.Set("scheme", "count")
	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		splitURL("/api/split/plan", q), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	var view PlanView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if view.Files != 5 {
		t.Errorf("files = %d, want 5", view.Files)
	}
	if view.Confirm != "root" {
		t.Errorf("confirm = %q, want \"root\" for the pair root", view.Confirm)
	}
	if len(view.Samples) == 0 {
		t.Error("the plan should show at least one example mapping")
	}

	// Nothing moved: the original files are still there.
	items, err := os.ReadDir(local)
	if err != nil {
		t.Fatalf("read local: %v", err)
	}
	if len(items) != 5 {
		t.Errorf("local holds %d entries after planning, want the original 5", len(items))
	}
}

// Executing without the confirmation string must be refused: an HTTP client is not
// the page, and a destructive endpoint must not rely on the caller having asked.
func TestSplitExecuteRequiresConfirmation(t *testing.T) {
	_, api, local := splitJob(t, "s", 5)

	for _, confirm := range []string{"", "wrong", "Root"} {
		q := url.Values{}
		q.Set("job", "s")
		q.Set("scheme", "count")
		if confirm != "" {
			q.Set("confirm", confirm)
		}
		rec := httptest.NewRecorder()
		api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
			splitURL("/api/split/execute", q), nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("confirm=%q status = %d, want 400", confirm, rec.Code)
		}
	}

	// Nothing moved.
	items, _ := os.ReadDir(local)
	if len(items) != 5 {
		t.Errorf("local holds %d entries, want the original 5", len(items))
	}
}

// The confirmed split runs, and the directories come out as planned.
func TestSplitExecuteMovesFiles(t *testing.T) {
	_, api, local := splitJob(t, "s", 5)

	q := url.Values{}
	q.Set("job", "s")
	q.Set("scheme", "count")
	q.Set("confirm", "root")
	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		splitURL("/api/split/execute", q), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	var res struct {
		Moved    int      `json:"moved"`
		Verified bool     `json:"verified"`
		Buckets  []string `json:"buckets"`
		SnapPath string   `json:"snapshot_path"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Moved != 5 || !res.Verified {
		t.Errorf("result = %+v, want 5 moves verified", res)
	}
	if res.SnapPath == "" {
		t.Error("the result must name the snapshot, or an undo has nothing to point at")
	}

	// Local: only the buckets remain, and every file is in one of them.
	items, err := os.ReadDir(local)
	if err != nil {
		t.Fatalf("read local: %v", err)
	}
	if len(items) != len(res.Buckets) {
		t.Errorf("local holds %d entries, want %d buckets", len(items), len(res.Buckets))
	}
	total := 0
	for _, it := range items {
		sub, err := os.ReadDir(filepath.Join(local, it.Name()))
		if err != nil {
			t.Fatalf("read bucket: %v", err)
		}
		total += len(sub)
	}
	if total != 5 {
		t.Errorf("%d files across the buckets, want 5", total)
	}
}

// A split of a subdirectory needs the subdirectory's own name to confirm, which is
// the point: the confirmation can only be produced by reading the plan.
func TestSplitConfirmWordForSubdirectory(t *testing.T) {
	d, api, local := splitJob(t, "s", 2)
	sub := filepath.Join(local, "reports")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for i := 0; i < 4; i++ {
		if err := os.WriteFile(filepath.Join(sub, "r"+string(rune('0'+i))+".txt"),
			[]byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	_ = d

	q := url.Values{}
	q.Set("job", "s")
	q.Set("dir", "reports")
	q.Set("scheme", "count")
	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		splitURL("/api/split/plan", q), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var view PlanView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if view.Confirm != "reports" {
		t.Errorf("confirm = %q, want \"reports\"", view.Confirm)
	}

	// The root word must not unlock a subdirectory split.
	q.Set("confirm", "root")
	rec = httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		splitURL("/api/split/execute", q), nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for the wrong confirmation", rec.Code)
	}
}

// A path that escapes the pair must be refused before anything is read.
func TestSplitRejectsEscapingPath(t *testing.T) {
	_, api, _ := splitJob(t, "s", 2)

	for _, dir := range []string{"../etc", "/etc", `C:\Windows`, "a/../../b"} {
		q := url.Values{}
		q.Set("job", "s")
		q.Set("dir", dir)
		rec := httptest.NewRecorder()
		api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
			splitURL("/api/split/plan", q), nil))
		if rec.Code == http.StatusOK {
			t.Errorf("dir=%q was accepted", dir)
		}
	}
}

// An unknown scheme is a client error, not a silent fallback: the user would get a
// different split from the one they asked for.
func TestSplitRejectsUnknownScheme(t *testing.T) {
	_, api, _ := splitJob(t, "s", 2)

	q := url.Values{}
	q.Set("job", "s")
	q.Set("scheme", "alphabetical")
	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		splitURL("/api/split/plan", q), nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "scheme") {
		t.Errorf("the error should name the parameter: %s", rec.Body.String())
	}
}

// Splitting while a run is in progress would move files the run is reading.
func TestSplitRefusedWhileRunning(t *testing.T) {
	d, api, _ := splitJob(t, "s", 2)

	release := make(chan struct{})
	started := make(chan struct{})
	d.runJob = func(ctx context.Context, cfg *job.Config, w io.Writer, opt job.Options) ([]job.PairResult, error) {
		close(started)
		<-release
		return nil, nil
	}
	if err := d.Trigger(context.Background(), "s"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	<-started

	q := url.Values{}
	q.Set("job", "s")
	q.Set("scheme", "count")
	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		splitURL("/api/split/precheck", q), nil))
	if rec.Code != http.StatusConflict {
		t.Errorf("precheck status = %d, want 409", rec.Code)
	}

	close(release)
	waitIdle(t, d, "s")
}

// The precheck must report what it found, and default the target to the biggest
// offender.
func TestSplitPrecheckReportsFindings(t *testing.T) {
	_, api, local := splitJob(t, "s", 2)

	// 1410 files is over the 1400 threshold.
	big := filepath.Join(local, "big")
	if err := os.MkdirAll(big, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for i := 0; i < 1410; i++ {
		name := filepath.Join(big, "f"+itoa(i)+".txt")
		if err := os.WriteFile(name, []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	q := url.Values{}
	q.Set("job", "s")
	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		splitURL("/api/split/precheck", q), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var view SplitView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(view.Findings) != 1 {
		t.Fatalf("findings = %+v, want the one big directory", view.Findings)
	}
	if view.Findings[0].Total != 1410 {
		t.Errorf("total = %d, want 1410", view.Findings[0].Total)
	}
	if view.Dir != "big" {
		t.Errorf("default dir = %q, want the offending one", view.Dir)
	}
	if view.BackendLimit != 0 || view.RefuseAt != 1400 {
		t.Errorf("limits reported = backend %d / refuse %d, want 0/1400",
			view.BackendLimit, view.RefuseAt)
	}
	if view.Backend != "local" {
		t.Errorf("backend = %q, want the on-the-fly scheme \"local\"", view.Backend)
	}
}

// A destination that declares no per-directory limit must not produce findings.
//
// This is the wizard's half of F12. It used to report directories as over a
// "Feishu" limit even when the destination was a local directory.
func TestSplitPrecheckReportsNoLimitForLocalBackend(t *testing.T) {
	_, api, local := splitJobWithSync(t, "s", 2, "")

	big := filepath.Join(local, "big")
	if err := os.MkdirAll(big, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for i := 0; i < 1410; i++ {
		name := filepath.Join(big, "f"+itoa(i)+".txt")
		if err := os.WriteFile(name, []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	q := url.Values{}
	q.Set("job", "s")
	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		splitURL("/api/split/precheck", q), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var view SplitView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(view.Findings) != 0 {
		t.Errorf("findings = %+v, want none: a local disk has no per-folder limit",
			view.Findings)
	}
	if view.RefuseAt != 0 || view.BackendLimit != 0 {
		t.Errorf("limits = %d/%d, want 0/0 (no limit)", view.RefuseAt, view.BackendLimit)
	}
	if view.Dir != "" {
		t.Errorf("dir = %q, want empty when nothing is over the limit", view.Dir)
	}
}

// Undo must only accept a ledger that belongs to the pair. Accepting any path would
// turn this into an endpoint that acts on an arbitrary file.
func TestSplitUndoRejectsForeignSnapshot(t *testing.T) {
	_, api, _ := splitJob(t, "s", 2)

	outside := filepath.Join(t.TempDir(), "split-evil-20260101-000000.json")
	if err := os.WriteFile(outside, []byte(`{"version":1,"moves":[{"from":"a","to":"b"}]}`),
		0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	q := url.Values{}
	q.Set("job", "s")
	q.Set("snapshot", outside)
	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		splitURL("/api/split/undo", q), nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a snapshot outside the pair", rec.Code)
	}
}

// The undocumented-in-full lifecycle: split, list the ledger, undo it.
func TestSplitUndoRoundTrip(t *testing.T) {
	_, api, local := splitJob(t, "s", 4)

	q := url.Values{}
	q.Set("job", "s")
	q.Set("scheme", "count")
	q.Set("confirm", "root")
	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		splitURL("/api/split/execute", q), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("execute status = %d body=%s", rec.Code, rec.Body.String())
	}

	// The ledger is listed.
	rec = httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		splitURL("/api/split/snapshots", q), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshots status = %d body=%s", rec.Code, rec.Body.String())
	}
	var list SnapshotsView
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list.Snapshots) != 1 {
		t.Fatalf("snapshots = %+v, want one", list.Snapshots)
	}
	if !list.Snapshots[0].Completed || list.Snapshots[0].Pending != 0 {
		t.Errorf("ledger = %+v, want completed with nothing pending", list.Snapshots[0])
	}

	// Undo restores the original names.
	uq := url.Values{}
	uq.Set("job", "s")
	uq.Set("snapshot", list.Snapshots[0].Path)
	rec = httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		splitURL("/api/split/undo", uq), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("undo status = %d body=%s", rec.Code, rec.Body.String())
	}

	items, err := os.ReadDir(local)
	if err != nil {
		t.Fatalf("read local: %v", err)
	}
	names := make([]string, 0, len(items))
	for _, it := range items {
		names = append(names, it.Name())
	}
	if len(names) != 4 {
		t.Errorf("local holds %v, want the original four files", names)
	}
	for _, n := range names {
		if !strings.HasSuffix(n, ".txt") {
			t.Errorf("leftover directory %q after undo", n)
		}
	}

	// The listing must report the ledger as undone, so the console stops
	// offering to resume it.
	rec = httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		splitURL("/api/split/snapshots", q), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshots after undo status = %d body=%s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode after undo: %v", err)
	}
	if len(list.Snapshots) != 1 {
		t.Fatalf("snapshots after undo = %+v, want one", list.Snapshots)
	}
	brief := list.Snapshots[0]
	if !brief.Undone || brief.Completed || brief.Pending != 0 {
		t.Errorf("ledger after undo = %+v, want undone, not completed, nothing pending", brief)
	}
}

// Two splits on one job must not run at the same time.
func TestSplitIsSingleFlightPerJob(t *testing.T) {
	d, _, _ := splitJob(t, "s", 2)

	if !d.beginSplit("s") {
		t.Fatal("the first split should be allowed")
	}
	if d.beginSplit("s") {
		t.Error("a second split on the same job must be refused")
	}
	// A different job is independent.
	if !d.beginSplit("other") {
		t.Error("a different job should be allowed")
	}
	d.endSplit("s")
	if !d.beginSplit("s") {
		t.Error("the slot should be free after endSplit")
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var digits []byte
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	return string(digits)
}
