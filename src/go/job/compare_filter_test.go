package job

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// R22/R23: the four things a sync session is made of - paths, filter, compare,
// sync action - each configurable at the job level and overridden per pair.
//
// These tests exist because only half of them existed: filters were global and
// exclude-only, and the comparison strategy did not exist at all. The user's
// question ("these eight things, are they all the same?") is the specification.

// makeFiles writes several files into dir from a name->content map.
func makeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		writeFile(t, filepath.Join(dir, name), content)
	}
}

// touchFile pushes a file's modification time forward, so a rewrite is visible to
// a timestamp comparison even on a filesystem with coarse time resolution.
func touchFile(t *testing.T, path string) {
	t.Helper()
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

// TestCompareStrategyResolves pins the three strategies and the precedence.
func TestCompareStrategyResolves(t *testing.T) {
	cfg, _, _ := localPair(t, VariantUpdate, "")
	spec, err := cfg.Pair[0].EffectiveSync(cfg)
	if err != nil {
		t.Fatalf("EffectiveSync: %v", err)
	}
	if spec.Compare != CompareTimeSize {
		t.Fatalf("default compare = %q, want %q", spec.Compare, CompareTimeSize)
	}

	for _, want := range CompareStrategies {
		cfg, _, _ := localPair(t, VariantUpdate, "\n[pair.sync]\ncompare = \""+want+"\"\n")
		got, err := cfg.Pair[0].EffectiveSync(cfg)
		if err != nil {
			t.Fatalf("compare=%s: %v", want, err)
		}
		if got.Compare != want {
			t.Errorf("compare = %q, want %q", got.Compare, want)
		}
	}
}

// A typo must be rejected at load time, not silently become the default: the
// difference between "compares content" and "compares timestamps" is hours of
// downloading, and finding out by surprise is the wrong way to find out.
func TestUnknownCompareIsRejected(t *testing.T) {
	base := t.TempDir()
	body := `
[job]
[[pair]]
name = "p"
[pair.local]
root = "` + filepath.ToSlash(base) + `"
[pair.remote]
root = ":local:` + filepath.ToSlash(base) + `"
[pair.sync]
compare = "contents"
`
	path := filepath.Join(base, "job.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("a misspelled compare must be rejected")
	}
	if !strings.Contains(err.Error(), "compare") {
		t.Fatalf("the error should name the setting, got: %v", err)
	}
}

// Filters merge job level first, then the pair's own, for both lists.
func TestFiltersMergeJobAndPair(t *testing.T) {
	cfg, _, _ := localPair(t, VariantUpdate, `
[pair.sync.filter]
include = ["*.xlsx", "*.csv"]
exclude = ["_*"]
`)
	// Add a job-level filter on top.
	cfg.Job.Sync.Filter.Include = []string{"reports/**"}
	cfg.Job.Sync.Filter.Exclude = []string{"*.tmp"}

	spec, err := cfg.Pair[0].EffectiveSync(cfg)
	if err != nil {
		t.Fatalf("EffectiveSync: %v", err)
	}

	wantInclude := []string{"reports/**", "*.xlsx", "*.csv"}
	if len(spec.Include) != len(wantInclude) {
		t.Fatalf("include = %v, want %v", spec.Include, wantInclude)
	}
	for i := range wantInclude {
		if spec.Include[i] != wantInclude[i] {
			t.Fatalf("include = %v, want %v", spec.Include, wantInclude)
		}
	}

	wantExclude := []string{"*.tmp", "_*"}
	if len(spec.Exclude) != len(wantExclude) || spec.Exclude[0] != wantExclude[0] ||
		spec.Exclude[1] != wantExclude[1] {
		t.Fatalf("exclude = %v, want %v", spec.Exclude, wantExclude)
	}
}

// A blank line in a filter list is a mistake, and it must not silently become a
// pattern that matches nothing (or everything).
func TestEmptyFilterPatternIsRejected(t *testing.T) {
	base := t.TempDir()
	body := `
[job]
[[pair]]
name = "p"
[pair.local]
root = "` + filepath.ToSlash(base) + `"
[pair.remote]
root = ":local:` + filepath.ToSlash(base) + `"
[pair.sync.filter]
exclude = ["*.tmp", "   "]
`
	path := filepath.Join(base, "job.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("a blank filter pattern must be rejected")
	}
}

// The filters have to reach the pass, otherwise they are decoration. An include
// whitelist is the sharpest test: nothing outside it may be transferred.
func TestIncludeFilterLimitsWhatIsTransferred(t *testing.T) {
	cfg, local, remote := localPair(t, VariantUpdate, `
[pair.sync.filter]
include = ["*.xlsx"]
`)
	makeFiles(t, local, map[string]string{
		"a.xlsx": "xlsx one",
		"b.csv":  "csv should not travel",
		"c.txt":  "txt should not travel",
	})

	res, err := RunPair(context.Background(), cfg, &cfg.Pair[0], io.Discard, Options{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Actual.Transferred != 1 {
		t.Fatalf("transferred = %d, want only the .xlsx", res.Actual.Transferred)
	}
	if _, err := os.Stat(filepath.Join(remote, "a.xlsx")); err != nil {
		t.Errorf("the whitelisted file did not arrive: %v", err)
	}
	for _, name := range []string{"b.csv", "c.txt"} {
		if _, err := os.Stat(filepath.Join(remote, name)); err == nil {
			t.Errorf("%s was transferred despite the include list", name)
		}
	}
}

// A pair-level exclude is a real removal from the sync set, in both the preview
// and the run - the same pattern must not behave differently in the two.
func TestPairExcludeAppliesToBothPasses(t *testing.T) {
	cfg, local, remote := localPair(t, VariantUpdate, `
[pair.sync.filter]
exclude = ["*.tmp"]
`)
	makeFiles(t, local, map[string]string{
		"keep.txt":    "keep",
		"scratch.tmp": "skip",
	})

	preview, err := RunPair(context.Background(), cfg, &cfg.Pair[0], io.Discard,
		Options{DryRun: true, CollectPlan: true})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	for _, row := range preview.Plan {
		if strings.HasSuffix(row.Path, ".tmp") {
			t.Errorf("the preview planned a file the filter excludes: %s", row.Path)
		}
	}

	if _, err := RunPair(context.Background(), cfg, &cfg.Pair[0], io.Discard, Options{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(remote, "keep.txt")); err != nil {
		t.Errorf("the kept file did not arrive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(remote, "scratch.tmp")); err == nil {
		t.Error("the excluded file was transferred")
	}
}

// size-only is a real change of decision: a file whose content changed but whose
// size did not must be left alone. That is what the strategy promises, and it is
// also why it is not the default.
func TestSizeOnlyIgnoresTimestampAndSizeMatchWins(t *testing.T) {
	cfg, local, remote := localPair(t, VariantUpdate, `
[pair.sync]
compare = "size-only"
`)
	// Same size, different content: with time-size this would re-upload, with
	// size-only it must not.
	makeFiles(t, local, map[string]string{"f.txt": "AAAA"})
	if _, err := RunPair(context.Background(), cfg, &cfg.Pair[0], io.Discard, Options{}); err != nil {
		t.Fatalf("first run: %v", err)
	}
	// Change the local content but keep the length, and bump the mtime.
	doc := filepath.Join(local, "f.txt")
	if err := os.WriteFile(doc, []byte("BBBB"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	touchFile(t, doc)

	res, err := RunPair(context.Background(), cfg, &cfg.Pair[0], io.Discard, Options{})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if res.Actual.Transferred != 0 {
		t.Fatalf("transferred = %d, want 0: size-only treats same-size as unchanged",
			res.Actual.Transferred)
	}
	body, err := os.ReadFile(filepath.Join(remote, "f.txt"))
	if err != nil {
		t.Fatalf("read remote: %v", err)
	}
	if string(body) != "AAAA" {
		t.Fatalf("remote content = %q, want the untouched original", body)
	}
}

// And the default strategy must still notice the change, or size-only would be
// indistinguishable from the default and the setting would mean nothing.
func TestTimeSizeNoticesASameSizeChange(t *testing.T) {
	cfg, local, remote := localPair(t, VariantUpdate, "")
	makeFiles(t, local, map[string]string{"f.txt": "AAAA"})
	if _, err := RunPair(context.Background(), cfg, &cfg.Pair[0], io.Discard, Options{}); err != nil {
		t.Fatalf("first run: %v", err)
	}

	doc := filepath.Join(local, "f.txt")
	if err := os.WriteFile(doc, []byte("BBBB"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	touchFile(t, doc)

	res, err := RunPair(context.Background(), cfg, &cfg.Pair[0], io.Discard, Options{})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if res.Actual.Transferred != 1 {
		t.Fatalf("transferred = %d, want 1: the default strategy compares timestamps",
			res.Actual.Transferred)
	}
	body, err := os.ReadFile(filepath.Join(remote, "f.txt"))
	if err != nil {
		t.Fatalf("read remote: %v", err)
	}
	if string(body) != "BBBB" {
		t.Fatalf("remote content = %q, want the new content", body)
	}
}
