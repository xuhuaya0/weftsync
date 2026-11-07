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

	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/config/configfile"
	"github.com/rclone/rclone/fs/config/obscure"

	// The credentials view is built from each backend's own option table, so the
	// backend has to be registered in this test binary. Only main.go imports it
	// in production, which is why the listing degrades to "unknown backend type"
	// rather than panicking when it is absent.
	_ "weftsync/backend/feishu"
)

// withTempConfig points rclone's config store at a throwaway file.
//
// The credentials endpoints work on "the config file rclone was started with", so
// a test has to replace that path globally. Both the path and the in-memory store
// must be reset, otherwise the store keeps the previous file's contents and the
// test silently reads somebody else's config.
func withTempConfig(t *testing.T, body string) string {
	t.Helper()

	oldPath := config.GetConfigPath()
	t.Cleanup(func() { _ = config.SetConfigPath(oldPath) })

	path := filepath.Join(t.TempDir(), "rclone.conf")
	if body != "" {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("seed config: %v", err)
		}
	}
	if err := config.SetConfigPath(path); err != nil {
		t.Fatalf("SetConfigPath: %v", err)
	}
	config.SetData(&configfile.Storage{})
	return path
}

const twoRemotes = `[edge]
type = feishu
app_id = cli_test_id
app_secret = plain-text-secret
root_token = fld_test

[other]
type = local
`

func credentialAPI(t *testing.T) *API {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "job.toml")
	body := `
[job]
name = "sync"

[[pair]]
name = "p"
[pair.local]
root = "` + filepath.ToSlash(filepath.Join(dir, "local")) + `"
[pair.remote]
backend = "edge"
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

// getCredentials runs the GET handler and returns the raw body.
func getCredentials(t *testing.T, api *API) (string, []CredentialView) {
	t.Helper()
	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/credentials", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Remotes []CredentialView `json:"remotes"`
		File    string           `json:"file"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return rec.Body.String(), payload.Remotes
}

func findView(views []CredentialView, name string) *CredentialView {
	for i := range views {
		if views[i].Name == name {
			return &views[i]
		}
	}
	return nil
}

func findField(v *CredentialView, name string) *CredentialField {
	for i := range v.Fields {
		if v.Fields[i].Name == name {
			return &v.Fields[i]
		}
	}
	return nil
}

// The response must never contain the secret, in either its stored or revealed
// form. This is the rule the whole file exists to protect.
func TestCredentialsNeverReturnSecret(t *testing.T) {
	withTempConfig(t, twoRemotes)
	api := credentialAPI(t)

	raw, views := getCredentials(t, api)
	if strings.Contains(raw, "plain-text-secret") {
		t.Error("the response contains the secret in plaintext")
	}

	edge := findView(views, "edge")
	if edge == nil {
		t.Fatal("the remote is missing from the listing")
	}
	secret := findField(edge, "app_secret")
	if secret == nil {
		t.Fatal("app_secret is missing from the field list")
	}
	if !secret.Secret {
		t.Error("app_secret must be marked secret")
	}
	if secret.Value != "" {
		t.Errorf("a secret field carried a value: %q", secret.Value)
	}
	if !secret.Set {
		t.Error("the field is populated, so Set must be true")
	}
	if secret.Length != len("plain-text-secret") {
		t.Errorf("length = %d, want %d", secret.Length, len("plain-text-secret"))
	}

	// Non-secret fields still show their value: the point is to let the user check
	// app_id and root_token without opening the file.
	if got := findField(edge, "app_id"); got == nil || got.Value != "cli_test_id" {
		t.Errorf("app_id = %+v, want its value visible", got)
	}
}

// Saving a password field stores it obscured, so the file does not hold the secret
// in readable form.
func TestCredentialSaveObscuresPassword(t *testing.T) {
	path := withTempConfig(t, twoRemotes)
	api := credentialAPI(t)

	body := `{"values":{"app_secret":"a-brand-new-secret-value"}}`
	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/credentials?name=edge", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "a-brand-new-secret-value") {
		t.Error("the save response echoed the secret")
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(raw), "a-brand-new-secret-value") {
		t.Error("the config file stores the secret in plaintext")
	}
	if !strings.Contains(string(raw), "app_secret = ") {
		t.Error("app_secret was not written at all")
	}

	// And it must be recoverable, i.e. actually obscured rather than mangled.
	stored, _ := config.FileGetValue("edge", "app_secret")
	revealed, err := obscure.Reveal(stored)
	if err != nil {
		t.Fatalf("the stored value is not a valid obscured blob: %v", err)
	}
	if revealed != "a-brand-new-secret-value" {
		t.Errorf("revealed = %q, want the submitted secret", revealed)
	}

	// The other section must be untouched: a save is scoped to one remote.
	if v, _ := config.FileGetValue("other", "type"); v != "local" {
		t.Errorf("the other remote was modified: type = %q", v)
	}
}

// Surrounding whitespace is stripped rather than stored.
//
// A trailing newline is the single most common reason a correct-looking secret
// fails, and silently keeping it would make this page the source of the problem.
func TestCredentialSaveTrimsWhitespace(t *testing.T) {
	withTempConfig(t, twoRemotes)
	api := credentialAPI(t)

	body := `{"values":{"app_id":"  spaced-id\r\n"}}`
	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/credentials?name=edge", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	got, _ := config.FileGetValue("edge", "app_id")
	if got != "spaced-id" {
		t.Errorf("app_id = %q, want it trimmed", got)
	}
}

// An unknown option name is a typo, and writing it would add a key the backend
// ignores while looking like it did something.
func TestCredentialSaveRejectsUnknownOption(t *testing.T) {
	path := withTempConfig(t, twoRemotes)
	before, _ := os.ReadFile(path)
	api := credentialAPI(t)

	body := `{"values":{"app_secert":"typo"}}`
	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/credentials?name=edge", strings.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "app_secert") {
		t.Errorf("the error should name the offending key, got %s", rec.Body.String())
	}

	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("the config changed despite the rejection")
	}
}

// A name that is not in the config is a 404, and no section is invented: creating a
// remote needs a type and a test, which is a different feature.
func TestCredentialSaveRejectsUnknownRemote(t *testing.T) {
	withTempConfig(t, twoRemotes)
	api := credentialAPI(t)

	body := `{"values":{"app_id":"x"}}`
	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/credentials?name=typo-remote", strings.NewReader(body)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if s := config.FileSections(); len(s) != 2 {
		t.Errorf("sections = %v, want the two that were already there", s)
	}
}

// An empty string clears a field; null leaves it alone. The distinction is what
// makes a partial form safe to submit.
func TestCredentialSaveClearAndLeaveAlone(t *testing.T) {
	withTempConfig(t, twoRemotes)
	api := credentialAPI(t)

	// null for app_id (leave), "" for root_token (clear).
	body := `{"values":{"app_id":null,"root_token":""}}`
	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/credentials?name=edge", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	if got, _ := config.FileGetValue("edge", "app_id"); got != "cli_test_id" {
		t.Errorf("app_id = %q, want it left alone", got)
	}
	if got, found := config.FileGetValue("edge", "root_token"); found && got != "" {
		t.Errorf("root_token = %q, want it removed", got)
	}
}

// The config file holds credentials, so it must not be group- or world-readable.
func TestCredentialSaveRestrictsPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions do not apply on Windows")
	}
	path := withTempConfig(t, twoRemotes)
	// Start deliberately loose.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	api := credentialAPI(t)

	body := `{"values":{"app_id":"tightened"}}`
	rec := httptest.NewRecorder()
	api.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/credentials?name=edge", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600", perm)
	}
}

// used_by must connect a job to the remote it writes to, in both spellings.
func TestCredentialViewsMapsJobsToRemotes(t *testing.T) {
	withTempConfig(t, twoRemotes)

	dir := t.TempDir()
	mk := func(name, remoteBlock string) string {
		path := filepath.Join(dir, name+".toml")
		body := `
[job]
name = "` + name + `"

[[pair]]
name = "p"
[pair.local]
root = "` + filepath.ToSlash(filepath.Join(dir, name+"-local")) + `"
[pair.remote]
` + remoteBlock
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write job: %v", err)
		}
		return path
	}

	// One job names the remote in backend, the other inline in root.
	a := mk("named", "backend = \"edge\"\nroot = \"team/docs\"")
	b := mk("inline", "root = \"edge:other/path\"")
	d, err := New([]string{a, b}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, views := getCredentials(t, &API{D: d})
	edge := findView(views, "edge")
	if edge == nil {
		t.Fatal("the remote is missing")
	}
	if len(edge.UsedBy) != 2 || edge.UsedBy[0] != "inline" || edge.UsedBy[1] != "named" {
		t.Errorf("used_by = %v, want both jobs sorted", edge.UsedBy)
	}

	// A remote nobody uses must still be listed, with an empty used_by.
	other := findView(views, "other")
	if other == nil {
		t.Fatal("an unused remote should still be listed")
	}
	if len(other.UsedBy) != 0 {
		t.Errorf("used_by = %v, want empty", other.UsedBy)
	}
}

// An environment variable is reported as the source, because the page cannot
// change it and overwriting a working setup is worse than saying so.
func TestCredentialViewsReportsEnvironment(t *testing.T) {
	withTempConfig(t, "[edge]\ntype = feishu\n")
	t.Setenv("RCLONE_CONFIG_EDGE_APP_ID", "from-the-environment")

	api := credentialAPI(t)
	_, views := getCredentials(t, api)
	edge := findView(views, "edge")
	if edge == nil {
		t.Fatal("the remote is missing")
	}
	field := findField(edge, "app_id")
	if field == nil {
		t.Fatal("app_id is missing")
	}
	if !field.FromEnv {
		t.Error("the field should be marked as coming from the environment")
	}
	if field.Value != "from-the-environment" {
		t.Errorf("value = %q", field.Value)
	}
}

// A secret that is only in the environment must not be echoed either.
func TestCredentialViewsHidesEnvironmentSecret(t *testing.T) {
	withTempConfig(t, "[edge]\ntype = feishu\n")
	t.Setenv("RCLONE_CONFIG_EDGE_APP_SECRET", "env-secret-value")

	api := credentialAPI(t)
	raw, views := getCredentials(t, api)
	if strings.Contains(raw, "env-secret-value") {
		t.Error("the response leaked a secret that came from the environment")
	}
	edge := findView(views, "edge")
	field := findField(edge, "app_secret")
	if field == nil {
		t.Fatal("app_secret is missing")
	}
	if field.Value != "" {
		t.Error("a secret field carried a value")
	}
	if !field.Set {
		t.Error("the field is set through the environment")
	}
}
