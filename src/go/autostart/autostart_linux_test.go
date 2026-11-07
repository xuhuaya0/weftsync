//go:build linux

package autostart

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// useTempConfig points os.UserConfigDir at a temp directory so the test cannot
// write into the real ~/.config.
func useTempConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	return dir
}

func TestEnableReadDisableLinux(t *testing.T) {
	dir := useTempConfig(t)

	want := Spec{
		Exe:  "/usr/local/bin/weftsync",
		Args: []string{"daemon", "/home/me/My Documents/job.toml"},
	}

	st, err := Read(want)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if st.Enabled {
		t.Fatal("nothing should be registered yet")
	}

	if err := Enable(want); err != nil {
		t.Fatalf("Enable: %v", err)
	}

	path := filepath.Join(dir, "autostart", fileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the entry should exist at %s: %v", path, err)
	}
	for _, want := range []string{
		"[Desktop Entry]", "Type=Application", "Exec=", "X-GNOME-Autostart-enabled=true",
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("entry is missing %q:\n%s", want, raw)
		}
	}

	st, err = Read(want)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !st.Enabled || !st.Matches {
		t.Errorf("entry should match the spec it was written from (enabled=%v matches=%v)\n%s",
			st.Enabled, st.Matches, raw)
	}

	if err := Disable(); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	st, _ = Read(want)
	if st.Enabled {
		t.Error("entry should be gone after Disable")
	}
}

// A quoted argument must come back as one argument, not several.
func TestExecRoundTrip(t *testing.T) {
	cases := [][]string{
		{"/usr/bin/weftsync", "daemon", "/home/me/job.toml"},
		{"/usr/bin/weftsync", "daemon", "/home/me/My Documents/job.toml"},
		{"/usr/bin/weftsync", "daemon", "/home/me/a$b/job.toml"},
		{"/usr/bin/weftsync", "daemon", "/home/me/a`b/job.toml"},
		{"/usr/bin/weftsync", "daemon", `/home/me/a\b/job.toml`},
		{"/usr/bin/weftsync", "daemon", "/home/me/100%/job.toml"},
		{"/usr/bin/weftsync", "daemon", "/home/me/job.toml", "--addr", "127.0.0.1:6000"},
	}
	for _, args := range cases {
		spec := Spec{Exe: args[0], Args: args[1:]}
		got := parseExec(Command(spec))
		if !equal(got, args) {
			t.Errorf("round trip failed\n  value: %s\n    got: %q\n   want: %q",
				Command(spec), got, args)
		}
	}
}

// The Exec value must be quoted where the spec requires it, and left alone
// otherwise so a hand-written entry still matches.
func TestExecQuoting(t *testing.T) {
	if got := execQuote("/opt/weftsync"); got != "/opt/weftsync" {
		t.Errorf("a plain path needs no quotes, got %s", got)
	}
	if got := execQuote("/opt/my dir/weftsync"); got != `"/opt/my dir/weftsync"` {
		t.Errorf("a path with a space must be quoted, got %s", got)
	}
	if got := execQuote("a%b"); got != "a%%b" {
		t.Errorf("a literal %% must be doubled, got %s", got)
	}
}

// A stale entry must not claim to match.
func TestReadDetectsStaleEntryLinux(t *testing.T) {
	useTempConfig(t)

	if err := Enable(Spec{Exe: "/opt/weftsync", Args: []string{"daemon", "/a.toml"}}); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	st, err := Read(Spec{Exe: "/opt/weftsync", Args: []string{"daemon", "/b.toml"}})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !st.Enabled {
		t.Fatal("the entry exists")
	}
	if st.Matches {
		t.Error("an entry pointing at different jobs must not match")
	}
}

func TestDisableWithoutEntryIsFine(t *testing.T) {
	useTempConfig(t)
	if err := Disable(); err != nil {
		t.Errorf("Disable on a missing entry should succeed: %v", err)
	}
}
