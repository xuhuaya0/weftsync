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
)

// oneConfig writes a valid job file named jobName in dir.
func oneConfig(t *testing.T, dir, jobName string) string {
	t.Helper()
	body := `
[job]
name = "` + jobName + `"
variant = "update"

[[pair]]
name = "p1"
[pair.local]
root = "` + filepath.ToSlash(filepath.Join(dir, "local")) + `"
[pair.remote]
root = ":local:` + filepath.ToSlash(filepath.Join(dir, "remote")) + `"
`
	path := filepath.Join(dir, jobName+".toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// A config file has to be openable from the console, not only at daemon start.
// Before this, the set of configurations was fixed by the command line, so
// "open that other one" had no answer.
func TestAddConfigMakesItSelectable(t *testing.T) {
	dir := t.TempDir()
	first := oneConfig(t, dir, "one")
	second := oneConfig(t, dir, "two")

	d, err := New([]string{first}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	api := &API{D: d}
	if _, err := d.AddConfig(second); err != nil {
		t.Fatalf("AddConfig: %v", err)
	}

	// It must now answer on the same endpoint the console uses for every config.
	v := getConfig(t, api, "two")
	if v.Name != "two" {
		t.Errorf("name = %q, want two", v.Name)
	}
	if len(v.Pairs) != 1 {
		t.Errorf("pairs = %d, want 1", len(v.Pairs))
	}

	// And it must show up in the status list, or the console cannot offer it.
	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if !strings.Contains(rec.Body.String(), `"two"`) {
		t.Errorf("the added config is missing from /api/status: %s", rec.Body.String())
	}
}

// Two files claiming the same config name would make the console's addressing
// ambiguous, so the second one is refused with a message that names the file.
func TestAddConfigRejectsDuplicateName(t *testing.T) {
	dir := t.TempDir()
	first := oneConfig(t, dir, "same")
	other := filepath.Join(dir, "sub")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	second := oneConfig(t, other, "same")

	d, err := New([]string{first}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = d.AddConfig(second)
	if err == nil {
		t.Fatal("a duplicate name was accepted")
	}
	if !strings.Contains(err.Error(), "same") {
		t.Errorf("error %q does not name the config", err)
	}
}

// Adding the same file twice is not an error - pressing Open on the config that
// is already loaded must be harmless, not a failure dialog.
func TestAddConfigIsIdempotentForTheSameFile(t *testing.T) {
	dir := t.TempDir()
	first := oneConfig(t, dir, "one")
	d, err := New([]string{first}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := d.AddConfig(first); err != nil {
		t.Fatalf("re-adding the same file must succeed, got: %v", err)
	}
	if n := len(d.Status()); n != 1 {
		t.Errorf("configs = %d, want 1 (the same file was added twice)", n)
	}
}

// The picker has to show the configs in a directory, including the broken ones:
// a file that will not parse is exactly what the user needs to see, and hiding it
// makes the directory look empty.
func TestListConfigFilesReportsBrokenFiles(t *testing.T) {
	dir := t.TempDir()
	oneConfig(t, dir, "good")
	if err := os.WriteFile(filepath.Join(dir, "broken.toml"), []byte("this is not toml [[["), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Not a config: must be ignored entirely.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	rec := httptest.NewRecorder()
	api := &API{D: mustDaemon(t, dir)}
	api.Routes().ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/api/config/files?dir="+dir, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Files []ConfigFile `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Files) == 0 {
		t.Fatal("the listing is empty")
	}
	byFile := map[string]ConfigFile{}
	for _, f := range got.Files {
		byFile[f.File] = f
	}
	if _, ok := byFile["base.toml"]; !ok {
		t.Fatalf("the daemon's own config is missing from the listing: %+v", got.Files)
	}
	if _, ok := byFile["notes.txt"]; ok {
		t.Error("a non-config file was listed; the picker only wants .toml")
	}
	if byFile["good.toml"].Name != "good" {
		t.Errorf("good.toml name = %q", byFile["good.toml"].Name)
	}
	if byFile["good.toml"].Error != "" {
		t.Errorf("good.toml reported an error: %s", byFile["good.toml"].Error)
	}
	if byFile["broken.toml"].Error == "" {
		t.Error("broken.toml was listed without an error; the picker would offer a file that cannot open")
	}
}

// mustDaemon builds a daemon whose single config lives in dir.
func mustDaemon(t *testing.T, dir string) *Daemon {
	t.Helper()
	path := oneConfig(t, dir, "base")
	d, err := New([]string{path}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return d
}

// Save As is the operation the console was missing entirely: keep the current
// settings under a new name, leaving the original alone.
func TestSaveAsCreatesANewConfig(t *testing.T) {
	dir := t.TempDir()
	base := oneConfig(t, dir, "base")
	d, err := New([]string{base}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	api := &API{D: d}

	view := getConfig(t, api, "base")
	view.Name = "copy"
	body, _ := json.Marshal(view)
	target := filepath.Join(dir, "copy.toml")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost,
		"/api/config/save-as?path="+target, strings.NewReader(string(body)))
	api.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("save-as: %d %s", rec.Code, rec.Body.String())
	}

	// The new file exists, carries the name of its file, and the original is
	// untouched.
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("the saved file is missing: %v", err)
	}
	saved := getConfig(t, api, "copy")
	if saved.Name != "copy" {
		t.Errorf("saved name = %q, want copy", saved.Name)
	}
	if got := getConfig(t, api, "base").Name; got != "base" {
		t.Errorf("the original config was renamed to %q", got)
	}
}

// Overwriting silently is the one thing Save As must not do: the user picked a
// name believing it was free.
func TestSaveAsRefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()
	base := oneConfig(t, dir, "base")
	d, err := New([]string{base}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	api := &API{D: d}
	body, _ := json.Marshal(getConfig(t, api, "base"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost,
		"/api/config/save-as?path="+oneConfig(t, dir, "taken"), strings.NewReader(string(body)))
	api.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusConflict, rec.Body.String())
	}
}

// A path with no extension would be invisible to the picker, which lists .toml.
func TestSaveAsAddsTheTomlExtension(t *testing.T) {
	dir := t.TempDir()
	base := oneConfig(t, dir, "base")
	d, err := New([]string{base}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	api := &API{D: d}
	body, _ := json.Marshal(getConfig(t, api, "base"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost,
		"/api/config/save-as?path="+filepath.Join(dir, "noext"), strings.NewReader(string(body)))
	api.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("save-as: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "noext.toml")); err != nil {
		t.Fatalf("expected noext.toml: %v", err)
	}
}
