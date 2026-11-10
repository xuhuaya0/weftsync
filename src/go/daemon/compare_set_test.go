package daemon

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"weftsync/job"
)

// compareJob writes a job whose [pair.sync] may or may not state compare.
func compareJob(t *testing.T, dir, jobSync, pairSync string) string {
	t.Helper()
	body := `
[job]
name = "cmp"
variant = "update"
` + jobSync + `
[[pair]]
name = "cmp"
[pair.local]
root = "` + filepath.ToSlash(filepath.Join(dir, "local")) + `"
[pair.remote]
backend = "feishu"
root = "team/docs"
` + pairSync
	path := filepath.Join(dir, "cmp.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write job: %v", err)
	}
	return path
}

// Compare is the effective strategy, so a pair that inherits and a pair that
// spells out the same value look identical in the view. CompareSet is what tells
// them apart, and the dialog needs it: with only the effective value every pair
// looks like it overrides the configuration, the "跟配置" choice cannot be
// offered, and a freshly opened pair dialog cannot be told apart from one with
// real overrides.
func TestCompareSetDistinguishesInheritedFromStated(t *testing.T) {
	cases := []struct {
		name      string
		jobSync   string
		pairSync  string
		wantValue string
		wantSet   bool
	}{
		{
			name:      "pair inherits the job default",
			jobSync:   "",
			pairSync:  "",
			wantValue: job.CompareTimeSize,
			wantSet:   false,
		},
		{
			name:      "pair states the same value as the default",
			jobSync:   "",
			pairSync:  "[pair.sync]\ncompare = \"time-size\"\n",
			wantValue: job.CompareTimeSize,
			wantSet:   true,
		},
		{
			name:      "pair inherits a non-default job value",
			jobSync:   "[job.sync]\ncompare = \"size-only\"\n",
			pairSync:  "",
			wantValue: job.CompareSize,
			wantSet:   false,
		},
		{
			name:      "pair overrides a non-default job value",
			jobSync:   "[job.sync]\ncompare = \"size-only\"\n",
			pairSync:  "[pair.sync]\ncompare = \"content\"\n",
			wantValue: job.CompareContent,
			wantSet:   true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			path := compareJob(t, dir, c.jobSync, c.pairSync)
			d, err := New([]string{path}, io.Discard)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			view := getConfig(t, &API{D: d}, "cmp")
			if len(view.Pairs) != 1 {
				t.Fatalf("pairs = %d, want 1", len(view.Pairs))
			}
			got := view.Pairs[0].Sync
			if got.Compare != c.wantValue {
				t.Errorf("Compare = %q, want %q", got.Compare, c.wantValue)
			}
			if got.CompareSet != c.wantSet {
				t.Errorf("CompareSet = %v, want %v", got.CompareSet, c.wantSet)
			}
		})
	}
}

// Clearing a pair's compare must remove the line from the file, not write the
// inherited value back as if it had been chosen. Spelling it out pins the pair to
// today's default, so a later change to the job default silently misses it.
func TestClearingPairCompareRemovesTheOverride(t *testing.T) {
	dir := t.TempDir()
	path := compareJob(t, dir, "[job.sync]\ncompare = \"size-only\"\n",
		"[pair.sync]\ncompare = \"content\"\n")

	d, err := New([]string{path}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	view := getConfig(t, &API{D: d}, "cmp")
	if !view.Pairs[0].Sync.CompareSet {
		t.Fatal("precondition: the pair should start with a stated compare")
	}

	// What the console does when the user picks 跟配置.
	view.Pairs[0].Sync.Compare = ""
	if _, err := d.ApplyConfigView("cmp", view); err != nil {
		t.Fatalf("ApplyConfigView: %v", err)
	}

	reloaded := getConfig(t, &API{D: d}, "cmp")
	got := reloaded.Pairs[0].Sync
	if got.CompareSet {
		t.Errorf("CompareSet = true after clearing, want false (the override is still in the file)")
	}
	if got.Compare != job.CompareSize {
		t.Errorf("Compare = %q, want the inherited %q", got.Compare, job.CompareSize)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read job: %v", err)
	}
	if strings.Contains(string(raw), `compare = "content"`) {
		t.Errorf("the cleared override is still in the file:\n%s", raw)
	}
}
