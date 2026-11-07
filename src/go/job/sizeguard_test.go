package job

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

// The byte guard exists because file counts are a poor proxy for damage: five
// 1 KiB files and one 5 GiB file are both "five deletions". rclone counts the
// bytes but exposes no reader for the total, so the guard borrows rclone's own
// threshold on the dry pass and reports "over the limit" without an exact number.
func TestSizeGuardBlocksTheRun(t *testing.T) {
	cfg, _, remote := localPair(t, VariantMirror, `
[pair.sync]
max_delete = 50
max_delete_size = "1k"
`)

	// Four 1 KiB files: over the 1k ceiling in total, but only four deletions,
	// so the count guard would let them through.
	for i := 0; i < 4; i++ {
		writeFile(t, filepath.Join(remote, "big"+itoa(i)+".bin"), strings.Repeat("x", 1024))
	}
	before := listDir(t, remote)

	res, err := runPair(t, cfg)
	if err == nil {
		t.Fatal("the byte guard must stop the run")
	}
	var sizeGuard *SizeGuardError
	if !errors.As(err, &sizeGuard) {
		t.Fatalf("expected SizeGuardError, got %T: %v", err, err)
	}
	if !res.GuardHit {
		t.Error("result should record that a guard fired")
	}
	if sizeGuard.Limit != 1024 {
		t.Errorf("guard limit = %d, want 1024", sizeGuard.Limit)
	}
	if !strings.Contains(err.Error(), "NOTHING was deleted") {
		t.Errorf("message must state plainly that nothing was deleted\ngot: %v", err)
	}

	after := listDir(t, remote)
	if strings.Join(before, ",") != strings.Join(after, ",") {
		t.Errorf("destination changed despite the guard: %v -> %v", before, after)
	}
}

// Below the ceiling the run proceeds and really deletes, so the guard is not a
// blanket refusal.
func TestSizeGuardAllowsSmallDeletions(t *testing.T) {
	cfg, _, remote := localPair(t, VariantMirror, `
[pair.sync]
max_delete = 50
max_delete_size = "1M"
`)
	writeFile(t, filepath.Join(remote, "a.bin"), strings.Repeat("x", 1024))

	res, err := runPair(t, cfg)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.GuardHit {
		t.Error("guard should not fire below the ceiling")
	}
	if left := listDir(t, remote); len(left) != 0 {
		t.Errorf("the file should be gone, still have %v", left)
	}
}

// A preview reports the guard instead of failing: the plan panel has to be able
// to say "a real run would be refused".
func TestSizeGuardIsReportedByPreview(t *testing.T) {
	cfg, _, remote := localPair(t, VariantMirror, `
[pair.sync]
max_delete_size = "1k"
`)
	for i := 0; i < 4; i++ {
		writeFile(t, filepath.Join(remote, "big"+itoa(i)+".bin"), strings.Repeat("x", 1024))
	}

	res, err := RunPair(context.Background(), cfg, &cfg.Pair[0], io.Discard, Options{DryRun: true})
	if err != nil {
		t.Fatalf("a preview must report, not fail: %v", err)
	}
	if !res.GuardHit {
		t.Error("a preview should still record that the guard would fire")
	}
	if n := len(listDir(t, remote)); n != 4 {
		t.Errorf("a preview must not delete anything, remote has %d files", n)
	}
}

// badSizeIsRejectedHereToo guards against the parser being lenient in a way that
// turns a typo into "no limit".
func TestSizeGuardRejectsGarbage(t *testing.T) {
	if err := loadErr(t, `
[[pair]]
name = "p"
[pair.local]
root = "/tmp/l"
[pair.remote]
backend = "feishu"
root = "r"
[pair.sync]
max_delete_size = "10 feet"
`); err == nil {
		t.Fatal("a size that is not a size must be rejected")
	} else if !strings.Contains(err.Error(), "max_delete_size") {
		t.Errorf("the error should name the field, got: %v", err)
	}
}
