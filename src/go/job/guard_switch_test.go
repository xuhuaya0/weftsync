package job

import (
	"context"
	"io"
	"path/filepath"
	"testing"

	"weftsync/plan"
)

// The deletion guard is off unless the pair asks for it.
//
// 2026-11-18, the user's words: 「你还可以保留这个最大删除，但是可以加个开关默认关闭吗？
// 我觉得不需要啊」. Their reasoning is the one that matters here: a number chosen in
// advance cannot know what a run is about to do, and the guard's answer to "68
// deletions" was the same as its answer to "11 deletions" - stop and go read the
// config. FreeFileSync's shape - show what will be deleted, ask, let the human
// decide with the list in front of them - is a better fit for a tool one person
// runs by hand.
//
// So the default flips. What must not happen is the old behaviour silently
// surviving: a mirror pair with max_delete = 2 and twenty deletions has to *run*
// now, not abort.
func TestGuardIsOffByDefault(t *testing.T) {
	cfg, _, remote := localPair(t, VariantMirror, `
[pair.sync]
max_delete = 2
`)
	for i := 1; i <= 20; i++ {
		writeFile(t, filepath.Join(remote, "old"+string(rune('a'+i))+".txt"), "r")
	}

	res, err := runPair(t, cfg)
	if err != nil {
		t.Fatalf("with the guard off the run must proceed, got: %v", err)
	}
	if res.GuardHit {
		t.Error("no guard fired, so guard_hit must stay false")
	}
	if res.GuardEnabled {
		t.Error("guard_enabled must be false: the pair never asked for a ceiling")
	}
	if res.Actual.Deleted != 20 {
		t.Errorf("deleted = %d, want 20: the ceiling is not supposed to apply",
			res.Actual.Deleted)
	}
	if n := len(listDir(t, remote)); n != 0 {
		t.Errorf("destination should be an exact copy, still has %d file(s)", n)
	}
}

// Turning the switch on restores the old behaviour, including on a preview, so the
// plan panel can still warn before anything happens.
func TestGuardTurnsBackOnWhenAsked(t *testing.T) {
	cfg, _, remote := localPair(t, VariantMirror, `
[pair.sync]
delete_guard = true
max_delete = 2
`)
	for i := 1; i <= 5; i++ {
		writeFile(t, filepath.Join(remote, "old"+string(rune('a'+i))+".txt"), "r")
	}

	res, err := RunPair(context.Background(), cfg, &cfg.Pair[0], io.Discard,
		Options{DryRun: true, CollectPlan: true})
	if err != nil {
		t.Fatalf("a preview must report, not fail: %v", err)
	}
	if !res.GuardEnabled {
		t.Error("guard_enabled must be true when the pair asked for it")
	}
	if !res.GuardHit {
		t.Error("5 deletions against max_delete = 2 must be reported")
	}
	// The plan still lists what would happen, because that is what the reader
	// needs in order to decide whether to raise the limit.
	var deletes int
	for _, r := range res.Plan {
		if r.Action == plan.DeleteRemote {
			deletes++
		}
	}
	if deletes != 5 {
		t.Errorf("plan has %d delete rows, want 5", deletes)
	}
}

// The switch inherits like every other sync setting: a pair that says nothing
// takes the job's answer, in both directions.
func TestDeleteGuardInheritsFromTheJob(t *testing.T) {
	cfg, local, _ := localPair(t, VariantUpdate, "")
	writeFile(t, filepath.Join(local, "a.txt"), "a")

	on := true
	cfg.Job.Sync.DeleteGuard = &on
	spec, err := cfg.Pair[0].EffectiveSync(cfg)
	if err != nil {
		t.Fatalf("EffectiveSync: %v", err)
	}
	if !spec.DeleteGuard {
		t.Error("a pair with no opinion must inherit the job's delete_guard = true")
	}

	// And a pair may say no even when the job says yes.
	off := false
	cfg.Pair[0].Sync.DeleteGuard = &off
	spec, err = cfg.Pair[0].EffectiveSync(cfg)
	if err != nil {
		t.Fatalf("EffectiveSync: %v", err)
	}
	if spec.DeleteGuard {
		t.Error("a pair that says delete_guard = false must not inherit the job's true")
	}
}
