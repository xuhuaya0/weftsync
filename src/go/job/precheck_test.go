package job

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"weftsync/split"
)

// makeManyFiles writes n files into dir.
func makeManyFiles(t *testing.T, dir string, n int) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for i := 0; i < n; i++ {
		name := filepath.Join(dir, "f"+itoa(i)+".txt")
		if err := os.WriteFile(name, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var digits []byte
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	return string(digits)
}

// A directory over the limit must stop the run before anything is uploaded, with an
// error that names the directory. Feishu only complains on the create/upload call,
// which is halfway through the run.
func TestOverLimitDirectoryRefusesTheRun(t *testing.T) {
	cfg, local, remote := localPair(t, VariantUpdate, "")
	makeManyFiles(t, local, 8)

	// A low threshold keeps the test quick; the behaviour is the same at 1400.
	opt := Options{MaxNodesPerDir: 5}
	res, err := RunPair(context.Background(), cfg, &cfg.Pair[0], io.Discard, opt)
	if err == nil {
		t.Fatal("an over-limit directory must stop the run")
	}

	var limitErr *split.OverLimitError
	if !errors.As(err, &limitErr) {
		t.Fatalf("the error should be a split.OverLimitError, got %T: %v", err, err)
	}
	if len(res.OverLimit) != 1 {
		t.Errorf("OverLimit = %v, want the one offending directory", res.OverLimit)
	}
	if !strings.Contains(err.Error(), "1400") && !strings.Contains(err.Error(), "5") {
		t.Errorf("the error should name the limit, got: %v", err)
	}

	// Nothing may have been uploaded: the remote directory stays empty.
	entries, readErr := os.ReadDir(remote)
	if readErr != nil {
		t.Fatalf("read remote: %v", readErr)
	}
	if len(entries) != 0 {
		t.Errorf("the run uploaded %d entries despite refusing", len(entries))
	}
}

// A preview must not fail on an over-limit directory: its job is to report what a
// real run would do, and the answer is "be refused". The finding has to reach the
// caller either way, so the plan panel and the error agree.
func TestOverLimitIsReportedByPreviewWithoutFailing(t *testing.T) {
	cfg, local, _ := localPair(t, VariantUpdate, "")
	makeManyFiles(t, local, 8)

	res, err := RunPair(context.Background(), cfg, &cfg.Pair[0], io.Discard,
		Options{DryRun: true, MaxNodesPerDir: 5})
	if err != nil {
		t.Fatalf("a preview must not fail on an over-limit directory: %v", err)
	}
	if len(res.OverLimit) != 1 {
		t.Errorf("OverLimit = %v, want the offending directory reported", res.OverLimit)
	}
}

// A directory at the threshold is fine; only strictly over it is refused.
//
// The boundary matters: 1400 entries is the recommended maximum, so refusing 1400
// would make the documented advice unusable.
func TestDirectoryAtLimitIsAllowed(t *testing.T) {
	cfg, local, _ := localPair(t, VariantUpdate, "")
	makeManyFiles(t, local, 5)

	if _, err := RunPair(context.Background(), cfg, &cfg.Pair[0], io.Discard,
		Options{DryRun: true, MaxNodesPerDir: 5}); err != nil {
		t.Fatalf("a directory exactly at the limit must be allowed: %v", err)
	}
}

// A nested over-limit directory must be found too, not only the root.
func TestNestedOverLimitDirectoryIsFound(t *testing.T) {
	cfg, local, _ := localPair(t, VariantUpdate, "")
	deep := filepath.Join(local, "a", "b")
	makeManyFiles(t, deep, 8)

	res, err := RunPair(context.Background(), cfg, &cfg.Pair[0], io.Discard,
		Options{DryRun: true, MaxNodesPerDir: 5})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(res.OverLimit) != 1 || !strings.Contains(res.OverLimit[0], "b") {
		t.Errorf("OverLimit = %v, want the nested directory", res.OverLimit)
	}
}

// The versioning folder is excluded from sync, so counting it would refuse a run
// over files that are never uploaded.
func TestVersioningFolderIsNotCounted(t *testing.T) {
	cfg, local, _ := localPair(t, VariantUpdate, "")
	makeManyFiles(t, local, 2)
	makeManyFiles(t, filepath.Join(local, "_weftsync_trash"), 20)

	if _, err := RunPair(context.Background(), cfg, &cfg.Pair[0], io.Discard,
		Options{DryRun: true, MaxNodesPerDir: 5}); err != nil {
		t.Fatalf("the trash folder must not count towards the limit: %v", err)
	}
}

// An unreadable tree must be reported as a failure, not silently treated as
// within limits: that would send the run into the very error this check prevents.
func TestPrecheckFailureIsReported(t *testing.T) {
	if _, err := split.Precheck(filepath.Join(t.TempDir(), "does-not-exist"),
		split.PrecheckOptions{}); err == nil {
		t.Fatal("a missing root must be an error")
	}
}
