package job

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// The daemon's global exclude list is appended to every pair's own exclude list.
// rclone's rule set is first-match-wins (fs/filter/rules.go: include() returns on
// the first matching rule), and run.go adds the include patterns before any
// exclude. So the question "does a job's own whitelist beat a global exclude?"
// is decided by insertion order, not by intent, and the settings page has to say
// something about it.
//
// This test pins the real answer so the wording cannot drift away from it.
func TestGlobalExcludeVsPairInclude(t *testing.T) {
	cfg, local, remote := localPair(t, VariantUpdate, `
[pair.sync.filter]
include = ["*.xlsx"]
`)
	makeFiles(t, local, map[string]string{
		"a.xlsx": "xlsx",
		"b.csv":  "csv",
	})

	res, err := RunPair(context.Background(), cfg, &cfg.Pair[0], io.Discard,
		Options{Exclude: []string{"*.xlsx"}})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	_, xlsxErr := os.Stat(filepath.Join(remote, "a.xlsx"))
	_, csvErr := os.Stat(filepath.Join(remote, "b.csv"))

	// Measured: the pair's include is added first, so it wins and the file still
	// travels. Recorded here because it is the opposite of "global settings are
	// the authority", which is the intuitive reading of the settings page.
	t.Logf("transferred=%d a.xlsx=%v b.csv=%v", res.Actual.Transferred, xlsxErr == nil, csvErr == nil)

	if res.Actual.Transferred != 1 || xlsxErr != nil {
		t.Fatalf("pair include should beat the global exclude: transferred=%d xlsxErr=%v",
			res.Actual.Transferred, xlsxErr)
	}
	if csvErr == nil {
		t.Error("the include whitelist still has to drop b.csv")
	}
}

// The direction that does hold: a global exclude removes files that no whitelist
// mentions. Otherwise the whole setting would be decoration.
func TestGlobalExcludeAppliesToEveryPair(t *testing.T) {
	cfg, local, remote := localPair(t, VariantUpdate, "")
	makeFiles(t, local, map[string]string{
		"keep.txt":    "keep",
		"scratch.tmp": "skip",
	})

	res, err := RunPair(context.Background(), cfg, &cfg.Pair[0], io.Discard,
		Options{Exclude: []string{"*.tmp"}})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Actual.Transferred != 1 {
		t.Fatalf("transferred = %d, want only keep.txt", res.Actual.Transferred)
	}
	if _, err := os.Stat(filepath.Join(remote, "scratch.tmp")); err == nil {
		t.Error("the global exclude did not reach the pass")
	}
}
