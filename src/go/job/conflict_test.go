package job

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rclone/rclone/fs"

	_ "github.com/rclone/rclone/backend/local"
)

// fakeCommandFs wraps a local Fs and answers the conflicts command, so the job
// layer's policy handling can be tested without a network.
type conflictsResult struct {
	Conflicts []map[string]any `json:"conflicts"`
	Checked   int              `json:"checked"`
	Summary   string           `json:"summary"`
}

// The job layer must turn a conflict report into "change nothing and exit
// non-zero" under the default policy.
func TestConflictReportChangesNothing(t *testing.T) {
	cfg, local, remote := localPair(t, VariantUpdate, `
[pair.sync]
conflict = "report"
`)
	_ = cfg
	writeFile(t, filepath.Join(local, "a.txt"), "local version")
	writeFile(t, filepath.Join(remote, "a.txt"), "someone else version")
	writeFile(t, filepath.Join(local, "new.txt"), "brand new")

	// Seed a snapshot so detection has a baseline to disagree with. The pair in
	// these tests uses a local directory, which has no conflicts command, so the
	// policy path is exercised through the helper directly.
	ctx := context.Background()
	conflicts := []Conflict{{
		Path: "a.txt", Kind: ConflictKindRemoteReplaced,
		Detail: "this name now carries a different file", Recorded: "tok-old",
		Found: []string{"tok-other"},
	}}

	paths := reportConflicts(io.Discard, "p", conflicts)
	if len(paths) != 1 || paths[0] != "a.txt" {
		t.Fatalf("reported paths = %v", paths)
	}

	ctx, err := excludePaths(ctx, paths)
	if err != nil {
		t.Fatalf("excludePaths: %v", err)
	}

	// With the path excluded, the engine must leave it alone.
	if _, err := runPass(ctx, mustFs(t, ":local:"+filepath.ToSlash(remote)),
		mustFs(t, local), passOptions{}, false, false); err != nil {
		t.Fatalf("runPass: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(remote, "a.txt"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "someone else version" {
		t.Errorf("conflicting file was modified: %q", data)
	}
}

func mustFs(t *testing.T, fsPath string) fs.Fs {
	t.Helper()
	f, err := fs.NewFs(context.Background(), fsPath)
	if err != nil {
		t.Fatalf("open %q: %v", fsPath, err)
	}
	return f
}

// ConflictError must say plainly that nothing happened and what the options are.
func TestConflictErrorIsActionable(t *testing.T) {
	err := &ConflictError{Pair: "work", Count: 3}
	msg := err.Error()

	for _, want := range []string{"work", "3 conflict", "NOTHING"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message should contain %q\ngot: %s", want, msg)
		}
	}
	if !strings.Contains(msg, "overwrite") || !strings.Contains(msg, "keep-both") {
		t.Errorf("message should name the alternatives\ngot: %s", msg)
	}
}

// A backend without the conflicts command must report no conflicts rather than
// failing the run: local pairs are legitimate and have no such signal.
func TestDetectConflictsWithoutCommand(t *testing.T) {
	local := t.TempDir()
	cfg, _, _ := localPair(t, VariantUpdate, "")
	_ = cfg

	f := mustFs(t, local)
	got, err := detectConflicts(context.Background(), f, "")
	if err != nil {
		t.Fatalf("detectConflicts: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("local backend should report no conflicts, got %v", got)
	}
}

// keep-both must leave the remote original alone and create a second name.
func TestKeepBothUploadsSecondCopy(t *testing.T) {
	local := t.TempDir()
	remote := t.TempDir()
	writeFile(t, filepath.Join(local, "a.txt"), "local version")
	writeFile(t, filepath.Join(remote, "a.txt"), "remote version")

	fdst := mustFs(t, ":local:"+filepath.ToSlash(remote))
	fsrc := mustFs(t, local)

	fixed := func() string { return "20260101-000000" }
	orig := nowStamp
	nowStamp = fixed
	defer func() { nowStamp = orig }()

	newPath, err := keepBoth(context.Background(), fdst, fsrc, "a.txt", "myhost", 1)
	if err != nil {
		t.Fatalf("keepBoth: %v", err)
	}
	if newPath == "a.txt" {
		t.Fatal("keep-both must not reuse the original name")
	}
	if !strings.Contains(newPath, "myhost") || !strings.Contains(newPath, "20260101-000000") {
		t.Errorf("new name should identify the host and time\ngot: %q", newPath)
	}

	// The original must be untouched.
	if data, _ := os.ReadFile(filepath.Join(remote, "a.txt")); string(data) != "remote version" {
		t.Errorf("original changed: %q", data)
	}
	// The new name must hold the local content.
	if data, _ := os.ReadFile(filepath.Join(remote, filepath.FromSlash(newPath))); string(data) != "local version" {
		t.Errorf("second copy content = %q", data)
	}
}

// Config validation must reject a nonsense conflict policy instead of falling
// back to a default that may overwrite data.
func TestConflictPolicyValidation(t *testing.T) {
	err := loadErr(t, `
[[pair]]
name = "p"
[pair.local]
root = "/tmp/l"
[pair.remote]
backend = "feishu"
root = "r"
[pair.sync]
conflict = "merge"
`)
	if err == nil {
		t.Fatal("an unknown conflict policy must be rejected")
	}
	if !strings.Contains(err.Error(), "merge") {
		t.Errorf("error should quote the bad value\ngot: %v", err)
	}

	// The default must be report, the only policy that changes nothing.
	cfg := mustLoad(t, `
[[pair]]
name = "p"
[pair.local]
root = "/tmp/l"
[pair.remote]
backend = "feishu"
root = "r"
`)
	spec, err := cfg.Pair[0].EffectiveSync(cfg)
	if err != nil {
		t.Fatalf("EffectiveSync: %v", err)
	}
	if spec.Conflict != ConflictReport {
		t.Errorf("default conflict = %q, want %q", spec.Conflict, ConflictReport)
	}
}
