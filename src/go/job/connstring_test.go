package job

import (
	"strings"
	"testing"

	"github.com/rclone/rclone/fs/fspath"
)

// Documents exactly how rclone parses the connection strings this package
// builds, so the escaping rules are pinned by a test rather than folklore.
func TestConnectionStringParsing(t *testing.T) {
	cases := []struct {
		in       string
		wantName string
		wantPath string
		wantOpt  string
	}{
		{`test:`, "test", "", ""},
		{`test:sub/dir`, "test", "sub/dir", ""},
		{`test,state_file=abc.json:`, "test", "", "abc.json"},
		{`test,state_file="rel/cs.json":`, "test", "", "rel/cs.json"},
		{`test,state_file="/tmp/cs.json":`, "test", "", "/tmp/cs.json"},
		{`test,state_file="C:/tmp/cs.json":`, "test", "", "C:/tmp/cs.json"},
		{`test,state_file="C:/tmp/cs.json":sub`, "test", "sub", "C:/tmp/cs.json"},
	}

	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			parsed, err := fspath.Parse(c.in)
			if err != nil {
				t.Fatalf("Parse(%q): %v", c.in, err)
			}
			if parsed.Name != c.wantName {
				t.Errorf("Name = %q, want %q", parsed.Name, c.wantName)
			}
			if parsed.Path != c.wantPath {
				t.Errorf("Path = %q, want %q", parsed.Path, c.wantPath)
			}
			if got := parsed.Config["state_file"]; got != c.wantOpt {
				t.Errorf("state_file = %q, want %q", got, c.wantOpt)
			}
		})
	}
}

// The generated path must survive rclone's parser intact, including a Windows
// path whose drive-letter colon would otherwise end the value early.
func TestRemotePathRoundTripsThroughRcloneParser(t *testing.T) {
	cases := []struct {
		name string
		pair Pair
		root string
		opt  string
	}{
		{
			name: "no options",
			pair: Pair{Name: "p", Remote: Endpoint{Backend: "feishu", Root: "team/docs"}},
			root: "team/docs",
		},
		{
			name: "windows state_file",
			pair: Pair{Name: "p", Remote: Endpoint{
				Backend: "feishu", Root: "team/docs",
				Options: map[string]string{"state_file": `C:\Users\me\AppData\Roaming\weftsync\state\a1b2.state.json`},
			}},
			root: "team/docs",
			opt:  `C:\Users\me\AppData\Roaming\weftsync\state\a1b2.state.json`,
		},
		{
			name: "value containing a comma and a quote",
			pair: Pair{Name: "p", Remote: Endpoint{
				Backend: "feishu", Root: "",
				Options: map[string]string{"state_file": `C:\odd,name"s.state.json`},
			}},
			root: "",
			opt:  `C:\odd,name"s.state.json`,
		},
		{
			name: "root with a subpath",
			pair: Pair{Name: "p", Remote: Endpoint{
				Backend: "feishu", Root: "a/b c/d",
				Options: map[string]string{"state_file": "/var/lib/w/x.state.json"},
			}},
			root: "a/b c/d",
			opt:  "/var/lib/w/x.state.json",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := c.pair.RemotePath()
			if err != nil {
				t.Fatalf("RemotePath: %v", err)
			}

			parsed, err := fspath.Parse(got)
			if err != nil {
				t.Fatalf("rclone cannot parse %q that we generated: %v", got, err)
			}
			if parsed.Name != "feishu" {
				t.Errorf("name = %q, want feishu (got path %q)", parsed.Name, got)
			}
			if parsed.Path != c.root {
				t.Errorf("root = %q, want %q (path %q)", parsed.Path, c.root, got)
			}
			if stateFile := parsed.Config["state_file"]; stateFile != c.opt {
				t.Errorf("state_file = %q, want %q (path %q)", stateFile, c.opt, got)
			}
		})
	}
}

// A password in a job file would end up in rclone's logs, because options are
// folded into the remote name. It must be refused with an explanation.
func TestOptionsRejectCredentials(t *testing.T) {
	for _, key := range []string{"app_secret", "appSecret", "root_token", "password", "api_key", "credentials"} {
		t.Run(key, func(t *testing.T) {
			err := loadErr(t, `
[[pair]]
name = "p"
[pair.local]
root = "/tmp/l"
[pair.remote]
backend = "feishu"
root = "docs"
[pair.remote.options]
`+key+` = "hunter2"
`)
			if err == nil {
				t.Fatalf("option %q must be refused", key)
			}
			if !strings.Contains(err.Error(), "credential") &&
				!strings.Contains(err.Error(), "not settable") {
				t.Errorf("error should explain why\ngot: %v", err)
			}
			if strings.Contains(err.Error(), "hunter2") {
				t.Errorf("the error must not echo the secret\ngot: %v", err)
			}
		})
	}
}

func TestOptionsRejectUnknownKey(t *testing.T) {
	err := loadErr(t, `
[[pair]]
name = "p"
[pair.local]
root = "/tmp/l"
[pair.remote]
backend = "feishu"
root = "docs"
[pair.remote.options]
chunk_size = "8M"
`)
	if err == nil {
		t.Fatal("an unknown option should be refused rather than silently ignored")
	}
	if !strings.Contains(err.Error(), "state_file") {
		t.Errorf("error should list the allowed option\ngot: %v", err)
	}
}

// Options without a backend name cannot be expressed as an rclone path.
func TestOptionsRequireBackend(t *testing.T) {
	err := loadErr(t, `
[[pair]]
name = "p"
[pair.local]
root = "/tmp/l"
[pair.remote]
root = "feishu:docs"
[pair.remote.options]
state_file = "/tmp/s.json"
`)
	if err == nil {
		t.Fatal("options without remote.backend should be refused")
	}
}
