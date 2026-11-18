package job

import (
	"context"
	"io"
	"path/filepath"
	"testing"

	"weftsync/plan"
)

// A preview the guard would refuse has to publish the limit it compared against.
//
// 2026-11-18: a pair was switched to "mirror" while its local root held five files
// and the cloud folder held sixty-eight. The plan was right and the guard was right
// to refuse, but the card could only say "over max_delete" - neither number, no
// statement that pressing the button would do nothing, and no hint where the limit
// lives. The user's question was 「这是为什么」, which is what that card should have
// answered.
func TestPreviewPublishesTheGuardLimit(t *testing.T) {
	cfg, local, remote := localPair(t, VariantMirror, `
[pair.sync]
delete_guard = true
max_delete = 2
`)
	// Five files only on the destination: a mirror would delete them all.
	for i := 1; i <= 5; i++ {
		writeFile(t, filepath.Join(remote, "remote"+string(rune('0'+i))+".txt"), "r")
	}
	writeFile(t, filepath.Join(local, "local1.txt"), "l")

	res, err := RunPair(context.Background(), cfg, &cfg.Pair[0], io.Discard,
		Options{DryRun: true, CollectPlan: true})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if !res.GuardHit {
		t.Fatalf("the preview should report that a run would be refused")
	}
	if res.MaxDelete != 2 {
		t.Errorf("max_delete = %d, want 2: the console cannot name the limit otherwise",
			res.MaxDelete)
	}

	// The plan itself must still be complete: it is the only thing that says what
	// would happen if the limit were raised.
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

// The limit is published on ordinary previews too, so the console does not have to
// special-case the moment the guard fires.
func TestPreviewPublishesTheLimitEvenWhenItDoesNotFire(t *testing.T) {
	cfg, local, _ := localPair(t, VariantUpdate, `
[pair.sync]
delete_guard = true
max_delete = 7
`)
	writeFile(t, filepath.Join(local, "a.txt"), "a")

	res, err := RunPair(context.Background(), cfg, &cfg.Pair[0], io.Discard,
		Options{DryRun: true, CollectPlan: true})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if res.GuardHit {
		t.Fatal("nothing is being deleted, so the guard must not fire")
	}
	if res.MaxDelete != 7 {
		t.Errorf("max_delete = %d, want 7", res.MaxDelete)
	}
}

// A pair that never mentions max_delete still resolves one - the documented
// default - so the console can show what turning the guard on would enforce.
//
// The switch is what decides whether it applies, and it starts off (D103).
func TestDefaultLimitIsPublished(t *testing.T) {
	cfg, local, _ := localPair(t, VariantUpdate, "")
	writeFile(t, filepath.Join(local, "a.txt"), "a")

	res, err := RunPair(context.Background(), cfg, &cfg.Pair[0], io.Discard,
		Options{DryRun: true, CollectPlan: true})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if res.MaxDelete != DefaultMaxDelete {
		t.Errorf("max_delete = %d, want the default %d", res.MaxDelete, DefaultMaxDelete)
	}
	if res.GuardEnabled {
		t.Error("the guard must be off unless the pair asks for it")
	}
}
