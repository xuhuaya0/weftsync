package daemon

import (
	"context"
	"strings"
	"testing"
)

// Path classification is the hinge of the whole picker: get it wrong and the
// daemon either tries to open "C:" as a remote or shows a cloud folder as empty
// because it looked on the local disk. These cases are all shapes that appear in
// real job files.
func TestIsRemotePath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"feishu:token", true},
		{"feishu:", true},
		{"backup:photos/2024", true},
		{"my-remote:a", true},
		{"my_remote:a", true},
		{"a.b:a", true},
		// Windows drive letters look exactly like a remote named "C".
		{`C:\data`, false},
		{`C:/data`, false},
		{"C:", false},
		{`D:\`, false},
		// Local paths that happen to contain a colon.
		{"/tmp/x", false},
		{`\\server\share`, false},
		{"relative/dir", false},
		{"", false},
		// rclone's on-the-fly form: the colon is at position 0, so it is not a
		// remote name here; stripLocalBackend handles it before this runs.
		{":local:C:/data", false},
	}
	for _, c := range cases {
		if got := isRemotePath(c.path); got != c.want {
			t.Errorf("isRemotePath(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestStripLocalBackend(t *testing.T) {
	cases := []struct {
		in       string
		want     string
		stripped bool
	}{
		{":local:C:/data", "C:/data", true},
		{":LOCAL:/tmp/x", "/tmp/x", true},
		{"feishu:token", "feishu:token", false},
		{`C:\data`, `C:\data`, false},
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := stripLocalBackend(c.in)
		if got != c.want || ok != c.stripped {
			t.Errorf("stripLocalBackend(%q) = (%q,%v), want (%q,%v)",
				c.in, got, ok, c.want, c.stripped)
		}
	}
}

// Every navigation step has to produce a path the picker can send straight back.
// A double separator or a trailing slash is not cosmetic: rclone would list a
// different folder, or none.
func TestRemotePathArithmetic(t *testing.T) {
	cases := []struct{ anchor, name, joined string }{
		{"feishu:", "docs", "feishu:docs"},
		{"feishu:docs", "2024", "feishu:docs/2024"},
		{"feishu:docs/2024/", "q1", "feishu:docs/2024/q1"},
		{`feishu:docs\2024`, "q1", "feishu:docs/2024/q1"},
		{"feishu:/docs", "q1", "feishu:docs/q1"},
	}
	for _, c := range cases {
		if got := joinRemotePath(c.anchor, c.name); got != c.joined {
			t.Errorf("joinRemotePath(%q,%q) = %q, want %q",
				c.anchor, c.name, got, c.joined)
		}
	}

	parents := []struct{ anchor, up string }{
		{"feishu:", ""},            // the remote root has no parent
		{"feishu:docs", "feishu:"}, // one level below the root
		{"feishu:docs/2024", "feishu:docs"},
		{"feishu:docs/2024/", "feishu:docs"},
		{"feishu:/docs", "feishu:"},
	}
	for _, c := range parents {
		if got := remoteParent(c.anchor); got != c.up {
			t.Errorf("remoteParent(%q) = %q, want %q", c.anchor, got, c.up)
		}
	}

	names := []struct{ anchor, name string }{
		{"feishu:", "feishu"},
		{"feishu:docs", "feishu"},
		{"feishu:docs/2024", "feishu"},
	}
	for _, c := range names {
		if got := remoteNameOf(c.anchor); got != c.name {
			t.Errorf("remoteNameOf(%q) = %q, want %q", c.anchor, got, c.name)
		}
	}

	displays := []struct{ anchor, want string }{
		{"feishu:", "feishu:"},
		{"feishu:/", "feishu:"},
		{"feishu:docs", "feishu:docs"},
		{"feishu:docs/", "feishu:docs"},
		{"feishu://docs", "feishu:docs"},
	}
	for _, c := range displays {
		if got := displayRemotePath(c.anchor); got != c.want {
			t.Errorf("displayRemotePath(%q) = %q, want %q", c.anchor, got, c.want)
		}
	}
}

// A local path must not be sent through the remote half. This is the bug that
// would make the picker show an empty folder for a directory that exists.
func TestBrowseViewDispatchesLocal(t *testing.T) {
	dir := t.TempDir()
	view := browseView(context.Background(), dir)
	if view.Kind != BrowseLocal {
		t.Fatalf("kind = %q, want %q", view.Kind, BrowseLocal)
	}
	if view.Error != "" {
		t.Fatalf("unexpected error: %s", view.Error)
	}
	if got := strings.TrimSuffix(view.Path, "/"); got != strings.TrimSuffix(dir, "/") &&
		got != strings.TrimSuffix(dir, `\`) {
		t.Fatalf("view.Path = %q, want %q", view.Path, dir)
	}
}

// The :local: form is what a pair actually stores when the backend is spelled
// out, so the picker has to accept it as a local directory.
func TestBrowseViewAcceptsLocalBackendPrefix(t *testing.T) {
	dir := t.TempDir()
	view := browseView(context.Background(), ":local:"+dir)
	if view.Kind != BrowseLocal {
		t.Fatalf("kind = %q, want %q; a :local: path is a disk path", view.Kind, BrowseLocal)
	}
	if view.Error != "" {
		t.Fatalf("unexpected error: %s", view.Error)
	}
}

// An unknown remote reports the failure in the view instead of an HTTP error, so
// the dialog can still offer the remote list and let the user switch.
func TestBrowseViewRemoteFailureIsReported(t *testing.T) {
	view := browseView(context.Background(), "no-such-remote-xyz:some/path")
	if view.Kind != BrowseRemote {
		t.Fatalf("kind = %q, want %q", view.Kind, BrowseRemote)
	}
	if view.Error == "" {
		t.Fatal("an unknown remote must report an error, not an empty listing")
	}
	// The message has to name the remote, or the user cannot tell which of
	// several pairs is broken.
	if !strings.Contains(view.Error, "no-such-remote-xyz") {
		t.Fatalf("error %q does not name the remote", view.Error)
	}
}

// The dialog needs somewhere to start from; offering an empty remote list would
// leave a cloud-only user with a picker they cannot use.
func TestConfiguredRemotesHasNoEmptyNames(t *testing.T) {
	for _, r := range configuredRemotes() {
		if r.Name == "" {
			t.Fatal("configuredRemotes returned an entry with no name")
		}
		if r.Type == "" {
			t.Fatalf("remote %q has no type but was offered anyway", r.Name)
		}
	}
}

// A successful listing still has to carry the starting points. On Windows there
// is no single filesystem root, so a picker that only sends Roots on failure can
// never leave the drive it opened on - which is what happened: the drive list was
// dead code until this assertion existed.
func TestBrowseViewAlwaysReportsRoots(t *testing.T) {
	view := browseView(context.Background(), t.TempDir())
	if view.Error != "" {
		t.Fatalf("unexpected error: %s", view.Error)
	}
	if len(view.Roots) == 0 {
		t.Fatal("a successful local listing sent no roots; the picker cannot switch drives")
	}
}

// Both worlds are offered in the same dialog, so each view has to carry the other
// side's starting points - otherwise switching between them means closing it.
func TestBrowseViewCarriesBothWorlds(t *testing.T) {
	local := browseView(context.Background(), t.TempDir())
	if local.Remotes == nil {
		t.Fatal("a local view must still list the configured remotes")
	}

	remote := browseView(context.Background(), "no-such-remote-xyz:")
	if len(remote.Roots) == 0 {
		t.Fatal("a remote view must still list the local roots")
	}
}
