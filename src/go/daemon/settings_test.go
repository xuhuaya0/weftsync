package daemon

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// withTempSettings keeps the real user profile untouched.
//
// A test that writes to %APPDATA% leaves state behind that changes the next run -
// including the next run of the real program.
func withTempSettings(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev := settingsPath
	settingsPath = func() string { return filepath.Join(dir, "settings.json") }
	t.Cleanup(func() { settingsPath = prev })
	return settingsPath()
}

func TestSettingsDefaultsAreTheSensibleOnes(t *testing.T) {
	withTempSettings(t)
	s, err := LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings on a fresh dir: %v", err)
	}
	if !s.NotificationsOn() {
		t.Error("notifications should default to on: they only fire for problems")
	}
	if len(s.Exclude) != 0 {
		t.Errorf("exclude = %v, want none by default", s.Exclude)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	path := withTempSettings(t)
	off := false
	in := Settings{NotifyEnabled: &off, Exclude: []string{"*.tmp", ".git/**"}}
	if err := in.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// The file must be owner-only: it names folders that are synchronised.
	// Windows has no unix permission bits - access there is governed by the ACL,
	// and os.WriteFile's mode is ignored - so this assertion only means something
	// on the platforms where the mode is real.
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if mode := info.Mode().Perm(); mode&0o077 != 0 {
			t.Errorf("mode = %v, want no group/other access", mode)
		}
	}

	out, err := LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if out.NotificationsOn() {
		t.Error("the saved false was not preserved")
	}
	if strings.Join(out.Exclude, ",") != "*.tmp,.git/**" {
		t.Errorf("exclude = %v", out.Exclude)
	}
}

// A corrupt file must not stop the daemon: the log says so and the defaults apply.
func TestCorruptSettingsFallBackToDefaults(t *testing.T) {
	path := withTempSettings(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	sink := NewLogSink(io.Discard)
	dir := t.TempDir()
	d, err := New([]string{writeJobFile(t, dir, "a", simpleJob("a", ""))}, sink)
	if err != nil {
		t.Fatalf("New must survive a corrupt settings file: %v", err)
	}
	if !d.SettingsOf().NotificationsOn() {
		t.Error("defaults should apply after a parse failure")
	}
	lines := sink.(*logSink).Recent()
	var sawWarning bool
	for _, line := range lines {
		if strings.Contains(line.Text, "settings:") {
			sawWarning = true
		}
	}
	if !sawWarning {
		t.Error("the daemon should say once that settings could not be read")
	}
}

// A pattern with a syntax error has to be refused at save time, not discovered by
// a scheduled run hours later.
func TestSettingsRejectBadFilterPattern(t *testing.T) {
	withTempSettings(t)
	dir := t.TempDir()
	d, _ := New([]string{writeJobFile(t, dir, "a", simpleJob("a", ""))}, io.Discard)
	api := &API{D: d, Addr: "x"}

	body := `{"exclude":["[unclosed"]}`
	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/settings",
		strings.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "unclosed") {
		t.Errorf("the error should quote the bad pattern\ngot: %s", rec.Body.String())
	}
}

func TestSettingsAPIRoundTrip(t *testing.T) {
	path := withTempSettings(t)
	dir := t.TempDir()
	d, _ := New([]string{writeJobFile(t, dir, "a", simpleJob("a", ""))}, io.Discard)
	api := &API{D: d, Addr: "x"}

	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/settings",
		strings.NewReader(`{"notify_enabled":false,"exclude":["*.bak"]}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST status = %d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("settings file was not written: %v", err)
	}
	// Applied without a restart.
	if d.SettingsOf().NotificationsOn() {
		t.Error("the running daemon did not pick up the new settings")
	}

	rec = httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d", rec.Code)
	}
	var view SettingsView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if view.NotificationsOn() {
		t.Error("GET should report the saved value")
	}
	if !strings.HasSuffix(view.SettingsFile, "settings.json") {
		t.Errorf("SettingsFile = %q", view.SettingsFile)
	}
	if view.NotificationsAvailable {
		t.Error("no notifier is installed in this test, so availability must be false")
	}
}

// Turning notifications off must silence the callback, and turning them back on
// must restore it - not merely record the preference.
func TestNotifySwitchSilencesTheCallback(t *testing.T) {
	withTempSettings(t)
	dir := t.TempDir()
	d, err := New([]string{writeJobFile(t, dir, "a", simpleJob("a", ""))}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var calls int
	d.SetNotifier(func(title, text string) { calls++ })

	d.notifyf("case one", "should be delivered")
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}

	off := false
	d.ApplySettings(Settings{NotifyEnabled: &off})
	d.notifyf("case two", "should be suppressed")
	if calls != 1 {
		t.Errorf("calls = %d after disabling, want 1", calls)
	}

	on := true
	d.ApplySettings(Settings{NotifyEnabled: &on})
	d.notifyf("case three", "delivered again")
	if calls != 2 {
		t.Errorf("calls = %d after re-enabling, want 2", calls)
	}
}

func TestSettingsRejectsUnknownMethod(t *testing.T) {
	withTempSettings(t)
	dir := t.TempDir()
	d, _ := New([]string{writeJobFile(t, dir, "a", simpleJob("a", ""))}, io.Discard)
	api := &API{D: d, Addr: "x"}
	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/settings", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}
