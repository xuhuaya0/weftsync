package job

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rclone/rclone/fs"
)

// The conflict source is injected because the local backend has no `conflicts`
// command: the interesting part of the two-way variant is what RunPair does with
// a report, not how the report is produced.
func withConflicts(t *testing.T, scan ConflictScan) {
	t.Helper()
	prev := detectConflictsFn
	detectConflictsFn = func(ctx context.Context, fdst fs.Fs, dir string) (ConflictScan, error) {
		return scan, nil
	}
	t.Cleanup(func() { detectConflictsFn = prev })
}

func runTwoWay(t *testing.T, cfg *Config, dryRun bool) (PairResult, error) {
	t.Helper()
	return RunPair(context.Background(), cfg, &cfg.Pair[0], io.Discard, Options{DryRun: dryRun})
}

func fingerprint(t *testing.T, path string) (int64, int64) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.ModTime().Unix(), info.Size()
}

func readText(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// The precondition that makes stage one safe: a cloud-side replacement is pulled
// down only when the local copy still matches the recorded fingerprint.
func TestTwoWayPullsCloudChange(t *testing.T) {
	cfg, local, remote := localPair(t, VariantTwoWay, "")
	writeFile(t, filepath.Join(local, "a.txt"), "local")
	writeFile(t, filepath.Join(local, "b.txt"), "local")
	writeFile(t, filepath.Join(remote, "b.txt"), "original")

	mt, sz := fingerprint(t, filepath.Join(local, "b.txt"))
	withConflicts(t, ConflictScan{
		Available: true,
		Conflicts: []Conflict{{
			Path: "b.txt", Kind: ConflictKindRemoteReplaced,
			RecordedMTime: mt, RecordedSize: sz, Found: []string{"tok-cloud"},
		}},
	})

	res, err := runTwoWay(t, cfg, false)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Pulled != 1 || res.Held != 0 {
		t.Errorf("pulled=%d held=%d, want 1/0", res.Pulled, res.Held)
	}
	if got := readText(t, filepath.Join(local, "b.txt")); got != "original" {
		t.Errorf("local copy of b.txt = %q, want the cloud content", got)
	}
	if got := readText(t, filepath.Join(remote, "a.txt")); got != "local" {
		t.Errorf("remote a.txt = %q, want the local content (the upload half must still run)", got)
	}
}

// Both sides moved: downloading would discard the local edit, so it is held.
func TestTwoWayHoldsWhenBothSidesChanged(t *testing.T) {
	cfg, local, _ := localPair(t, VariantTwoWay, "")
	writeFile(t, filepath.Join(local, "a.txt"), "edited locally")

	withConflicts(t, ConflictScan{
		Available: true,
		Conflicts: []Conflict{{
			Path: "a.txt", Kind: ConflictKindRemoteReplaced,
			// A fingerprint that cannot match the file on disk.
			RecordedMTime: 1, RecordedSize: 1, Found: []string{"tok-cloud"},
		}},
	})

	res, err := runTwoWay(t, cfg, false)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Pulled != 0 || res.Held != 1 {
		t.Errorf("pulled=%d held=%d, want 0/1", res.Pulled, res.Held)
	}
	if got := readText(t, filepath.Join(local, "a.txt")); got != "edited locally" {
		t.Errorf("the local edit must survive, got %q", got)
	}
}

// Remote deletions are reported and never propagated in stage one: Feishu has no
// API-level restore, so a wrong decision here is unrecoverable.
func TestTwoWayNeverPropagatesDeletion(t *testing.T) {
	cfg, local, remote := localPair(t, VariantTwoWay, "")
	writeFile(t, filepath.Join(local, "gone-remotely.txt"), "local")
	writeFile(t, filepath.Join(remote, "other.txt"), "remote")

	mt, sz := fingerprint(t, filepath.Join(local, "gone-remotely.txt"))
	withConflicts(t, ConflictScan{
		Available: true,
		Conflicts: []Conflict{{
			Path: "gone-remotely.txt", Kind: ConflictKindRemoteDeleted,
			RecordedMTime: mt, RecordedSize: sz,
		}},
	})

	res, err := runTwoWay(t, cfg, false)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Pulled != 0 || res.Held != 1 {
		t.Errorf("pulled=%d held=%d, want 0/1", res.Pulled, res.Held)
	}
	if !exists(filepath.Join(local, "gone-remotely.txt")) {
		t.Error("the local file must not be deleted")
	}
	if !exists(filepath.Join(remote, "other.txt")) {
		t.Error("a two-way run must not delete on the remote either")
	}
}

// A cloud-only file is not downloaded: that would let arbitrary cloud content
// write into the local disk.
func TestTwoWayDoesNotPullCloudOnlyFiles(t *testing.T) {
	cfg, local, remote := localPair(t, VariantTwoWay, "")
	writeFile(t, filepath.Join(remote, "stranger.txt"), "not ours")

	withConflicts(t, ConflictScan{Available: true})

	res, err := runTwoWay(t, cfg, false)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if exists(filepath.Join(local, "stranger.txt")) {
		t.Error("a file that only exists in the cloud must not appear locally")
	}
	if res.Pulled != 0 {
		t.Errorf("pulled = %d, want 0", res.Pulled)
	}
}

// A preview must say what it would pull and change nothing.
func TestTwoWayPreviewChangesNothing(t *testing.T) {
	cfg, local, remote := localPair(t, VariantTwoWay, "")
	writeFile(t, filepath.Join(local, "a.txt"), "local")
	writeFile(t, filepath.Join(remote, "a.txt"), "original")

	mt, sz := fingerprint(t, filepath.Join(local, "a.txt"))
	withConflicts(t, ConflictScan{
		Available: true,
		Conflicts: []Conflict{{
			Path: "a.txt", Kind: ConflictKindRemoteReplaced,
			RecordedMTime: mt, RecordedSize: sz, Found: []string{"tok-cloud"},
		}},
	})

	var out strings.Builder
	res, err := RunPair(context.Background(), cfg, &cfg.Pair[0], &out,
		Options{DryRun: true})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if res.Pulled != 1 {
		t.Errorf("a preview should report the pull it would do, got %d", res.Pulled)
	}
	if got := readText(t, filepath.Join(local, "a.txt")); got != "local" {
		t.Errorf("a preview must not download, local = %q", got)
	}
	if !strings.Contains(out.String(), "would download") {
		t.Errorf("the preview should say what it would download\n%s", out.String())
	}
}

// A two-way pair with no snapshot cannot tell cloud changes from anything else,
// so it must fall back to plain upload and say so rather than fail.
func TestTwoWayWithoutStateFallsBackToUpload(t *testing.T) {
	cfg, local, remote := localPair(t, VariantTwoWay, "")
	writeFile(t, filepath.Join(local, "a.txt"), "local")

	withConflicts(t, ConflictScan{Available: false, Summary: "no state_file"})

	var out strings.Builder
	if _, err := RunPair(context.Background(), cfg, &cfg.Pair[0], &out, Options{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := readText(t, filepath.Join(remote, "a.txt")); got != "local" {
		t.Errorf("remote a.txt = %q, want the upload", got)
	}
	if !strings.Contains(out.String(), "two-way needs a state_file") {
		t.Errorf("the run should explain why it could only upload\n%s", out.String())
	}
}
