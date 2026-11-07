//go:build windows

package autostart

import (
	"strings"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// useTempKey points the package at a throwaway registry key so the test never
// touches the machine's real autostart list, and removes it afterwards.
func useTempKey(t *testing.T) {
	t.Helper()
	oldPath, oldValue := runKeyPath, runValue
	runKeyPath = `Software\weftsync-test\Run`
	runValue = "weftsync-test"
	t.Cleanup(func() {
		runKeyPath, runValue = oldPath, oldValue
		// DeleteKey only removes an empty key, so the leaf must go first;
		// skipping that leaves a stray \Run behind on every test run.
		_ = registry.DeleteKey(registry.CURRENT_USER, `Software\weftsync-test\Run`)
		_ = registry.DeleteKey(registry.CURRENT_USER, `Software\weftsync-test`)
	})
	// Start from a clean slate even if a previous run crashed.
	_ = Disable()
}

// Enable must write something Status can read back and match.
func TestEnableReadDisable(t *testing.T) {
	useTempKey(t)

	want := Spec{
		Exe:  `C:\Program Files\weftsync\weftsync.exe`,
		Args: []string{"daemon", `C:\Users\me\My Documents\job.toml`},
	}

	st, err := Read(want)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if st.Enabled {
		t.Fatal("nothing should be registered yet")
	}
	if !st.Supported {
		t.Error("windows must report supported=true")
	}

	if err := Enable(want); err != nil {
		t.Fatalf("Enable: %v", err)
	}

	st, err = Read(want)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !st.Enabled {
		t.Fatal("entry should exist after Enable")
	}
	if !st.Matches {
		t.Errorf("entry should match the spec it was written from\n got: %s\nwant: %s",
			st.Command, Command(want))
	}

	if err := Disable(); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	st, err = Read(want)
	if err != nil {
		t.Fatalf("Read after Disable: %v", err)
	}
	if st.Enabled {
		t.Error("entry should be gone after Disable")
	}
}

// A path with spaces must survive. An unquoted entry is the classic way to get
// an autostart item that silently does nothing.
func TestCommandQuotesPathsWithSpaces(t *testing.T) {
	got := Command(Spec{
		Exe:  `C:\Program Files\weftsync\weftsync.exe`,
		Args: []string{"daemon", `C:\Jobs\night ly.toml`},
	})
	if !strings.Contains(got, `"C:\Program Files\weftsync\weftsync.exe"`) {
		t.Errorf("the executable must be quoted: %s", got)
	}
	if !strings.Contains(got, `"C:\Jobs\night ly.toml"`) {
		t.Errorf("an argument with a space must be quoted: %s", got)
	}
	// A path with no space does not need quotes, and adding them would make the
	// comparison with a hand-edited entry fail for no reason.
	if strings.Contains(got, `"daemon"`) {
		t.Errorf("a plain argument should not be quoted: %s", got)
	}
}

// An entry written earlier for different job files must not report Matches:
// that is what the UI turns into a "the entry is stale" warning.
func TestReadDetectsStaleEntry(t *testing.T) {
	useTempKey(t)

	old := Spec{Exe: `C:\tools\weftsync.exe`, Args: []string{"daemon", `C:\a.toml`}}
	if err := Enable(old); err != nil {
		t.Fatalf("Enable: %v", err)
	}

	now := Spec{Exe: `C:\tools\weftsync.exe`, Args: []string{"daemon", `C:\b.toml`}}
	st, err := Read(now)
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

// Disabling something that is not there is the desired state, not a failure.
func TestDisableWithoutEntryIsFine(t *testing.T) {
	useTempKey(t)
	if err := Disable(); err != nil {
		t.Errorf("Disable on an empty key should succeed: %v", err)
	}
}

func TestEnableRejectsEmptyExe(t *testing.T) {
	useTempKey(t)
	if err := Enable(Spec{}); err == nil {
		t.Fatal("an empty executable must be refused")
	}
}
