package job

import (
	"strings"
	"testing"
)

// Encode is a hand-rolled compact TOML writer (D33), so every field of SyncSpec
// has to be listed in it explicitly. A field that is added to the struct but not
// to the writer is dropped on the next save - silently, because the file still
// parses. That is exactly what happened when the three two-way switches were
// added: the console's save wiped them, and the only symptom was a feature that
// quietly stopped working.
//
// This test fills every field and demands the writer to reproduce each one, so
// the next field cannot be forgotten without a red test.
func TestEncodeCoversEverySyncField(t *testing.T) {
	limit := int64(7)
	cfg := &Config{
		Job: Job{Name: "j"},
		Pair: []Pair{{
			Name:    "p",
			Variant: VariantTwoWay,
			Local:   Endpoint{Root: "/tmp/l"},
			Remote: Endpoint{
				Backend: "feishu",
				Root:    "r",
			},
			Sync: SyncSpec{
				Deletion:              DeletionVersioning,
				VersioningDir:         "_trash",
				VersioningNaming:      NamingTimestampFile,
				Conflict:              ConflictKeepBoth,
				MaxDelete:             &limit,
				MaxDeleteSize:         "500M",
				PropagateLocalDelete:  true,
				PropagateRemoteDelete: true,
				PullNewFiles:          true,
				MaxPullSize:           "250M",
			},
		}},
	}

	raw, err := Encode(cfg)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	out := string(raw)

	wants := []string{
		`deletion = "versioning"`,
		`versioning_dir = "_trash"`,
		`versioning_naming = "timestamp-file"`,
		`conflict = "keep-both"`,
		`max_delete = 7`,
		`max_delete_size = "500M"`,
		`propagate_local_delete = true`,
		`propagate_remote_delete = true`,
		`pull_new_files = true`,
		`max_pull_size = "250M"`,
	}
	for _, want := range wants {
		if !strings.Contains(out, want) {
			t.Errorf("Encode dropped %q\n--- produced ---\n%s", want, out)
		}
	}

	// Round-trip: whatever it wrote must load back to the same struct. This is
	// the check that catches a writer using a key the parser does not know.
	back := mustLoad(t, out)
	got := back.Pair[0].Sync
	if got.Deletion != DeletionVersioning || got.VersioningDir != "_trash" ||
		got.VersioningNaming != NamingTimestampFile || got.Conflict != ConflictKeepBoth {
		t.Errorf("string fields did not survive: %+v", got)
	}
	if got.MaxDelete == nil || *got.MaxDelete != 7 || got.MaxDeleteSize != "500M" {
		t.Errorf("delete limits did not survive: %+v", got)
	}
	if !got.PropagateLocalDelete || !got.PropagateRemoteDelete || !got.PullNewFiles {
		t.Errorf("the two-way switches did not survive: %+v", got)
	}
	if got.MaxPullSize != "250M" {
		t.Errorf("max_pull_size = %q", got.MaxPullSize)
	}
}

// An all-default sync spec must produce no [pair.sync] section at all: a file
// full of empty sections is noise the user has to read past.
func TestEncodeOmitsAnEmptySyncSection(t *testing.T) {
	cfg := &Config{
		Job: Job{Name: "j"},
		Pair: []Pair{{
			Name:   "p",
			Local:  Endpoint{Root: "/tmp/l"},
			Remote: Endpoint{Backend: "feishu", Root: "r"},
		}},
	}
	raw, err := Encode(cfg)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if strings.Contains(string(raw), "[pair.sync]") {
		t.Errorf("an empty sync section was written\n%s", raw)
	}
}

// sameSync is the self-check Save runs after writing: if it did not know about a
// field, it would report "saved" for a file that lost it.
func TestSameSyncSeesTheTwoWayFields(t *testing.T) {
	base := SyncSpec{Deletion: DeletionPermanent}
	cases := []struct {
		name string
		mut  func(*SyncSpec)
	}{
		{"propagate_local_delete", func(s *SyncSpec) { s.PropagateLocalDelete = true }},
		{"propagate_remote_delete", func(s *SyncSpec) { s.PropagateRemoteDelete = true }},
		{"pull_new_files", func(s *SyncSpec) { s.PullNewFiles = true }},
		{"max_pull_size", func(s *SyncSpec) { s.MaxPullSize = "1G" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			modified := base
			c.mut(&modified)
			if sameSync(base, modified) {
				t.Errorf("sameSync thinks %s is unchanged; Save would not notice "+
					"the field being lost", c.name)
			}
		})
	}
}
