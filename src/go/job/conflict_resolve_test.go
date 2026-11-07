package job

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A path from an HTTP request is used both to edit the snapshot and to write a
// local file, so it must not be able to escape the pair.
func TestCleanConflictPath(t *testing.T) {
	ok := []struct{ in, want string }{
		{"a.txt", "a.txt"},
		{"sub/a.txt", "sub/a.txt"},
		{"  spaced.txt  ", "spaced.txt"},
		{`sub\a.txt`, "sub/a.txt"}, // windows separators become slashes
	}
	for _, c := range ok {
		got, err := cleanConflictPath(c.in)
		if err != nil {
			t.Errorf("%q: unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("%q normalised to %q, want %q", c.in, got, c.want)
		}
	}

	bad := []string{
		"",
		"   ",
		"/etc/passwd",
		`\windows\system32`,
		"../../etc/passwd",
		"sub/../../escape.txt",
		`sub\..\..\escape.txt`,
	}
	for _, in := range bad {
		if _, err := cleanConflictPath(in); err == nil {
			t.Errorf("%q must be refused", in)
		}
	}
}

// Windows absolute paths written with a drive letter must be refused too, and on
// Windows the separator check alone would miss "C:/x".
func TestCleanConflictPathRejectsAbsolute(t *testing.T) {
	for _, in := range []string{"C:/Windows/win.ini", "c:/x"} {
		got, err := cleanConflictPath(in)
		if err == nil {
			t.Errorf("%q was accepted as %q", in, got)
		}
	}
}

func TestFindPair(t *testing.T) {
	cfg := &Config{Pair: []Pair{{Name: "a"}, {Name: "b"}}}

	if p := findPair(cfg, "b"); p == nil || p.Name != "b" {
		t.Errorf("findPair(b) = %v", p)
	}
	if p := findPair(cfg, "missing"); p != nil {
		t.Error("an unknown name must not resolve to a pair")
	}
	// An empty name is only unambiguous when there is exactly one pair.
	if p := findPair(cfg, ""); p != nil {
		t.Error("an empty name is ambiguous with two pairs")
	}

	one := &Config{Pair: []Pair{{Name: "only"}}}
	if p := findPair(one, ""); p == nil || p.Name != "only" {
		t.Error("an empty name should resolve when there is one pair")
	}
}

// An unknown action must be refused before any remote is opened.
func TestResolveConflictRejectsUnknownAction(t *testing.T) {
	cfg, _, _ := localPair(t, VariantUpdate, "")
	err := ResolveConflict(context.Background(), cfg, "", "a.txt", "delete")
	if err == nil {
		t.Fatal("an unknown action must be refused")
	}
	if !strings.Contains(err.Error(), "push") {
		t.Errorf("the error should name the accepted actions, got: %v", err)
	}
}

// A backend without the resolve command must say so instead of appearing to work.
func TestResolveConflictUnsupportedBackend(t *testing.T) {
	cfg, local, _ := localPair(t, VariantUpdate, "")
	writeFile(t, filepath.Join(local, "a.txt"), "x")

	err := ResolveConflict(context.Background(), cfg, "", "a.txt", ActionPush)
	if err == nil {
		t.Fatal("the local backend has no resolve command, so this must fail")
	}
	// The message matters: "does not support" tells the user this is a missing
	// feature, not a broken snapshot.
	if !strings.Contains(err.Error(), "does not support") &&
		!strings.Contains(err.Error(), "cannot resolve") {
		t.Errorf("unhelpful error: %v", err)
	}
}

// The local mtime of a downloaded file must be truncated to the second.
//
// The snapshot stores whole seconds; the local filesystem keeps sub-second
// precision. Without truncation the next run compares 12:00:00.437 against
// 12:00:00 and uploads the file straight back - an infinite ping-pong.
func TestTruncateMTime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	stamp := time.Date(2026, 11, 7, 12, 0, 0, 437_000_000, time.Local)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	if err := truncateMTime(path); err != nil {
		t.Fatalf("truncateMTime: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	got := info.ModTime()
	if got.Nanosecond() != 0 {
		t.Errorf("mtime still has %d ns", got.Nanosecond())
	}
	if got.Unix() != stamp.Unix() {
		t.Errorf("mtime moved to %v, want the same second as %v", got, stamp)
	}

	// And the recorded value (whole seconds) now matches exactly.
	if info.ModTime().Unix() != stamp.Unix() {
		t.Error("the truncated mtime does not equal the value that would be recorded")
	}
}

// A missing file is an error, not a silent no-op.
func TestTruncateMTimeMissingFile(t *testing.T) {
	if err := truncateMTime(filepath.Join(t.TempDir(), "nope.txt")); err == nil {
		t.Fatal("a missing file must be reported")
	}
}
