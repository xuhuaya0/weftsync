package job

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Stage two adds three propagation switches, all off by default. The tests here
// pin two properties for each: it does nothing when off, and it does exactly one
// thing when on.

// withBaseline stubs the backend resolve command so a stage-two delete can drop
// the snapshot entry without a Feishu backend.
func withBaseline(t *testing.T) *[]string {
	t.Helper()
	forgotten := &[]string{}
	prev := resolveFn
	resolveFn = func(ctx context.Context, p *Pair, path string, opt map[string]string) error {
		if opt["action"] == "forget" {
			*forgotten = append(*forgotten, path)
		}
		return nil
	}
	t.Cleanup(func() { resolveFn = prev })
	return forgotten
}

// --- A: local deletion propagates to the cloud ------------------------------

// Off by default: a file missing locally must not cost the cloud its copy.
func TestLocalDeleteNotPropagatedByDefault(t *testing.T) {
	cfg, local, remote := localPair(t, VariantTwoWay, "")
	writeFile(t, filepath.Join(remote, "gone.txt"), "cloud copy")

	// The cloud still holds exactly what the snapshot recorded, and the local
	// file is absent - the exact situation a propagation switch acts on.
	withConflicts(t, ConflictScan{
		Available: true,
		Matched: []Conflict{{
			Path: "gone.txt", Kind: ConflictKindUnchanged,
			Found: []string{"tok-1"},
		}},
	})
	forgotten := withBaseline(t)

	res, err := RunPair(context.Background(), cfg, &cfg.Pair[0], io.Discard, Options{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.LocalDeleted != 0 {
		t.Errorf("LocalDeleted = %d, want 0 with the switch off", res.LocalDeleted)
	}
	if !exists(filepath.Join(remote, "gone.txt")) {
		t.Error("the cloud copy was deleted although the switch is off")
	}
	if len(*forgotten) != 0 {
		t.Errorf("the baseline was dropped for %v; nothing should have happened", *forgotten)
	}
	_ = local
}

// On: the cloud copy goes and the baseline goes with it, so the next run does not
// see the same deletion again.
func TestLocalDeletePropagatesWhenEnabled(t *testing.T) {
	cfg, _, remote := localPair(t, VariantTwoWay, `
[pair.sync]
propagate_local_delete = true
`)
	writeFile(t, filepath.Join(remote, "gone.txt"), "cloud copy")

	withConflicts(t, ConflictScan{
		Available: true,
		Matched: []Conflict{{
			Path: "gone.txt", Kind: ConflictKindUnchanged,
			Found: []string{"tok-1"},
		}},
	})
	forgotten := withBaseline(t)

	res, err := RunPair(context.Background(), cfg, &cfg.Pair[0], io.Discard, Options{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.LocalDeleted != 1 {
		t.Errorf("LocalDeleted = %d, want 1", res.LocalDeleted)
	}
	if exists(filepath.Join(remote, "gone.txt")) {
		t.Error("the cloud copy is still there")
	}
	if len(*forgotten) != 1 || (*forgotten)[0] != "gone.txt" {
		t.Errorf("forgotten = %v, want [gone.txt]", *forgotten)
	}
}

// A file that is still present locally must never be treated as deleted, however
// the switch is set.
func TestLocalDeleteLeavesPresentFilesAlone(t *testing.T) {
	cfg, local, remote := localPair(t, VariantTwoWay, `
[pair.sync]
propagate_local_delete = true
`)
	writeFile(t, filepath.Join(local, "here.txt"), "still here")
	writeFile(t, filepath.Join(remote, "here.txt"), "cloud copy")

	withConflicts(t, ConflictScan{
		Available: true,
		Matched: []Conflict{{
			Path: "here.txt", Kind: ConflictKindUnchanged,
			Found: []string{"tok-1"},
		}},
	})
	withBaseline(t)

	res, err := RunPair(context.Background(), cfg, &cfg.Pair[0], io.Discard, Options{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.LocalDeleted != 0 {
		t.Errorf("LocalDeleted = %d, want 0 for a file that exists", res.LocalDeleted)
	}
	if !exists(filepath.Join(remote, "here.txt")) {
		t.Error("a present local file cost the cloud its copy")
	}
}

// --- B: cloud deletion propagates to the local disk ------------------------

func TestRemoteDeleteNotPropagatedByDefault(t *testing.T) {
	cfg, local, _ := localPair(t, VariantTwoWay, "")
	writeFile(t, filepath.Join(local, "gone.txt"), "local copy")

	mt, sz := fingerprint(t, filepath.Join(local, "gone.txt"))
	withConflicts(t, ConflictScan{
		Available: true,
		Conflicts: []Conflict{{
			Path: "gone.txt", Kind: ConflictKindRemoteDeleted,
			RecordedMTime: mt, RecordedSize: sz,
		}},
	})

	res, err := RunPair(context.Background(), cfg, &cfg.Pair[0], io.Discard, Options{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.RemoteDeleted != 0 {
		t.Errorf("RemoteDeleted = %d, want 0 with the switch off", res.RemoteDeleted)
	}
	if !exists(filepath.Join(local, "gone.txt")) {
		t.Error("the local file was deleted although the switch is off")
	}
}

func TestRemoteDeletePropagatesWhenEnabled(t *testing.T) {
	cfg, local, _ := localPair(t, VariantTwoWay, `
[pair.sync]
propagate_remote_delete = true
`)
	writeFile(t, filepath.Join(local, "gone.txt"), "local copy")

	mt, sz := fingerprint(t, filepath.Join(local, "gone.txt"))
	withConflicts(t, ConflictScan{
		Available: true,
		Conflicts: []Conflict{{
			Path: "gone.txt", Kind: ConflictKindRemoteDeleted,
			RecordedMTime: mt, RecordedSize: sz,
		}},
	})
	forgotten := withBaseline(t)

	res, err := RunPair(context.Background(), cfg, &cfg.Pair[0], io.Discard, Options{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.RemoteDeleted != 1 {
		t.Errorf("RemoteDeleted = %d, want 1", res.RemoteDeleted)
	}
	if exists(filepath.Join(local, "gone.txt")) {
		t.Error("the local file is still there")
	}
	if len(*forgotten) != 1 {
		t.Errorf("the baseline was not dropped: %v", *forgotten)
	}
}

// The one that protects real work: a local file edited AFTER the cloud copy
// vanished must not be deleted. Deleting it throws away the newer content.
func TestRemoteDeleteKeepsLocallyEditedFiles(t *testing.T) {
	cfg, local, _ := localPair(t, VariantTwoWay, `
[pair.sync]
propagate_remote_delete = true
`)
	writeFile(t, filepath.Join(local, "edited.txt"), "new local work")

	withConflicts(t, ConflictScan{
		Available: true,
		Conflicts: []Conflict{{
			Path: "edited.txt", Kind: ConflictKindRemoteDeleted,
			// Fingerprint deliberately does not match the file on disk.
			RecordedMTime: 1, RecordedSize: 1,
		}},
	})
	withBaseline(t)

	var out strings.Builder
	res, err := RunPair(context.Background(), cfg, &cfg.Pair[0], &out, Options{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.RemoteDeleted != 0 {
		t.Errorf("RemoteDeleted = %d, want 0 for a locally changed file", res.RemoteDeleted)
	}
	if !exists(filepath.Join(local, "edited.txt")) {
		t.Fatal("a locally edited file was deleted - that is user work")
	}
	if !strings.Contains(out.String(), "changed locally") {
		t.Errorf("the run should explain why it kept the file\n%s", out.String())
	}
}

// --- C: cloud-added files ------------------------------------------------

func TestPullNewFilesNotDoneByDefault(t *testing.T) {
	cfg, local, remote := localPair(t, VariantTwoWay, "")
	writeFile(t, filepath.Join(remote, "new.txt"), "someone put this here")

	withConflicts(t, ConflictScan{
		Available: true,
		RemoteAdded: []Conflict{{
			Path: "new.txt", Kind: ConflictKindRemoteAdded, Found: []string{"tok-new"},
		}},
	})

	res, err := RunPair(context.Background(), cfg, &cfg.Pair[0], io.Discard, Options{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.NewPulled != 0 {
		t.Errorf("NewPulled = %d, want 0 with the switch off", res.NewPulled)
	}
	if exists(filepath.Join(local, "new.txt")) {
		t.Error("a cloud-only file was downloaded although the switch is off")
	}
}

func TestPullNewFilesDownloadsWhenEnabled(t *testing.T) {
	cfg, local, remote := localPair(t, VariantTwoWay, `
[pair.sync]
pull_new_files = true
`)
	writeFile(t, filepath.Join(remote, "new.txt"), "someone put this here")

	withConflicts(t, ConflictScan{
		Available: true,
		RemoteAdded: []Conflict{{
			Path: "new.txt", Kind: ConflictKindRemoteAdded, Found: []string{"tok-new"},
		}},
	})
	withBaseline(t)

	res, err := RunPair(context.Background(), cfg, &cfg.Pair[0], io.Discard, Options{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.NewPulled != 1 {
		t.Errorf("NewPulled = %d, want 1", res.NewPulled)
	}
	if got := readText(t, filepath.Join(local, "new.txt")); got != "someone put this here" {
		t.Errorf("local content = %q", got)
	}
}

// A preview must describe the same decisions without performing any.
func TestStageTwoPreviewChangesNothing(t *testing.T) {
	cfg, local, remote := localPair(t, VariantTwoWay, `
[pair.sync]
propagate_local_delete = true
pull_new_files = true
`)
	writeFile(t, filepath.Join(remote, "localdeleted.txt"), "cloud copy")
	writeFile(t, filepath.Join(remote, "added.txt"), "cloud only")

	withConflicts(t, ConflictScan{
		Available: true,
		Matched: []Conflict{{
			Path: "localdeleted.txt", Kind: ConflictKindUnchanged, Found: []string{"tok-1"},
		}},
		RemoteAdded: []Conflict{{
			Path: "added.txt", Kind: ConflictKindRemoteAdded, Found: []string{"tok-2"},
		}},
	})
	forgotten := withBaseline(t)

	var out strings.Builder
	res, err := RunPair(context.Background(), cfg, &cfg.Pair[0], &out, Options{DryRun: true})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if res.LocalDeleted != 1 || res.NewPulled != 1 {
		t.Errorf("preview should report 1 delete and 1 download, got %d/%d",
			res.LocalDeleted, res.NewPulled)
	}
	if !exists(filepath.Join(remote, "localdeleted.txt")) {
		t.Error("a preview deleted a cloud file")
	}
	if exists(filepath.Join(local, "added.txt")) {
		t.Error("a preview downloaded a file")
	}
	if len(*forgotten) != 0 {
		t.Errorf("a preview dropped baselines: %v", *forgotten)
	}
	if !strings.Contains(out.String(), "would delete in cloud") ||
		!strings.Contains(out.String(), "would download new file") {
		t.Errorf("the preview should describe both actions\n%s", out.String())
	}
}

// --- the switches are meaningless outside two-way -------------------------

func TestStageTwoSwitchesRejectedOnOtherVariants(t *testing.T) {
	for _, v := range []string{VariantUpdate, VariantMirror} {
		t.Run(v, func(t *testing.T) {
			err := loadErr(t, `
[job]
variant = "`+v+`"

[[pair]]
name = "p"
[pair.local]
root = "/tmp/l"
[pair.remote]
backend = "feishu"
root = "r"
[pair.sync]
propagate_remote_delete = true
`)
			if err == nil {
				t.Fatal("a two-way switch on a one-way pair must be refused, " +
					"not silently ignored")
			}
			if !strings.Contains(err.Error(), "two-way") {
				t.Errorf("the error should name the variant it needs\ngot: %v", err)
			}
		})
	}
}

func TestMaxPullSizeIsValidated(t *testing.T) {
	base := `
[[pair]]
name = "p"
variant = "two-way"
[pair.local]
root = "/tmp/l"
[pair.remote]
backend = "feishu"
root = "r"
[pair.sync]
pull_new_files = true
`
	if err := loadErr(t, base+`max_pull_size = "500M"`+"\n"); err != nil {
		t.Fatalf("a valid size should be accepted: %v", err)
	}
	if err := loadErr(t, base+"max_pull_size = \"500 megabytes\"\n"); err == nil {
		t.Error("a non-size max_pull_size should be refused at load")
	}
	if err := loadErr(t, base+"max_pull_size = \"0\"\n"); err == nil {
		t.Error("max_pull_size = 0 would mean 'download nothing', which is what " +
			"leaving the switch off already does; it should be refused")
	}
}

// deleteCloudCopy must refuse when the folder no longer holds exactly the copy the
// report described: deleting by name would take whichever copy the backend picked.
func TestDeleteCloudCopyRefusesExtraCopies(t *testing.T) {
	cfg, _, remote := localPair(t, VariantTwoWay, "")
	writeFile(t, filepath.Join(remote, "a.txt"), "x")

	err := deleteCloudCopy(context.Background(), &cfg.Pair[0], "a.txt", []string{"tok-1", "tok-2"})
	if err == nil {
		t.Fatal("two tokens means the situation changed; nothing should be deleted")
	}
	if !strings.Contains(err.Error(), "2 copies") {
		t.Errorf("the error should say why\ngot: %v", err)
	}
	if !exists(filepath.Join(remote, "a.txt")) {
		t.Error("the file was deleted anyway")
	}
}

// A local file that is a directory must not be removed as if it were a file.
func TestRemoteDeleteSkipsDirectories(t *testing.T) {
	cfg, local, _ := localPair(t, VariantTwoWay, `
[pair.sync]
propagate_remote_delete = true
`)
	sub := filepath.Join(local, "dir.txt")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	mt, sz := fingerprint(t, sub)
	withConflicts(t, ConflictScan{
		Available: true,
		Conflicts: []Conflict{{
			Path: "dir.txt", Kind: ConflictKindRemoteDeleted,
			RecordedMTime: mt, RecordedSize: sz,
		}},
	})
	withBaseline(t)

	res, err := RunPair(context.Background(), cfg, &cfg.Pair[0], io.Discard, Options{})
	if err != nil {
		var pathErr *os.PathError
		if errors.As(err, &pathErr) {
			t.Fatalf("run failed on a directory instead of skipping it: %v", err)
		}
		t.Fatalf("run: %v", err)
	}
	if res.RemoteDeleted != 0 {
		t.Errorf("RemoteDeleted = %d, want 0 for a directory", res.RemoteDeleted)
	}
	if !exists(sub) {
		t.Error("the directory was removed")
	}
}
