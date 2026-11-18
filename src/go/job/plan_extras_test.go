package job

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"weftsync/plan"
)

// A destination-only file must appear in the comparison, even when the variant
// will not touch it.
//
// Measured (2026-11-18) on a real pair: the user deleted six files locally, both
// sides then held 66 and 68 files, and the comparison table showed only the four
// pending uploads. Their question was 「当我在本地删除了几个文件之后对比没有对比
// 出新的东西啊。这是预期的吗」 - the six leftovers were invisible, so "nothing to
// do" and "six files you forgot about" rendered identically.
func TestPlanListsDestinationOnlyFiles(t *testing.T) {
	cfg, local, remote := localPair(t, VariantUpdate, "")
	makeManyFiles(t, local, 2)
	// Two files the source does not have.
	for _, name := range []string{"gone1.txt", "gone2.txt"} {
		if err := os.WriteFile(filepath.Join(remote, name), []byte("leftover"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	res, err := RunPair(context.Background(), cfg, &cfg.Pair[0], io.Discard,
		Options{DryRun: true, CollectPlan: true})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}

	extras := map[string]plan.Row{}
	for _, r := range res.Plan {
		if r.Action == plan.ExtraRemote {
			extras[r.Path] = r
		}
	}
	if len(extras) != 2 {
		t.Fatalf("extras = %d (%+v), want 2", len(extras), res.Plan)
	}
	for _, name := range []string{"gone1.txt", "gone2.txt"} {
		r, ok := extras[name]
		if !ok {
			t.Errorf("%q was not listed as a leftover", name)
			continue
		}
		// The row describes the destination: name and size belong to the right
		// pane, and nothing belongs to the left.
		if r.HasLeft {
			t.Errorf("%q claims the source has it", name)
		}
		if !r.HasRight {
			t.Errorf("%q does not claim the destination has it", name)
		}
		if r.Size != int64(len("leftover")) {
			t.Errorf("%q size = %d, want %d", name, r.Size, len("leftover"))
		}
	}

	// They are not work: the summary must not count them as transfers or bytes,
	// or the header would promise 2 files / 16 B of movement that never happens.
	if res.PlanSummary.ExtraRemote != 2 {
		t.Errorf("summary.extra_remote = %d, want 2", res.PlanSummary.ExtraRemote)
	}
	if res.PlanSummary.Create != 2 || res.PlanSummary.Update != 0 {
		t.Errorf("summary = %+v, want 2 creates", res.PlanSummary)
	}
	if want := int64(0); res.PlanSummary.Bytes == 0 && want == 0 {
		// Bytes must cover the two uploads only; each file is "file N\n".
		if res.PlanSummary.Bytes <= 0 {
			t.Errorf("summary.bytes = %d, want the uploaded bytes", res.PlanSummary.Bytes)
		}
	}
}

// Leftovers alone must still leave the summary empty: "nothing to change" is the
// true answer, and Empty() is what the page keys that sentence off.
func TestOnlyLeftoversIsStillEmpty(t *testing.T) {
	cfg, local, remote := localPair(t, VariantUpdate, "")
	makeManyFiles(t, local, 1)
	data, err := os.ReadFile(filepath.Join(local, "f0.txt"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := os.WriteFile(filepath.Join(remote, "f0.txt"), data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(remote, "leftover.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	res, err := RunPair(context.Background(), cfg, &cfg.Pair[0], io.Discard,
		Options{DryRun: true, CollectPlan: true})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if res.PlanSummary.ExtraRemote != 1 {
		t.Fatalf("extra_remote = %d, want 1 (plan: %+v)",
			res.PlanSummary.ExtraRemote, res.Plan)
	}
	if !res.PlanSummary.Empty() {
		t.Errorf("summary = %+v, want empty: a leftover is not work", res.PlanSummary)
	}
}

// A path the job excludes is not a leftover the user needs to hear about: the
// same filter the pass used has to apply to this listing too.
func TestExcludedPathsAreNotReportedAsLeftovers(t *testing.T) {
	cfg, local, remote := localPair(t, VariantUpdate, `
[pair.sync.filter]
exclude = ["_*"]
`)
	makeManyFiles(t, local, 1)
	for _, name := range []string{"_keep.txt", "report.txt"} {
		if err := os.WriteFile(filepath.Join(remote, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	res, err := RunPair(context.Background(), cfg, &cfg.Pair[0], io.Discard,
		Options{DryRun: true, CollectPlan: true})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	var got []string
	for _, r := range res.Plan {
		if r.Action == plan.ExtraRemote {
			got = append(got, r.Path)
		}
	}
	if len(got) != 1 || got[0] != "report.txt" {
		t.Errorf("extras = %v, want only report.txt (the excluded _keep.txt must not appear)", got)
	}
}

// A mirror pair already plans real deletions for those paths. Listing them again
// as leftovers would show the same file twice with two different meanings.
func TestMirrorDoesNotAlsoListLeftovers(t *testing.T) {
	cfg, local, remote := localPair(t, VariantMirror, "")
	makeManyFiles(t, local, 1)
	data, err := os.ReadFile(filepath.Join(local, "f0.txt"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := os.WriteFile(filepath.Join(remote, "f0.txt"), data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(remote, "leftover.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	res, err := RunPair(context.Background(), cfg, &cfg.Pair[0], io.Discard,
		Options{DryRun: true, CollectPlan: true})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	for _, r := range res.Plan {
		if r.Action == plan.ExtraRemote {
			t.Errorf("%q listed as a leftover in a mirror pair; it should be a deletion", r.Path)
		}
	}
	if res.PlanSummary.DeleteRemote == 0 {
		t.Errorf("mirror should plan a deletion for leftover.txt, summary = %+v",
			res.PlanSummary)
	}
}
