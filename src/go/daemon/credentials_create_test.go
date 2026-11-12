package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/config/configmap"
)

// Creating a remote from the console.
//
// The console used to be able to edit remotes that already existed and nothing
// else: "创建远程是另一个功能" was the comment, and the empty state told the user to
// run `weftsync config`. That is not available to someone driving the web page, so
// the cloud side of a folder pair could not be set up at all.

func postCredentials(a *API, query string, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/credentials"+query, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	a.handleCredentials(rec, req)
	return rec
}

func TestCreateRemoteWritesOnlyTheType(t *testing.T) {
	withTempConfig(t, "")
	a := credentialAPI(t)

	rec := postCredentials(a, "?name=cloud&type=feishu", "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}

	var out struct {
		Created string         `json:"created"`
		Remote  CredentialView `json:"remote"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Created != "cloud" || out.Remote.Name != "cloud" {
		t.Errorf("created = %q / %q", out.Created, out.Remote.Name)
	}
	if out.Remote.Type != "feishu" {
		t.Errorf("type = %q", out.Remote.Type)
	}

	// The file has the type and nothing else: a create request must not smuggle
	// credentials in, and the required fields have to be reported as unset so the
	// form asks for them.
	if typ, ok := config.FileGetValue("cloud", "type"); !ok || typ != "feishu" {
		t.Errorf("stored type = %q, ok = %v", typ, ok)
	}
	if _, ok := config.FileGetValue("cloud", "app_id"); ok {
		t.Error("a create request wrote app_id")
	}
	unset := map[string]bool{}
	for _, f := range out.Remote.Fields {
		if !f.Set {
			unset[f.Name] = true
		}
	}
	for _, must := range []string{"app_id", "app_secret", "root_token"} {
		if !unset[must] {
			t.Errorf("%s should be reported unset after creation", must)
		}
	}
	// Required fields come first, which is what makes the form usable.
	if len(out.Remote.Fields) == 0 || !out.Remote.Fields[0].Required {
		t.Error("first field is not a required one")
	}
}

func TestCreateRemoteThenFillIt(t *testing.T) {
	withTempConfig(t, "")
	a := credentialAPI(t)

	if rec := postCredentials(a, "?name=cloud&type=feishu", ""); rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	rec := postCredentials(a, "?name=cloud",
		`{"values":{"app_id":"cli_1","app_secret":"sec","root_token":"fld_1"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}

	// The secret must not come back, and the two plain values must.
	body := rec.Body.String()
	if strings.Contains(body, `"sec"`) {
		t.Error("the response echoed the secret")
	}
	if !strings.Contains(body, `"cli_1"`) || !strings.Contains(body, `"fld_1"`) {
		t.Errorf("plain values missing from %s", body)
	}

	// And the create path must not have been taken again for the save.
	if v, _ := config.FileGetValue("cloud", "app_id"); v != "cli_1" {
		t.Errorf("stored app_id = %q", v)
	}
}

func TestCreateRemoteRejectsBadInput(t *testing.T) {
	withTempConfig(t, twoRemotes)
	a := credentialAPI(t)

	cases := []struct {
		name  string
		query string
		want  int
		why   string
	}{
		{"existing name", "?name=edge&type=feishu", http.StatusConflict, "edge already exists"},
		{"empty name", "?name=&type=feishu", http.StatusBadRequest, "no name"},
		{"colon in name", "?name=a:b&type=feishu", http.StatusBadRequest, "colon is the path separator"},
		{"slash in name", "?name=a/b&type=feishu", http.StatusBadRequest, "slash looks like a subdirectory"},
		{"leading dash", "?name=-x&type=feishu", http.StatusBadRequest, "looks like a flag"},
		{"unknown type", "?name=fresh&type=nosuchbackend", http.StatusBadRequest, "unknown backend"},
		// No type at all is not a create request, it is an update of a remote that
		// does not exist - so it goes down the save path and 404s with a sentence
		// that says how to create one.
		{"no type", "?name=fresh", http.StatusNotFound, "that is a save, not a create"},
	}
	for _, c := range cases {
		rec := postCredentials(a, c.query, "")
		if rec.Code != c.want {
			t.Errorf("%s: status = %d, want %d (%s)", c.name, rec.Code, c.want, rec.Body.String())
		}
		if c.want == http.StatusNotFound && !strings.Contains(rec.Body.String(), "type") {
			t.Errorf("the 404 does not say how to create one: %s", rec.Body.String())
		}
	}

	// Nothing above may have created a section.
	for _, n := range []string{"a:b", "a/b", "-x", "fresh", ""} {
		if _, ok := config.FileGetValue(n, "type"); ok {
			t.Errorf("a rejected request created remote %q", n)
		}
	}
}

// env-var-backed fields must not be offered for editing, and a create must not
// claim they are unset - the backend will happily use the environment, and a form
// that says "未设置" beside a working configuration invites the user to overwrite it.
func TestCreateRemoteReportsEnvBackedFields(t *testing.T) {
	withTempConfig(t, "")
	a := credentialAPI(t)
	t.Setenv("RCLONE_CONFIG_CLOUD_APP_ID", "from-env")

	if rec := postCredentials(a, "?name=cloud&type=feishu", ""); rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}

	var out struct {
		Remote CredentialView `json:"remote"`
	}
	rec := postCredentials(a, "?name=cloud&type=feishu", "") // conflict, ignore body
	_ = rec
	// Read the list instead: it is the same view the page renders.
	greq := httptest.NewRequest(http.MethodGet, "/api/credentials", nil)
	grec := httptest.NewRecorder()
	a.handleCredentials(grec, greq)
	if grec.Code != http.StatusOK {
		t.Fatalf("list: %d", grec.Code)
	}
	var list struct {
		Remotes []CredentialView `json:"remotes"`
	}
	if err := json.Unmarshal(grec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	out.Remote = list.Remotes[0]

	for _, f := range out.Remote.Fields {
		if f.Name == "app_id" {
			if !f.Set || !f.FromEnv {
				t.Errorf("app_id: set=%v from_env=%v, want both true", f.Set, f.FromEnv)
			}
			return
		}
	}
	t.Fatal("app_id not in the field list")
}

// The ordering and filtering are tested against a fixed registry, not fs.Registry:
// the daemon's tests link only the feishu backend while main.go links all of them,
// so a test driven by the real registry would describe the test binary instead of
// what a user sees.
func TestRemoteTypesFromFiltersAndOrders(t *testing.T) {
	reg := []*fs.RegInfo{
		{Name: "aacrypt", Description: "wrapper"},
		{Name: "local", Description: "local disk"},          // not a cloud connection
		{Name: "hiddenthing", Description: "x", Hide: true}, // rclone hides it
		{Name: "zzz", Description: "last alphabetically"},
		{Name: "s3", Description: "Amazon S3"},
		{Name: "feishu", Description: "Feishu / Lark Drive"},
		{Name: "AAAcloud", Description: "first alphabetically"},
		nil,
		{Name: ""},
	}

	got := remoteTypesFrom(reg)

	var names []string
	for _, v := range got {
		names = append(names, v.Name)
	}
	want := []string{"feishu", "s3", "AAAcloud", "aacrypt", "zzz"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("types = %v, want %v", names, want)
	}

	if !got[0].Recommended || got[2].Recommended {
		t.Errorf("recommended flags wrong: %v", got)
	}
	for _, v := range got {
		if v.Description == "" {
			t.Errorf("%s has no description", v.Name)
		}
	}
}

// A backend that cannot be configured by typing values must be marked and sorted
// last, not silently offered. Both signals come from rclone's own declarations.
func TestRemoteTypesBlockWhatCannotBeFilledIn(t *testing.T) {
	wrapper := &fs.RegInfo{
		Name: "alias", Description: "Alias for an existing remote",
		Options: fs.Options{{Name: "remote", Required: true}},
	}
	oauth := &fs.RegInfo{
		Name: "drive", Description: "Google Drive",
		Options: fs.Options{{Name: "token"}, {Name: "scope"}},
		Config: func(context.Context, string, configmap.Mapper, fs.ConfigIn) (*fs.ConfigOut, error) {
			return nil, nil
		},
	}
	plain := &fs.RegInfo{
		Name: "webdav", Description: "WebDAV",
		Options: fs.Options{{Name: "url", Required: true}},
	}
	mem := &fs.RegInfo{Name: "memory", Description: "in memory"}

	got := remoteTypesFrom([]*fs.RegInfo{oauth, wrapper, plain, mem})

	byName := map[string]RemoteType{}
	for _, v := range got {
		byName[v.Name] = v
	}
	for _, n := range []string{"alias", "drive", "memory"} {
		if !byName[n].Blocked || byName[n].Reason == "" {
			t.Errorf("%s should be blocked with a reason, got %+v", n, byName[n])
		}
		if byName[n].Recommended {
			t.Errorf("%s is both blocked and recommended", n)
		}
	}
	if byName["webdav"].Blocked {
		t.Errorf("webdav should be usable: %+v", byName["webdav"])
	}
	if last := got[len(got)-1]; !last.Blocked {
		t.Errorf("last entry %q is not blocked; blocked ones must come last", last.Name)
	}
}

// The create endpoint refuses a blocked type instead of producing a connection
// that can never authenticate.
func TestCreateRemoteRefusesBlockedType(t *testing.T) {
	withTempConfig(t, "")
	a := credentialAPI(t)

	// alias/union are registered by backend/all in production; this test binary
	// links feishu only, so use whichever blocked type it does know about.
	var blocked string
	for _, v := range a.remoteTypes() {
		if v.Blocked {
			blocked = v.Name
			break
		}
	}
	if blocked == "" {
		t.Skip("this test binary links no blocked backend")
	}

	rec := postCredentials(a, "?name=w&type="+blocked, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "不能在这个页面创建") {
		t.Errorf("the refusal does not explain itself: %s", rec.Body.String())
	}
	if _, ok := config.FileGetValue("w", "type"); ok {
		t.Error("a blocked type was written to the config anyway")
	}
}

func deleteCredentials(a *API, query string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodDelete, "/api/credentials"+query, nil)
	rec := httptest.NewRecorder()
	a.handleCredentials(rec, req)
	return rec
}

func TestDeleteRemote(t *testing.T) {
	path := withTempConfig(t, twoRemotes)
	a := credentialAPI(t)

	rec := deleteCredentials(a, "?name=other&force=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}

	var out struct {
		Deleted string           `json:"deleted"`
		Remotes []CredentialView `json:"remotes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Deleted != "other" {
		t.Errorf("deleted = %q", out.Deleted)
	}
	if _, ok := config.FileGetValue("other", "type"); ok {
		t.Error("the section is still in the config")
	}
	for _, r := range out.Remotes {
		if r.Name == "other" {
			t.Error("the deleted remote is still listed")
		}
	}
	// The other remote survived.
	if typ, ok := config.FileGetValue("edge", "type"); !ok || typ != "feishu" {
		t.Errorf("edge was collateral damage: %q %v", typ, ok)
	}
	// And it is gone from the file on disk, not just from memory.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(data), "[other]") {
		t.Errorf("the section is still on disk:\n%s", data)
	}
}

// A connection a loaded job still points at must not be deleted silently: the job
// would keep a path whose remote no longer exists, and the failure would surface
// mid-run.
func TestDeleteRemoteRefusesWhileUsed(t *testing.T) {
	withTempConfig(t, twoRemotes)
	a := credentialAPI(t) // its job uses remote "edge"

	rec := deleteCredentials(a, "?name=edge")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "sync") {
		t.Errorf("the refusal does not name the job: %s", rec.Body.String())
	}
	if _, ok := config.FileGetValue("edge", "type"); !ok {
		t.Error("the remote was deleted despite being in use")
	}

	// force=1 is the explicit override.
	rec = deleteCredentials(a, "?name=edge&force=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("forced delete: %d %s", rec.Code, rec.Body.String())
	}
	if _, ok := config.FileGetValue("edge", "type"); ok {
		t.Error("forced delete did not remove the section")
	}
}

func TestDeleteRemoteRejectsUnknown(t *testing.T) {
	withTempConfig(t, twoRemotes)
	a := credentialAPI(t)

	for _, q := range []string{"?name=nosuch", "?name="} {
		rec := deleteCredentials(a, q)
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d (%s)", q, rec.Code, rec.Body.String())
		}
	}
}

// The real registry (whatever this binary links) must still surface feishu first:
// it is the backend this program ships and documents.
func TestRemoteTypesRealRegistryStartsWithFeishu(t *testing.T) {
	a := credentialAPI(t)
	types := a.remoteTypes()
	if len(types) == 0 {
		t.Fatal("no types at all")
	}
	if types[0].Name != "feishu" {
		t.Errorf("first type = %q, want feishu", types[0].Name)
	}
	for _, v := range types {
		if v.Name == "local" {
			t.Error("local leaked into the cloud type list")
		}
		if v.Blocked && v.Recommended {
			t.Errorf("%s is both blocked and recommended", v.Name)
		}
	}
}
