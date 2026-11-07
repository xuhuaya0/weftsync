package split

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// makeEntries builds n files with varied extensions and names.
func makeEntries(n int) []Entry {
	out := make([]Entry, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, Entry{
			Name:    fmt.Sprintf("file-%04d.txt", i),
			Size:    int64(i),
			ModTime: time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC),
		})
	}
	return out
}

// Every schema must produce a plan whose buckets respect the limit, and must move
// every entry exactly once. A plan that drops or duplicates a move would lose or
// duplicate a file.
func TestEverySchemeCoversAllEntriesAndRespectsLimit(t *testing.T) {
	entries := makeEntries(1632)

	for _, scheme := range Schemes() {
		t.Run(string(scheme), func(t *testing.T) {
			plan, err := BuildPlan("reports", entries, scheme, Options{MaxPerBucket: 1400})
			if err != nil {
				t.Fatalf("BuildPlan: %v", err)
			}

			if plan.Total() != len(entries) {
				t.Errorf("moves = %d, want %d", plan.Total(), len(entries))
			}

			seenFrom := map[string]bool{}
			perBucket := map[string]int{}
			for _, m := range plan.Moves {
				if seenFrom[m.From] {
					t.Errorf("duplicate move from %q", m.From)
				}
				seenFrom[m.From] = true
				if !strings.HasPrefix(m.To, "reports/") {
					t.Errorf("move target %q is not under the split directory", m.To)
				}
				perBucket[parentOf(m.To)]++
			}
			if len(seenFrom) != len(entries) {
				t.Errorf("distinct sources = %d, want %d", len(seenFrom), len(entries))
			}

			for dir, n := range perBucket {
				if n > 1400 {
					t.Errorf("bucket %q holds %d entries, over the 1400 limit", dir, n)
				}
			}

			// The declared bucket counts must match the moves, otherwise the UI
			// shows a number that is not what will happen.
			declared := 0
			for _, b := range plan.Buckets {
				declared += b.Files
			}
			if declared != len(entries) {
				t.Errorf("buckets declare %d files, want %d", declared, len(entries))
			}
		})
	}
}

// The count scheme at the default 1400 must produce exactly two parts for 1632.
func TestSchemeCountSplitsEvenly(t *testing.T) {
	plan, err := BuildPlan("", makeEntries(1632), SchemeCount, Options{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.Buckets) != 2 {
		t.Fatalf("buckets = %d, want 2: %+v", len(plan.Buckets), plan.Buckets)
	}
	// Sorted by name, so part-01 is the first 1400 after sorting by name.
	if plan.Buckets[0].Files != 1400 || plan.Buckets[1].Files != 232 {
		t.Errorf("bucket sizes = %d/%d, want 1400/232",
			plan.Buckets[0].Files, plan.Buckets[1].Files)
	}
}

// The type scheme groups by extension and keeps the extension in the name.
func TestSchemeTypeGroupsByExtension(t *testing.T) {
	entries := []Entry{
		{Name: "a.xlsx"}, {Name: "b.XLSX"}, {Name: "c.pdf"}, {Name: "noext"},
		{Name: "sub", IsDir: true},
	}
	plan, err := BuildPlan("docs", entries, SchemeType, Options{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	got := map[string]int{}
	for _, b := range plan.Buckets {
		got[b.Name] = b.Files
	}
	want := map[string]int{"_xlsx": 2, "_pdf": 1, "_noext": 1, "_folders": 1}
	for name, n := range want {
		if got[name] != n {
			t.Errorf("bucket %q = %d, want %d (all: %v)", name, got[name], n, got)
		}
	}
	// Case should not produce two buckets.
	if _, ok := got["_XLSX"]; ok {
		t.Error("extension case produced a separate bucket")
	}
}

// A directory must never be grouped by its "extension".
func TestSchemeTypePutsDirectoriesInTheirOwnBucket(t *testing.T) {
	entries := []Entry{{Name: "photos.png", IsDir: true}, {Name: "a.png"}}
	plan, err := BuildPlan("", entries, SchemeType, Options{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	for _, m := range plan.Moves {
		if m.From == "photos.png" && !strings.HasPrefix(m.To, "_folders/") {
			t.Errorf("a directory went to %q", m.To)
		}
	}
}

// Initial buckets follow the documented ranges.
func TestSchemeInitialRanges(t *testing.T) {
	entries := []Entry{
		{Name: "apple.txt"}, {Name: "Banana.txt"}, {Name: "zebra.txt"},
		{Name: "0start.txt"}, {Name: "中文.txt"},
	}
	plan, err := BuildPlan("", entries, SchemeInitial, Options{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	got := map[string]int{}
	for _, b := range plan.Buckets {
		got[b.Name] = b.Files
	}
	for name, n := range map[string]int{
		"A-H": 2, "Q-Z": 1, "0-9": 1, "_other": 1,
	} {
		if got[name] != n {
			t.Errorf("bucket %q = %d, want %d (all: %v)", name, got[name], n, got)
		}
	}
}

// Year buckets come from the modification time, and a zero time is not guessed.
func TestSchemeYearUsesModTime(t *testing.T) {
	entries := []Entry{
		{Name: "a", ModTime: time.Date(2023, 5, 1, 0, 0, 0, 0, time.UTC)},
		{Name: "b", ModTime: time.Date(2024, 1, 9, 0, 0, 0, 0, time.UTC)},
		{Name: "c", ModTime: time.Date(2024, 12, 9, 0, 0, 0, 0, time.UTC)},
		{Name: "d"}, // zero time
	}
	plan, err := BuildPlan("", entries, SchemeYear, Options{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	got := map[string]int{}
	for _, b := range plan.Buckets {
		got[b.Name] = b.Files
	}
	for name, n := range map[string]int{"2023": 1, "2024": 2, "_unknown-date": 1} {
		if got[name] != n {
			t.Errorf("bucket %q = %d, want %d (all: %v)", name, got[name], n, got)
		}
	}
}

// A bucket that is still too large after grouping is divided again, and the plan
// says so. Emitting it unchanged would produce a split that fails partway.
func TestOversizedBucketIsDivided(t *testing.T) {
	entries := make([]Entry, 0, 3000)
	for i := 0; i < 3000; i++ {
		entries = append(entries, Entry{Name: fmt.Sprintf("f%04d.xlsx", i)})
	}
	plan, err := BuildPlan("", entries, SchemeType, Options{MaxPerBucket: 1400})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	for _, b := range plan.Buckets {
		if b.Files > 1400 {
			t.Errorf("bucket %q = %d entries, over the limit", b.Name, b.Files)
		}
	}
	if len(plan.Buckets) != 3 {
		t.Errorf("buckets = %d, want 3 (1400+1400+200): %+v", len(plan.Buckets), plan.Buckets)
	}
	// One note, not one per resulting bucket: the same sentence twice reads as a bug
	// in the page.
	if len(plan.Notes) != 1 {
		t.Errorf("notes = %d, want 1: %q", len(plan.Notes), plan.Notes)
	}
}

// A new directory whose name already exists would be merged into, or overwrite a
// file. That has to be refused, not discovered halfway through the moves.
func TestNameCollisionIsRefused(t *testing.T) {
	entries := []Entry{{Name: "_txt", IsDir: true}, {Name: "a.txt"}}
	if _, err := BuildPlan("", entries, SchemeType, Options{}); err == nil {
		t.Fatal("a bucket name colliding with an existing entry must be refused")
	}
}

func TestUnknownSchemeIsRefused(t *testing.T) {
	if _, err := BuildPlan("", makeEntries(1), Scheme("nonsense"), Options{}); err == nil {
		t.Fatal("an unknown scheme must be refused")
	}
}

// The plan must be deterministic: the snapshot and the tests depend on it.
func TestPlanIsDeterministic(t *testing.T) {
	entries := makeEntries(50)
	a, err := BuildPlan("d", entries, SchemeType, Options{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	// Same input in a different order must give the same plan.
	reversed := make([]Entry, len(entries))
	for i := range entries {
		reversed[i] = entries[len(entries)-1-i]
	}
	b, err := BuildPlan("d", reversed, SchemeType, Options{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(a.Moves) != len(b.Moves) {
		t.Fatalf("move counts differ: %d vs %d", len(a.Moves), len(b.Moves))
	}
	for i := range a.Moves {
		if a.Moves[i] != b.Moves[i] {
			t.Errorf("move %d differs:\n %+v\n %+v", i, a.Moves[i], b.Moves[i])
		}
	}
}

// Bucket names must survive as directory names.
func TestBucketNamesAreSafe(t *testing.T) {
	entries := []Entry{
		{Name: "a.weird:name"}, {Name: "b.tar.gz"}, {Name: "c."}, {Name: ".hidden"},
	}
	plan, err := BuildPlan("", entries, SchemeType, Options{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	for _, b := range plan.Buckets {
		if strings.ContainsAny(b.Name, `:/\*?"<>| `) {
			t.Errorf("bucket name %q contains a character that is not safe in a path", b.Name)
		}
	}
}

// Total and Bytes summarise the plan for the UI.
func TestPlanTotals(t *testing.T) {
	entries := []Entry{{Name: "a.txt", Size: 10}, {Name: "b.txt", Size: 32}}
	plan, err := BuildPlan("", entries, SchemeType, Options{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if plan.Total() != 2 {
		t.Errorf("Total = %d", plan.Total())
	}
	if plan.Bytes() != 42 {
		t.Errorf("Bytes = %d, want 42", plan.Bytes())
	}
}

// An empty directory is not a candidate for splitting.
func TestEmptyPlan(t *testing.T) {
	plan, err := BuildPlan("", nil, SchemeType, Options{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if plan.Total() != 0 || len(plan.Buckets) != 0 {
		t.Errorf("plan = %+v, want empty", plan)
	}
}
