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
	// Valid TOML that is somebody else's file. It used to be listed as a
	// disabled row whose label was a decoder error cut at 40 characters - the
	// path at the front, with the reason past the edge:
	//
	//   pyproject.toml  (打不开: parse C:\Users\...\my_fc)
	//
	// The user's report: 「为什么打不开啊，而且写这么奇怪的标注」.
	if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"),
		[]byte("[project]\nname = \"other\"\nrequires-python = \">=3.11\"\n"), 0o644); err != nil {
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
		Other int          `json:"other"`
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
	if _, ok := byFile["pyproject.toml"]; ok {
		t.Error("pyproject.toml was listed as a broken config; it is not ours to open")
	}
	if got.Other != 1 {
		t.Errorf("other = %d, want 1 (the dialog says how many .toml files it skipped)",
			got.Other)
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

// The reason has to fit on one row and be about the file, not about the decoder.
// A config that really is ours but has a typo still has to say so.
func TestListConfigFilesErrorIsShortAndPathFree(t *testing.T) {
	dir := t.TempDir()
	oneConfig(t, dir, "base")
	path := filepath.Join(dir, "typo.toml")
	// A valid config, plus a key weftsync does not have.
	src, err := os.ReadFile(filepath.Join(dir, "base.toml"))
	if err != nil {
		t.Fatalf("read base: %v", err)
	}
	if err := os.WriteFile(path, append(src, []byte("\n[typo]\nx = 1\n")...), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	files, _, err := listConfigFiles(dir, dir)
	if err != nil {
		t.Fatalf("listConfigFiles: %v", err)
	}
	var found *ConfigFile
	for i := range files {
		if files[i].File == "typo.toml" {
			found = &files[i]
		}
	}
	if found == nil {
		t.Fatalf("typo.toml missing from the listing: %+v", files)
	}
	if found.Error == "" {
		t.Fatal("typo.toml loaded; the strict decoder should have refused it")
	}
	if strings.Contains(found.Error, path) {
		t.Errorf("the error repeats the path the row already shows: %q", found.Error)
	}
	if strings.Contains(found.Error, "strict mode") {
		t.Errorf("the error is the decoder's own wording: %q", found.Error)
	}
	if n := len([]rune(found.Error)); n > 60 {
		t.Errorf("the error is %d characters, too long for one row: %q", n, found.Error)
	}
}

// A config that could not be saved must come back as the reason only.
//
// job.Save reports validation failures as
//
//	the edited config is not valid: <absolute path>: <reason>
//
// which, shown verbatim in the save-as dialog, is what the user asked to have
// removed (「能不能把这类奇怪的东西删掉」, with a screenshot of exactly that string).
// The path is the client's own and already on screen; the wrapper repeats the
// heading. Both go; the reason stays.
func TestSaveAsErrorIsTheReasonOnly(t *testing.T) {
	dir := t.TempDir()
	d := mustDaemon(t, dir)
	api := &API{D: d}

	// A pair with no local root is invalid, and is what a half-filled form sends.
	in := ConfigView{Name: "bad"}
	in.Pairs = []PairView{{Name: "pair-1", LocalRoot: "", RemoteRoot: ":local:C:/tmp/dst"}}
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	target := filepath.Join(dir, "bad.toml")
	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/config/save-as?path="+target, strings.NewReader(string(body))))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if strings.Contains(out.Error, "the edited config is not valid") {
		t.Errorf("the wrapper is still there: %q", out.Error)
	}
	if strings.Contains(out.Error, dir) {
		t.Errorf("the error repeats the path the dialog already shows: %q", out.Error)
	}
	if !strings.Contains(out.Error, "local.root is required") {
		t.Errorf("the reason was lost: %q", out.Error)
	}
}

// The "file exists" refusal is about the name, not about what the code declines
// to do. Kept as a test because the old wording leaked into the dialog as
// "te.toml already exists; pick another name (its previous content would be
// overwritten)".
func TestSaveAsExistingFileSaysItInChinese(t *testing.T) {
	dir := t.TempDir()
	taken := oneConfig(t, dir, "taken")
	d, err := New([]string{taken}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	api := &API{D: d}

	body, _ := json.Marshal(getConfig(t, api, "taken"))
	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/config/save-as?path="+taken, strings.NewReader(string(body))))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if strings.Contains(out.Error, "would be overwritten") {
		t.Errorf("the policy explanation is still there: %q", out.Error)
	}
	if !strings.Contains(out.Error, "已经存在") {
		t.Errorf("the dialog would show English: %q", out.Error)
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

// Removing a config from the list must not touch the file unless it was asked to.
//
// The two halves of "delete this configuration" are a checkbox apart: unload the
// entry (reversible, the file is still there to re-open) versus unlink the file
// (not reversible). Getting the default wrong here would make a stray POST destroy
// somebody's settings.
func TestRemoveConfigUnloadsWithoutDeletingTheFile(t *testing.T) {
	dir := t.TempDir()
	base := oneConfig(t, dir, "base")
	other := oneConfig(t, dir, "other")
	d, err := New([]string{base, other}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	api := &API{D: d}

	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/config/remove?path="+base, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Name    string `json:"name"`
		Deleted bool   `json:"deleted"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Name != "base" {
		t.Errorf("name = %q, want %q", out.Name, "base")
	}
	if out.Deleted {
		t.Error("deleted = true; removing from the list must not unlink the file")
	}
	if _, err := os.Stat(base); err != nil {
		t.Fatalf("the file was deleted without being asked: %v", err)
	}
	if n := len(d.Status()); n != 1 {
		t.Errorf("configs = %d, want 1 (the other one is untouched)", n)
	}
	for _, st := range d.Status() {
		if st.Name == "base" {
			t.Error("base is still loaded")
		}
	}
}

// And with delete_file it really does remove the file.
func TestRemoveConfigDeletesTheFileWhenAsked(t *testing.T) {
	dir := t.TempDir()
	base := oneConfig(t, dir, "base")
	d, err := New([]string{base}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	api := &API{D: d}

	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/config/remove?path="+base+"&delete_file=true", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Deleted bool   `json:"deleted"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.Deleted || out.Error != "" {
		t.Errorf("deleted = %v, error = %q; want true and no error", out.Deleted, out.Error)
	}
	if _, err := os.Stat(base); !os.IsNotExist(err) {
		t.Fatalf("the file is still there: %v", err)
	}
	if n := len(d.Status()); n != 0 {
		t.Errorf("configs = %d, want 0", n)
	}
}

// A path that is not loaded is refused rather than silently succeeding: the
// console hides the button when nothing is open, so reaching this means the view
// and the daemon disagree, which is worth surfacing.
func TestRemoveConfigRefusesAnUnknownPath(t *testing.T) {
	dir := t.TempDir()
	d := mustDaemon(t, dir)
	rec := httptest.NewRecorder()
	api := &API{D: d}
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/config/remove?path="+filepath.Join(dir, "nope.toml"), nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if n := len(d.Status()); n != 1 {
		t.Errorf("configs = %d, want 1 (nothing was removed)", n)
	}
}
