package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// A config file holding a secret must not be readable by anybody else.
//
// This is the whole of the Linux credential protection, so it needs a test of its
// own rather than being inferred from "the code calls chmod".
func TestRestrictToOwnerLocksTheFileDown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rclone.conf")
	if err := os.WriteFile(path, []byte("[r]\napp_secret = x\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := restrictToOwner(path); err != nil {
		t.Fatalf("restrictToOwner: %v", err)
	}

	if runtime.GOOS == "windows" {
		// Windows ignores the unix mode; access is governed by the ACL of the
		// profile directory. Asserting 0600 here would be asserting a fiction.
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if info.IsDir() {
			t.Error("the file disappeared")
		}
		return
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode = %o, want 600", mode)
	}
}

// It must also work when the file is already 0600: the early return exists so a
// save does not needlessly touch the file, and getting it wrong would chmod on
// every save.
func TestRestrictToOwnerIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rclone.conf")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := restrictToOwner(path); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := restrictToOwner(path); err != nil {
		t.Fatalf("second call: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if runtime.GOOS != "windows" {
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Errorf("mode = %o after two calls, want 600", mode)
		}
	}
}

// A missing file is an error rather than a silent success: reporting "saved" for
// a file that is not there would be the worst outcome.
func TestRestrictToOwnerReportsAMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.conf")
	if err := restrictToOwner(path); err == nil {
		t.Fatal("a missing file should be reported")
	}
}
