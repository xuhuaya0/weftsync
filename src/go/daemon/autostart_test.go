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

	"weftsync/autostart"
)

func autostartAPI(t *testing.T) *API {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "job.toml")
	body := `
[job]
name = "j"

[[pair]]
name = "p"
[pair.local]
root = "` + filepath.ToSlash(filepath.Join(dir, "local")) + `"
[pair.remote]
backend = "feishu"
root = "team/docs"
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write job: %v", err)
	}
	d, err := New([]string{path}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &API{D: d}
}

// A daemon that cannot describe its own command line must say so rather than
// offer a toggle that would register nothing.
func TestAutostartUnavailableWithoutSpec(t *testing.T) {
	api := autostartAPI(t)

	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/autostart", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var st autostart.Status
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if st.Supported {
		t.Error("without a spec the feature must report itself unavailable")
	}
	if st.Detail == "" {
		t.Error("the UI needs a reason to show")
	}

	// Writing must be refused, not silently ignored.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/autostart",
		strings.NewReader(`{"enabled":true}`))
	api.Routes().ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Error("enabling must fail when there is no command to register")
	}
}

// A body without "enabled" is a client bug. Treating it as false would delete a
// working autostart entry on a malformed request.
func TestAutostartRequiresEnabledField(t *testing.T) {
	api := autostartAPI(t)
	api.Autostart = autostart.Spec{Exe: `C:\tools\weftsync.exe`, Args: []string{"daemon"}}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/autostart", strings.NewReader(`{}`))
	api.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400\nbody=%s", rec.Code, rec.Body.String())
	}
}

// Reading the status must work and must not write anything.
func TestAutostartStatusIsReadable(t *testing.T) {
	api := autostartAPI(t)
	api.Autostart = autostart.Spec{Exe: `C:\tools\weftsync.exe`, Args: []string{"daemon"}}

	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/autostart", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var st autostart.Status
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Whether the machine happens to have the entry is not this test's business;
	// what matters is that a location is reported and the shape is usable.
	if st.Location == "" {
		t.Error("the UI needs somewhere to point the user at")
	}
}

func TestAutostartRejectsOtherMethods(t *testing.T) {
	api := autostartAPI(t)
	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/autostart", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}
