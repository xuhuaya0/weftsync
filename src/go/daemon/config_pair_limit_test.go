package daemon

import (
	"io"
	"os"
	"testing"

	"weftsync/job"
)

// Setting a pair-level max_delete in the console must reach the file AND the
// running daemon.
//
// 2026-11-18: a mirror pair was refused by the delete guard (68 deletions against
// max_delete = 10). The card told the reader to raise the limit in 閰嶇疆琛ㄥ崟, so
// that path has to work - and while testing it by hand the symptom was "the file
// says 30, the next comparison still says 5".
//
// Two separate ways to get that wrong, so both are asserted here: the value not
// reaching the file (buildConfig dropping it) and the value not reaching the entry
// (reload not happening after a save).
func TestConfigSaveAppliesPairMaxDelete(t *testing.T) {
	dir := t.TempDir()
	jobPath := cfgJob(t, dir, "j", "")

	d, err := New([]string{jobPath}, io.Discard)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	raw, err := os.ReadFile(jobPath)
	if err != nil {
		t.Fatalf("read job: %v", err)
	}
	cfg, err := job.Load(jobPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	v := ViewConfig(&entry{cfg: cfg, name: "j", file: jobPath}, raw)

	// The form's normal state: inherited from the job default.
	if v.Pairs[0].Sync.MaxDeleteSet {
		t.Fatalf("fixture starts with an explicit pair limit (%d); "+
			"the inheritance case is covered by another test",
			v.Pairs[0].Sync.MaxDelete)
	}

	v.Pairs[0].Sync.MaxDelete = 30
	v.Pairs[0].Sync.MaxDeleteSet = true
	if _, err := d.ApplyConfigView("j", v); err != nil {
		t.Fatalf("ApplyConfigView: %v", err)
	}

	// 1. It has to be in the file, or the next daemon start loses it.
	reloaded, err := job.Load(jobPath)
	if err != nil {
		t.Fatalf("reload from disk: %v", err)
	}
	if reloaded.Pair[0].Sync.MaxDelete == nil {
		t.Fatal("the pair-level max_delete was not written to the file")
	}
	if got := *reloaded.Pair[0].Sync.MaxDelete; got != 30 {
		t.Errorf("file has pair max_delete = %d, want 30", got)
	}

	// 2. And in the entry, or the very next comparison still uses the old limit.
	live, err := d.ConfigOf("j")
	if err != nil {
		t.Fatalf("ConfigOf: %v", err)
	}
	spec, err := live.Pair[0].EffectiveSync(live)
	if err != nil {
		t.Fatalf("EffectiveSync: %v", err)
	}
	if spec.MaxDelete != 30 {
		t.Errorf("the daemon still computes max_delete = %d, want 30: "+
			"a saved limit that the next run does not use is worse than a refused save",
			spec.MaxDelete)
	}
}
