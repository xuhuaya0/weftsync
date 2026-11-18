package daemon

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// A daemon must start with no job files at all.
//
// 2026-11-18, the user's objection: 「为什么还要准备文件啊，所有的操作不应该都在 GUI
// 进行么？不应该是 windows 一键启动，linux 一个命令启动，所有都可以在gui配置吗？
// 即使你的说的配置文件不也应该是程序自己带的或者生成的？」
//
// They were right, and the chicken-and-egg was ours: the console could create,
// edit, schedule and save configurations, but daemon.New refused to start without
// one, so the console could never be reached to make the first one. The first
// configuration was the single thing that still needed a text editor.
func TestNewAcceptsNoJobFiles(t *testing.T) {
	d, err := New(nil, io.Discard)
	if err != nil {
		t.Fatalf("a daemon with no configurations must start: %v", err)
	}
	if got := len(d.Status()); got != 0 {
		t.Errorf("status reports %d jobs, want 0", got)
	}
}

// /api/config/new must return a shape the form can render and Save As can write.
//
// That is the whole point of the endpoint: 新建 has nothing to copy on a fresh
// daemon, so the blank shape has to come from somewhere. A template that the
// encoder rejects would move the dead end from "cannot start" to "cannot save".
func TestConfigNewTemplateIsSaveable(t *testing.T) {
	d, err := New(nil, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	api := &API{D: d}

	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/config/new", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/config/new = %d body=%s", rec.Code, rec.Body.String())
	}

	var v ConfigView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The form has to know the choices, or every dropdown renders empty and the
	// defaults it cannot show are the ones that get written back.
	if len(v.Variants) == 0 {
		t.Error("the template carries no variant choices")
	}
	if len(v.Schedule.Modes) == 0 {
		t.Error("the template carries no schedule modes")
	}
	if v.Variant == "" {
		t.Error("the template has no default variant")
	}

	// Now finish the job the way the console does: fill in a pair and save.
	dir := t.TempDir()
	v.Name = "first"
	v.Pairs = []PairView{{
		Name:       "p",
		LocalRoot:  filepath.ToSlash(filepath.Join(dir, "src")),
		RemoteRoot: ":local:" + filepath.ToSlash(filepath.Join(dir, "dst")),
		Sync:       v.Sync,
	}}
	v.Schedule = ScheduleView{
		Mode: "interval", Interval: "2h", StartAt: "08:00",
		AlignToStart: true, Modes: v.Schedule.Modes,
	}

	target := filepath.Join(dir, "first.toml")
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost,
		"/api/config/save-as?path="+target, bytes.NewReader(body))
	api.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("save-as = %d body=%s", rec.Code, rec.Body.String())
	}

	// And the daemon has to know about it immediately, or the console would show
	// a configuration it cannot run.
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("the file was not written: %v", err)
	}
	found := false
	for _, st := range d.Status() {
		if st.Name == "first" {
			found = true
			if st.Schedule == "" {
				t.Error("the saved schedule did not reach the daemon")
			}
		}
	}
	if !found {
		t.Error("the saved configuration is not selectable in the daemon")
	}
}

// With nothing loaded, the Open and Save-As dialogs need somewhere to start.
//
// configDirOf used to return "" in that case, so both dialogs rendered an empty
// directory with no rows - the other half of the same dead end. The working
// directory is the honest answer: it is where the daemon was started.
func TestConfigDirOfFallsBackToWorkingDirectory(t *testing.T) {
	d, err := New(nil, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got := d.configDirOf("")
	if got == "" {
		t.Fatal("a daemon with no configurations must still offer a start directory")
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if !samePath(got, wd) {
		t.Errorf("configDirOf = %q, want the working directory %q", got, wd)
	}
}
