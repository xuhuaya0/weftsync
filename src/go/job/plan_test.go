package job

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"weftsync/plan"
)

// The plan has to agree with the run it describes. That is the whole point of
// reading it back out of rclone's dry-run log instead of comparing the two sides
// ourselves: a preview that disagrees with what follows it teaches the user to
// stop reading previews.
//
// This is the end-to-end check of that property: a real dry pass over a real
// directory, then the row count against the counters from the same pass.
func TestPlanAgreesWithTheDryPass(t *testing.T) {
	cfg, local, _ := localPair(t, VariantUpdate, "")
	makeManyFiles(t, local, 7)

	res, err := RunPair(context.Background(), cfg, &cfg.Pair[0], io.Discard,
		Options{DryRun: true, CollectPlan: true})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}

	if res.DryRun.Transferred != 7 {
		t.Fatalf("transferred = %d, want 7", res.DryRun.Transferred)
	}

	// Count only file copies: rclone also logs "make directory" skips, which are
	// rows of their own (and useful), so they must not be folded into the file
	// count.
	var copies, dirs int
	for _, r := range res.Plan {
		switch r.Action {
		case plan.Create, plan.Update:
			copies++
		case plan.Mkdir:
			dirs++
		}
	}
	if int64(copies) != res.DryRun.Transferred {
		t.Fatalf("plan has %d copy rows but the pass reported %d transfers "+
			"(plan rows: %+v)", copies, res.DryRun.Transferred, res.Plan)
	}
	if dirs != 0 {
		// Documents actual behaviour rather than assuming: a local destination
		// needs no directory creation here.
		t.Logf("note: %d directory rows", dirs)
	}

	// Every planned path must be a real local file, or the list would send the
	// user looking for something that is not there.
	for _, r := range res.Plan {
		if r.Action != plan.Create && r.Action != plan.Update {
			continue
		}
		if _, err := os.Stat(filepath.Join(local, filepath.FromSlash(r.Path))); err != nil {
			t.Errorf("planned path %q is not on disk: %v", r.Path, err)
		}
	}

	// The summary must match the rows it summarises.
	if got := res.PlanSummary.Create + res.PlanSummary.Update; got != copies {
		t.Errorf("summary says %d copies, rows say %d", got, copies)
	}
}

// Nothing to do must produce an empty plan and say so, so the page can show
// "already in sync" instead of an empty table that looks broken.
func TestPlanIsEmptyWhenNothingToDo(t *testing.T) {
	cfg, local, remote := localPair(t, VariantUpdate, "")
	makeManyFiles(t, local, 2)
	// Pre-populate the destination with identical files so the pass has no work.
	for i := 0; i < 2; i++ {
		name := "f" + string(rune('0'+i)) + ".txt"
		data, err := os.ReadFile(filepath.Join(local, name))
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if err := os.WriteFile(filepath.Join(remote, name), data, 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	res, err := RunPair(context.Background(), cfg, &cfg.Pair[0], io.Discard,
		Options{DryRun: true, CollectPlan: true})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(res.Plan) != 0 {
		// Identical content with a different mtime makes rclone plan a
		// modification-time update, which is a real (if minor) action. What must
		// hold is that no file content would move, and that the summary says so.
		for _, r := range res.Plan {
			if r.Action != plan.Touch {
				t.Fatalf("plan = %+v, want only metadata rows for an already-synced pair", res.Plan)
			}
		}
	}
	if !res.PlanSummary.Empty() {
		t.Errorf("summary = %+v, want empty: no content changes", res.PlanSummary)
	}
}

// Without CollectPlan the field stays empty. The command line prints human text
// and must not pay for a row list nobody reads.
func TestPlanIsNotCollectedUnlessAsked(t *testing.T) {
	cfg, local, _ := localPair(t, VariantUpdate, "")
	makeManyFiles(t, local, 3)

	res, err := RunPair(context.Background(), cfg, &cfg.Pair[0], io.Discard,
		Options{DryRun: true})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(res.Plan) != 0 {
		t.Fatalf("plan = %+v, want none without CollectPlan", res.Plan)
	}
}

// A real (non-preview) run must not collect either: the rows come from dry-run
// logging, and a real run has none.
func TestPlanIsNotCollectedForARealRun(t *testing.T) {
	cfg, local, _ := localPair(t, VariantUpdate, "")
	makeManyFiles(t, local, 3)

	res, err := RunPair(context.Background(), cfg, &cfg.Pair[0], io.Discard,
		Options{DryRun: false, CollectPlan: true})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Actual.Transferred != 3 {
		t.Fatalf("transferred = %d, want 3", res.Actual.Transferred)
	}
	if len(res.Plan) != 0 {
		t.Fatalf("plan = %+v, want none for a real run", res.Plan)
	}
}
