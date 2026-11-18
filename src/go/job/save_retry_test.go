package job

import (
	"os"
	"path/filepath"
	"testing"
)

// renameWithRetry must survive the transient failures a syncing directory produces.
//
// Measured 2026-11-18 in this repository's own workspace (under OneDrive): os.Rename
// failed with "The system cannot find the file specified" and with "The process
// cannot access the file because it is being used by another process". Both succeed
// milliseconds later. Before this, one failure abandoned the save - and the failed
// press left the form showing values the file did not have, so the next press wrote
// those instead of what the user had typed.
//
// The failure itself is not reproduced here (it depends on a sync client), so what
// this covers is the contract around it: the rename really moves the bytes, leaves
// nothing behind, and a destination that cannot be replaced is still reported.
func TestRenameWithRetryMovesTheFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.tmp")
	dst := filepath.Join(dir, "a.toml")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := renameWithRetry(src, dst); err != nil {
		t.Fatalf("rename: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("content = %q, want %q", got, "hello")
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("the temporary file should be gone after a successful rename")
	}
}

// Replacing an existing file is the normal case (every save after the first), so it
// must work and the new content must win.
func TestRenameWithRetryReplacesExisting(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "a.toml")
	if err := os.WriteFile(dst, []byte("old"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	src := filepath.Join(dir, "a.tmp")
	if err := os.WriteFile(src, []byte("new"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := renameWithRetry(src, dst); err != nil {
		t.Fatalf("rename over an existing file: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "new" {
		t.Errorf("content = %q, want the new content", got)
	}
}

// A rename that cannot work must still fail rather than spin: retries are for the
// transient case, and hiding a real error behind five attempts would only delay the
// message.
func TestRenameWithRetryReportsAMissingSource(t *testing.T) {
	dir := t.TempDir()
	err := renameWithRetry(filepath.Join(dir, "nope.tmp"), filepath.Join(dir, "a.toml"))
	if err == nil {
		t.Fatal("renaming a file that does not exist must fail")
	}
}
