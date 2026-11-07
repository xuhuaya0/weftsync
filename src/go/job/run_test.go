package job

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	// The test binary needs the local backend registered: the "remote" side of
	// these pairs is a second local directory so the whole pipeline can be
	// exercised without a network.
	_ "github.com/rclone/rclone/backend/local"
)

// localPair builds a config whose "remote" side is just another local directory,
// so the whole guard/versioning pipeline can be exercised without a network.
func localPair(t *testing.T, variant string, syncBody string) (*Config, string, string) {
	t.Helper()
	base := t.TempDir()
	local := filepath.Join(base, "local")
	remote := filepath.Join(base, "remote")
	for _, d := range []string{local, remote} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	body := `
[job]
variant = "` + variant + `"
[[pair]]
name = "p"
[pair.local]
root = "` + filepath.ToSlash(local) + `"
[pair.remote]
root = ":local:` + filepath.ToSlash(remote) + `"
` + syncBody

	path := filepath.Join(base, "job.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write job: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg, local, remote
}

func runPair(t *testing.T, cfg *Config) (PairResult, error) {
	t.Helper()
	return RunPair(context.Background(), cfg, &cfg.Pair[0], io.Discard, Options{})
}

// The core safety property: when the pending deletions exceed the limit, the run
// stops and the destination is left completely untouched.
func TestGuardAbortsWithoutDeletingAnything(t *testing.T) {
	cfg, _, remote := localPair(t, VariantMirror, `
[pair.sync]
max_delete = 2
`)
	for i := 1; i <= 5; i++ {
		writeFile(t, filepath.Join(remote, "keep"+string(rune('0'+i))+".txt"), "remote")
	}

	before := listDir(t, remote)
	res, err := runPair(t, cfg)

	var guard *GuardError
	if !errors.As(err, &guard) {
		t.Fatalf("expected GuardError, got %v", err)
	}
	if !res.GuardHit {
		t.Error("result should record that the guard fired")
	}
	if guard.Files != 5 || guard.Limit != 2 {
		t.Errorf("guard reported %d files against limit %d, want 5 / 2", guard.Files, guard.Limit)
	}
	if !strings.Contains(guard.Error(), "NOTHING was deleted") {
		t.Errorf("message must state plainly that nothing was deleted\ngot: %s", guard.Error())
	}

	after := listDir(t, remote)
	if strings.Join(before, ",") != strings.Join(after, ",") {
		t.Errorf("destination changed despite the guard: %v -> %v", before, after)
	}
}

// At exactly the limit the run proceeds, so the boundary is not off by one.
func TestGuardAllowsExactlyAtLimit(t *testing.T) {
	cfg, _, remote := localPair(t, VariantMirror, `
[pair.sync]
max_delete = 2
`)
	writeFile(t, filepath.Join(remote, "a.txt"), "remote")
	writeFile(t, filepath.Join(remote, "b.txt"), "remote")

	res, err := runPair(t, cfg)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.GuardHit {
		t.Error("guard should not fire at exactly the limit")
	}
	if left := listDir(t, remote); len(left) != 0 {
		t.Errorf("destination should be emptied, still has %v", left)
	}
}

// max_delete = 0 means "never delete", so even a single deletion aborts.
func TestGuardZeroBlocksEveryDeletion(t *testing.T) {
	cfg, _, remote := localPair(t, VariantMirror, `
[pair.sync]
max_delete = 0
`)
	writeFile(t, filepath.Join(remote, "only.txt"), "remote")

	_, err := runPair(t, cfg)
	var guard *GuardError
	if !errors.As(err, &guard) {
		t.Fatalf("expected GuardError, got %v", err)
	}
	if exists(filepath.Join(remote, "only.txt")) == false {
		t.Error("the file must still be there")
	}
}

// update never deletes, regardless of max_delete.
func TestUpdateNeverDeletes(t *testing.T) {
	cfg, local, remote := localPair(t, VariantUpdate, "")
	writeFile(t, filepath.Join(remote, "only-remote.txt"), "keep me")
	writeFile(t, filepath.Join(local, "new.txt"), "fresh")

	if _, err := runPair(t, cfg); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !exists(filepath.Join(remote, "only-remote.txt")) {
		t.Error("update variant must never delete from the destination")
	}
	if !exists(filepath.Join(remote, "new.txt")) {
		t.Error("update variant must still upload new files")
	}
}

// Empty directories in the source are recreated (FR-014).
func TestUpdateCreatesEmptyDirectories(t *testing.T) {
	cfg, local, remote := localPair(t, VariantUpdate, "")
	if err := os.MkdirAll(filepath.Join(local, "docs", "empty"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, filepath.Join(local, "docs", "a.txt"), "x")

	if _, err := runPair(t, cfg); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !exists(filepath.Join(remote, "docs", "empty")) {
		t.Error("empty directory was not recreated")
	}
}

// deletion = versioning with naming=replace: the retired file lands under
// <dest>/_weftsync_trash/<same relative path>.
func TestVersioningReplace(t *testing.T) {
	cfg, _, remote := localPair(t, VariantMirror, `
[pair.sync]
max_delete = 10
deletion = "versioning"
versioning_dir = "_trash"
versioning_naming = "replace"
`)
	writeFile(t, filepath.Join(remote, "docs", "old.txt"), "retired")

	if _, err := runPair(t, cfg); err != nil {
		t.Fatalf("run: %v", err)
	}

	if exists(filepath.Join(remote, "docs", "old.txt")) {
		t.Error("the retired file should be gone from its original place")
	}
	trashed := filepath.Join(remote, "_trash", "docs", "old.txt")
	if !exists(trashed) {
		t.Fatalf("expected the retired file at %s; tree: %v", trashed, listTree(t, remote))
	}
	data, err := os.ReadFile(trashed)
	if err != nil {
		t.Fatalf("read trash copy: %v", err)
	}
	if string(data) != "retired" {
		t.Errorf("trash copy content = %q, want %q", data, "retired")
	}
}

// naming=timestamp-folder: one timestamped directory per run.
func TestVersioningTimestampFolder(t *testing.T) {
	cfg, _, remote := localPair(t, VariantMirror, `
[pair.sync]
max_delete = 10
deletion = "versioning"
versioning_dir = "_trash"
versioning_naming = "timestamp-folder"
`)
	writeFile(t, filepath.Join(remote, "a", "one.txt"), "1")
	writeFile(t, filepath.Join(remote, "a", "two.txt"), "2")

	if _, err := runPair(t, cfg); err != nil {
		t.Fatalf("run: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(remote, "_trash"))
	if err != nil {
		t.Fatalf("read trash dir: %v (tree: %v)", err, listTree(t, remote))
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly one timestamp folder, got %d: %v", len(entries), entries)
	}
	stampDir := filepath.Join(remote, "_trash", entries[0].Name())
	for _, name := range []string{"one.txt", "two.txt"} {
		if !exists(filepath.Join(stampDir, "a", name)) {
			t.Errorf("expected %s under the timestamp folder; tree: %v", name, listTree(t, remote))
		}
	}
}

// naming=timestamp-file: the stamp goes before the extension, like FreeFileSync.
func TestVersioningTimestampFile(t *testing.T) {
	cfg, _, remote := localPair(t, VariantMirror, `
[pair.sync]
max_delete = 10
deletion = "versioning"
versioning_dir = "_trash"
versioning_naming = "timestamp-file"
`)
	writeFile(t, filepath.Join(remote, "report.csv"), "rows")

	if _, err := runPair(t, cfg); err != nil {
		t.Fatalf("run: %v", err)
	}

	var found string
	_ = filepath.Walk(filepath.Join(remote, "_trash"), func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			found = p
		}
		return nil
	})
	if found == "" {
		t.Fatalf("no trashed file; tree: %v", listTree(t, remote))
	}
	base := filepath.Base(found)
	if !strings.HasPrefix(base, "report.") || !strings.HasSuffix(base, ".csv") {
		t.Errorf("suffix should sit between name and extension, got %q", base)
	}
	if base == "report.csv" {
		t.Error("the file was not renamed with a timestamp")
	}
}

// Deletion is permanent by default: no trash folder appears.
func TestPermanentDeletionLeavesNoTrash(t *testing.T) {
	cfg, _, remote := localPair(t, VariantMirror, `
[pair.sync]
max_delete = 10
`)
	writeFile(t, filepath.Join(remote, "gone.txt"), "x")

	if _, err := runPair(t, cfg); err != nil {
		t.Fatalf("run: %v", err)
	}
	if exists(filepath.Join(remote, DefaultVersioningDir)) {
		t.Error("permanent deletion must not create a trash folder")
	}
	if exists(filepath.Join(remote, "gone.txt")) {
		t.Error("the file should be gone")
	}
}

// The reported counts must match reality, because the guard is built on them.
func TestPassResultCountsMatchFilesystem(t *testing.T) {
	cfg, local, remote := localPair(t, VariantMirror, `
[pair.sync]
max_delete = 10
`)
	writeFile(t, filepath.Join(local, "kept.txt"), "same")
	writeFile(t, filepath.Join(remote, "kept.txt"), "same")
	writeFile(t, filepath.Join(remote, "extra1.txt"), "x")
	writeFile(t, filepath.Join(remote, "extra2.txt"), "x")
	writeFile(t, filepath.Join(local, "new.txt"), "n")

	res, err := runPair(t, cfg)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.DryRun.Deleted != 2 {
		t.Errorf("dry run counted %d deletions, want 2", res.DryRun.Deleted)
	}
	if res.Actual.Deleted != 2 {
		t.Errorf("real run deleted %d, want 2", res.Actual.Deleted)
	}
	if res.Actual.Transferred != 1 {
		t.Errorf("real run transferred %d, want 1", res.Actual.Transferred)
	}
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func listTree(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		if rel != "." {
			out = append(out, rel)
		}
		return nil
	})
	return out
}
