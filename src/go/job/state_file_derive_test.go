package job

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/config/configfile"
	"github.com/rclone/rclone/fs/fspath"

	// The derivation reads rclone's static option table, so the backend that
	// declares state_file has to be linked into this test binary. Without this
	// import rclone cannot resolve the type "feishu" and every assertion below
	// would pass for the wrong reason - the derivation would be silently dead in
	// tests while working in the shipped binary.
	_ "weftsync/backend/feishu"
)

// withFeishuRemote loads a throwaway rclone config defining "test" as a feishu
// remote.
//
// This is required, not decoration: the job layer only ever sees a remote *name*
// ("test"), and its backend *type* lives in that config file. Without the config,
// the derivation cannot know whether the destination accepts a state_file at all -
// which is exactly the distinction that keeps a local or SMB pair from being handed
// an option it would reject.
func withFeishuRemote(t *testing.T) {
	t.Helper()

	oldPath := config.GetConfigPath()
	t.Cleanup(func() { _ = config.SetConfigPath(oldPath) })

	path := filepath.Join(t.TempDir(), "rclone.conf")
	body := "[test]\ntype = feishu\napp_id = cli_test_id\napp_secret = cli_test_secret\nroot_token = root-token\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	if err := config.SetConfigPath(path); err != nil {
		t.Fatalf("SetConfigPath: %v", err)
	}
	config.SetData(&configfile.Storage{})
}

// feishuPair is the shape the demo job uses: an inline "test:" remote root with no
// options table at all.
func feishuPair(name, localRoot, remoteRoot string) Pair {
	return Pair{Name: name, Local: Endpoint{Root: localRoot}, Remote: Endpoint{Root: remoteRoot}}
}

// A feishu pair with no state_file must still get one.
//
// Leaving it out cannot mean "no snapshot": Feishu's listing carries no size and
// reports the upload time as the modification time, so without a snapshot nothing
// is comparable and every run re-uploads every file. Measured on the real tenant:
// 1487 files, 116 MB, on a pass where nothing had changed.
func TestFeishuPairWithoutStateFileGetsOne(t *testing.T) {
	withFeishuRemote(t)
	if _, err := fs.Find("feishu"); err != nil {
		t.Fatalf("the feishu backend is not linked into this test binary (%v); "+
			"the derivation cannot be tested here", err)
	}

	p := feishuPair("p", "C:/data/work", "test:")
	path, derived, err := p.StateFilePath()
	if err != nil {
		t.Fatalf("StateFilePath: %v", err)
	}
	if path == "" {
		t.Fatal("no snapshot path was derived; every run would re-upload everything")
	}
	if !derived {
		t.Error("derived should be true when the job file names no state_file")
	}
	if !strings.HasSuffix(path, ".state.json") {
		t.Errorf("derived path = %q, want a .state.json file", path)
	}

	// And it must actually reach the connection string, or the backend never sees it.
	rp, err := p.RemotePath()
	if err != nil {
		t.Fatalf("RemotePath: %v", err)
	}
	parsed, err := fspath.Parse(rp)
	if err != nil {
		t.Fatalf("rclone cannot parse %q: %v", rp, err)
	}
	if got := parsed.Config["state_file"]; got != path {
		t.Errorf("state_file in the connection string = %q, want %q (path %q)", got, path, rp)
	}
	if parsed.Name != "test" || parsed.Path != "" {
		t.Errorf("name/path = %q/%q, want test/\"\" (path %q)", parsed.Name, parsed.Path, rp)
	}
}

// Both spellings of the same pair must reach the same snapshot. Otherwise renaming
// how the remote is written would silently restart the incremental state.
func TestDerivedStateFileIsTheSameForBothSpellings(t *testing.T) {
	withFeishuRemote(t)
	inline := feishuPair("p", "C:/data/work", "test:FZivfuh4")
	explicit := Pair{Name: "p", Local: Endpoint{Root: "C:/data/work"},
		Remote: Endpoint{Backend: "test", Root: "FZivfuh4"}}

	a, _, err := inline.StateFilePath()
	if err != nil {
		t.Fatalf("inline: %v", err)
	}
	b, _, err := explicit.StateFilePath()
	if err != nil {
		t.Fatalf("backend+root: %v", err)
	}
	if a == "" || a != b {
		t.Errorf("the two spellings disagree: inline %q, backend+root %q", a, b)
	}
}

// One snapshot per pair: a different local root or a different remote root must not
// share a file, or two pairs would overwrite each other's incremental decisions.
func TestDerivedStateFileIsPerPair(t *testing.T) {
	withFeishuRemote(t)
	base := mustStateFile(t, feishuPair("p", "C:/data/work", "test:"))
	otherLocal := mustStateFile(t, feishuPair("p", "C:/data/home", "test:"))
	otherRemote := mustStateFile(t, feishuPair("p", "C:/data/work", "test:sub"))

	if base == "" {
		t.Fatal("no base path derived")
	}
	if base == otherLocal {
		t.Errorf("two different local roots share a snapshot: %q", base)
	}
	if base == otherRemote {
		t.Errorf("two different remote roots share a snapshot: %q", base)
	}
}

// mustStateFile derives the snapshot path for a pair, failing the test on error.
func mustStateFile(t *testing.T, p Pair) string {
	t.Helper()
	path, _, err := p.StateFilePath()
	if err != nil {
		t.Fatalf("StateFilePath: %v", err)
	}
	return path
}

// An explicit value wins and is not duplicated.
func TestExplicitStateFileWins(t *testing.T) {
	withFeishuRemote(t)
	const want = `C:\mine\p.state.json`
	p := Pair{Name: "p", Local: Endpoint{Root: "C:/data/work"},
		Remote: Endpoint{Backend: "test", Root: "FZivfuh4",
			Options: map[string]string{"state_file": want}}}

	got, derived, err := p.StateFilePath()
	if err != nil {
		t.Fatalf("StateFilePath: %v", err)
	}
	if got != want || derived {
		t.Errorf("got %q derived=%v, want %q derived=false", got, derived, want)
	}

	rp, err := p.RemotePath()
	if err != nil {
		t.Fatalf("RemotePath: %v", err)
	}
	if n := strings.Count(rp, "state_file="); n != 1 {
		t.Errorf("state_file appears %d times in %q, want 1", n, rp)
	}
}

// A backend without the option must be left alone: passing an option it does not
// declare makes rclone refuse to open the remote at all.
func TestBackendWithoutStateFileIsUntouched(t *testing.T) {
	withFeishuRemote(t)
	paths := []string{
		":local:C:/data/work",     // inline, local backend
		"C:/data/work",            // not an rclone path at all (caught elsewhere)
		"smb,user=me:/share/data", // inline with an existing option, no snapshot
	}
	for _, root := range paths {
		p := feishuPair("p", "C:/data/work", root)
		got, err := p.RemotePath()
		if err != nil {
			// "C:/data/work" is rejected on purpose; that is a separate rule.
			if root == "C:/data/work" {
				continue
			}
			t.Fatalf("%q: RemotePath: %v", root, err)
		}
		if strings.Contains(got, "state_file") {
			t.Errorf("%q grew a state_file (%q); that backend has no such option", root, got)
		}
		if got != root {
			t.Errorf("%q was rewritten to %q, want it passed through unchanged", root, got)
		}
	}
}
