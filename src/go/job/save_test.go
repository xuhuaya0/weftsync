package job

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRaw(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

const plainJobFile = `[job]
name = "nightly"
variant = "mirror"
[job.sync]
max_delete = 50

[[pair]]
name = "docs"
[pair.local]
root = "/tmp/docs"
[pair.remote]
backend = "feishu"
root = "team/docs"
`

// A file with no comments is safe to rewrite: blank-line and ordering
// differences carry nothing the user wrote to be read later.
func TestHasCommentsCleanFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.toml")
	writeRaw(t, path, plainJobFile)

	raw, _ := os.ReadFile(path)
	if HasComments(raw) {
		t.Error("a comment-free file should not report comments")
	}
}

// Comments are the one thing the structured form cannot hold, so they must be
// detected rather than silently dropped.
func TestHasCommentsDetectsComments(t *testing.T) {
	cases := map[string]string{
		"leading":      "# this explains max_delete\n" + plainJobFile,
		"indented":     plainJobFile + "\n   # trailing note\n",
		"inline-block": plainJobFile + "\n# end\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if !HasComments([]byte(body)) {
				t.Errorf("comments were not detected in:\n%s", body)
			}
		})
	}
}

// A '#' inside a quoted value is not a comment.
func TestHasCommentsIgnoresHashInValue(t *testing.T) {
	body := strings.Replace(plainJobFile, `root = "/tmp/docs"`, `root = "/tmp/we#ird"`, 1)
	if HasComments([]byte(body)) {
		t.Error("a '#' inside a value was mistaken for a comment")
	}
}

// Without force, saving over a commented file must refuse rather than silently
// delete the user's notes.
func TestSaveRefusesToDropComments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.toml")
	body := "# why this is low: the folder holds 40 files\n" + plainJobFile
	writeRaw(t, path, body)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	_, err = Save(cfg, path, SaveOptions{})
	if !errors.Is(err, ErrLossyRewrite) {
		t.Fatalf("error = %v, want ErrLossyRewrite", err)
	}

	// The file must be untouched.
	after, _ := os.ReadFile(path)
	if string(after) != body {
		t.Error("the file changed even though the save was refused")
	}
}

// With force the save proceeds, and the original is preserved as .bak.
func TestSaveForceKeepsBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.toml")
	body := "# a comment worth keeping somewhere\n" + plainJobFile
	writeRaw(t, path, body)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Change something so the write is meaningful.
	cfg.Job.Sync.MaxDelete = int64Ptr(7)

	res, err := Save(cfg, path, SaveOptions{Force: true})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !res.Lossy {
		t.Error("result should be marked lossy")
	}
	if res.BackupPath == "" {
		t.Fatal("no backup path reported")
	}

	bak, err := os.ReadFile(res.BackupPath)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(bak) != body {
		t.Error("the backup does not hold the original content")
	}

	// The new file must load back with the change applied.
	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Job.Sync.MaxDelete == nil || *reloaded.Job.Sync.MaxDelete != 7 {
		t.Errorf("max_delete = %v, want 7", reloaded.Job.Sync.MaxDelete)
	}
}

func int64Ptr(v int64) *int64 { return &v }

// A clean file is written without ceremony and still loads.
func TestSaveCleanFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.toml")
	writeRaw(t, path, plainJobFile)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.Job.Variant = VariantUpdate

	res, err := Save(cfg, path, SaveOptions{})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if res.Lossy {
		t.Error("a clean file should not be reported as lossy")
	}

	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Job.Variant != VariantUpdate {
		t.Errorf("variant = %q", reloaded.Job.Variant)
	}
}

// A config that would not survive its own validation must be refused BEFORE the
// file is touched. A saved-but-invalid job file breaks the next unattended run.
func TestSaveRefusesInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "job.toml")
	writeRaw(t, path, plainJobFile)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Break it: no local root.
	cfg.Pair[0].Local.Root = ""

	_, err = Save(cfg, path, SaveOptions{})
	if err == nil {
		t.Fatal("an invalid config must be refused")
	}
	if !strings.Contains(err.Error(), "not valid") {
		t.Errorf("error should say the config is invalid\ngot: %v", err)
	}

	after, _ := os.ReadFile(path)
	if string(after) != plainJobFile {
		t.Error("the file was modified despite the validation failure")
	}
}

// A bad schedule must be refused at save time. It used to be accepted because
// validate() only checked pairs, so the UI could write a file the daemon then
// refused to start on.
func TestSaveRefusesBadSchedule(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.toml")
	writeRaw(t, path, plainJobFile)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.Job.Schedule = ScheduleSpec{Mode: ScheduleInterval, Interval: "5m", StartAt: "10m"}

	_, err = Save(cfg, path, SaveOptions{Force: true})
	if err == nil {
		t.Fatal("a schedule with a malformed start_at must be refused")
	}
	if !strings.Contains(err.Error(), "HH:MM") {
		t.Errorf("error should mention HH:MM\ngot: %v", err)
	}

	after, _ := os.ReadFile(path)
	if string(after) != plainJobFile {
		t.Error("the file was modified despite the validation failure")
	}

	// The file was left alone, so its original (valid) content must still load.
	if _, err := Load(path); err != nil {
		t.Errorf("the untouched file should still load: %v", err)
	}
}

// A schedule that cannot resolve must make Load fail too, so a hand-edited file
// with a bad schedule is caught before a run starts rather than at daemon boot.
func TestLoadRefusesBadSchedule(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.toml")
	writeRaw(t, path, `[job]
name = "nightly"
[job.schedule]
mode = "interval"
interval = "5m"
start_at = "25:99"

[[pair]]
name = "docs"
[pair.local]
root = "/tmp/docs"
[pair.remote]
backend = "feishu"
root = "team/docs"
`)

	if _, err := Load(path); err == nil {
		t.Fatal("Load must reject an out-of-range start_at")
	}
}

// A nonsense safety setting must be refused too; validation runs the same checks
// as Load.
func TestSaveRefusesUnknownVariant(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.toml")
	writeRaw(t, path, plainJobFile)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.Job.Variant = "mirorr"

	if _, err := Save(cfg, path, SaveOptions{}); err == nil {
		t.Fatal("a mistyped variant must be refused")
	}
}

// Saving a brand new file works and produces something Load accepts.
func TestSaveNewFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "job.toml")

	cfg := NewConfig("fresh")
	cfg.Pair = []Pair{{
		Name:   "p",
		Local:  Endpoint{Root: "/tmp/local"},
		Remote: Endpoint{Backend: "feishu", Root: "docs"},
	}}

	res, err := Save(cfg, path, SaveOptions{})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if res.BackupPath != "" {
		t.Errorf("a new file should have no backup, got %q", res.BackupPath)
	}
	if res.Bytes == 0 {
		t.Error("nothing was written")
	}

	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Job.Name != "fresh" || len(reloaded.Pair) != 1 {
		t.Errorf("reloaded = %+v", reloaded)
	}
}

// No temp file may survive a successful save, otherwise the next Load would find
// a stray .tmp sitting next to the real file.
func TestSaveLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "job.toml")
	writeRaw(t, path, plainJobFile)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := Save(cfg, path, SaveOptions{}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}
