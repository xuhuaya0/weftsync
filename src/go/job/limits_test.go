package job

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rclone/rclone/fs"

	"weftsync/split"
)

// F12: the per-directory cap belongs to the backend.
//
// Bug it fixes, measured before the change: a local -> local pair with 1401 files
// in one directory was refused with
//
//	目录 "(根目录)" 有 1401 个直接子项，超过飞书单层上限（我们按 1400 拦截，飞书硬限 1500）
//
// for a limit that does not exist on a local disk. The pre-flight check ran before
// the destination was even opened, with Feishu's number compiled in.

// limitedBackend models a backend that declares a cap, like Feishu does.
type limitedBackend struct {
	fs.Fs
	children int
	depth    int
}

func (b *limitedBackend) MaxChildrenPerDir() int { return b.children }
func (b *limitedBackend) MaxDirDepth() int       { return b.depth }

// unlimitedBackend models a local disk or SMB share: it simply does not implement
// split.LimitReporter.
type unlimitedBackend struct{ fs.Fs }

// TestBackendWithoutLimitReportsZero is the core of the fix: "no limit" must be
// distinguishable from "the default limit", and the only way to know is to ask.
func TestBackendWithoutLimitReportsZero(t *testing.T) {
	if children, depth := split.LimitOf(&unlimitedBackend{}); children != 0 || depth != 0 {
		t.Fatalf("LimitOf(unlimited) = %d/%d, want 0/0", children, depth)
	}
	if children, depth := split.LimitOf(nil); children != 0 || depth != 0 {
		t.Fatalf("LimitOf(nil) = %d/%d, want 0/0", children, depth)
	}
	// And the declaring backend is actually read.
	if children, depth := split.LimitOf(&limitedBackend{children: 1500, depth: 15}); children != 1500 || depth != 15 {
		t.Fatalf("LimitOf(limited) = %d/%d, want 1500/15", children, depth)
	}
}

// TestBackendLimitBecomesTheThreshold checks the headroom arithmetic and that a
// backend declaring nothing produces "no check at all" rather than 0 - 100.
func TestBackendLimitBecomesTheThreshold(t *testing.T) {
	cases := []struct {
		backendLimit int
		wantRefuse   int
	}{
		{0, 0},     // no limit declared
		{-1, 0},    // nonsensical, treated as no limit
		{50, 50},   // smaller than the headroom: do not go negative
		{100, 100}, // exactly the headroom
		{1500, 1400},
		{2000, 1900},
	}
	for _, c := range cases {
		if got := split.RefuseAtFor(c.backendLimit); got != c.wantRefuse {
			t.Errorf("RefuseAtFor(%d) = %d, want %d", c.backendLimit, got, c.wantRefuse)
		}
		// The bucket size follows the same rule, or a split would recreate the
		// problem it just solved.
		if got := split.MaxPerBucketFor(c.backendLimit); got != c.wantRefuse {
			t.Errorf("MaxPerBucketFor(%d) = %d, want %d", c.backendLimit, got, c.wantRefuse)
		}
	}
}

// TestNodeLimitPrefersTheJobFile pins the precedence: the job file wins, then the
// backend, then "no limit".
func TestNodeLimitPrefersTheJobFile(t *testing.T) {
	p := &Pair{Name: "p"}
	p.Remote.Root = ":local:/tmp/x"
	limited := &limitedBackend{children: 1500, depth: 15}

	// No job-file value: the backend decides.
	refuseAt, backendLimit, backend := nodeLimit(limited, ResolvedSync{}, p)
	if refuseAt != 1400 || backendLimit != 1500 {
		t.Errorf("no override: got refuseAt=%d backendLimit=%d, want 1400/1500",
			refuseAt, backendLimit)
	}
	if backend != "local" {
		t.Errorf("backend name = %q, want the on-the-fly scheme \"local\"", backend)
	}

	// An explicit value wins.
	n := 7
	refuseAt, _, _ = nodeLimit(limited, ResolvedSync{MaxNodesPerDir: &n}, p)
	if refuseAt != 7 {
		t.Errorf("override: got refuseAt=%d, want 7", refuseAt)
	}

	// An explicit 0 means "no check", and it must survive the backend's limit.
	zero := 0
	refuseAt, _, _ = nodeLimit(limited, ResolvedSync{MaxNodesPerDir: &zero}, p)
	if refuseAt != 0 {
		t.Errorf("explicit 0: got refuseAt=%d, want 0 (no check)", refuseAt)
	}

	// An unlimited backend with no override has nothing to check either.
	refuseAt, backendLimit, _ = nodeLimit(&unlimitedBackend{}, ResolvedSync{}, p)
	if refuseAt != 0 || backendLimit != 0 {
		t.Errorf("unlimited backend: got refuseAt=%d backendLimit=%d, want 0/0",
			refuseAt, backendLimit)
	}
}

// TestRemoteKindNamesTheDestination keeps the error message from naming the wrong
// backend, which is what the old hardcoded "飞书" did.
func TestRemoteKindNamesTheDestination(t *testing.T) {
	cases := []struct {
		backend, root, want string
	}{
		{"test", "token/dir", "test"},
		{"", ":local:/tmp/x", "local"},
		{"", ":smb:/share", "smb"},
		{"", "/plain/path", "local"},
	}
	for _, c := range cases {
		p := &Pair{}
		p.Remote.Backend = c.backend
		p.Remote.Root = c.root
		if got := RemoteKind(p); got != c.want {
			t.Errorf("RemoteKind(backend=%q root=%q) = %q, want %q",
				c.backend, c.root, got, c.want)
		}
	}
}

// TestLocalPairIsNotRefusedForFeishuLimits is the end-to-end version of the bug:
// a local destination must never produce an over-limit refusal, whatever the size.
//
// It uses the real local backend through fs.NewFs, so it also proves the type
// assertion on the cached Fs behaves as expected (it finds no LimitReporter).
func TestLocalPairIsNotRefusedForFeishuLimits(t *testing.T) {
	cfg, local, _ := localPair(t, VariantUpdate, "")

	// 1401 is one past the old hardcoded Feishu threshold of 1400.
	makeManyFiles(t, local, 1401)

	res, err := RunPair(context.Background(), cfg, &cfg.Pair[0], io.Discard,
		Options{DryRun: true})
	if err != nil {
		var limitErr *split.OverLimitError
		if errors.As(err, &limitErr) {
			t.Fatalf("a local destination was refused by a Feishu limit: %v", err)
		}
		t.Fatalf("preview: %v", err)
	}
	if len(res.OverLimit) != 0 {
		t.Fatalf("OverLimit = %v, want none for a local destination", res.OverLimit)
	}
}

// TestExplicitZeroReallyMeansNoLimit covers the third state of the job-file
// setting. Without it there was no way to say "this backend has no cap" and the
// check could only be relaxed, never turned off.
func TestExplicitZeroReallyMeansNoLimit(t *testing.T) {
	cfg, local, _ := localPair(t, VariantUpdate, `
[pair.sync]
max_nodes_per_dir = 0
`)
	makeManyFiles(t, local, 8)

	res, err := RunPair(context.Background(), cfg, &cfg.Pair[0], io.Discard,
		Options{DryRun: true})
	if err != nil {
		t.Fatalf("max_nodes_per_dir = 0 must not refuse anything: %v", err)
	}
	if len(res.OverLimit) != 0 {
		t.Fatalf("OverLimit = %v, want none", res.OverLimit)
	}
}

// TestNegativeLimitIsRejected: a negative value must not slip through as "no
// limit". It is a typo in a safety setting, not a way to disable the guard.
func TestNegativeLimitIsRejected(t *testing.T) {
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
max_nodes_per_dir = -1
`
	path := filepath.Join(base, "job.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write job: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("a negative max_nodes_per_dir must be rejected")
	}
	if !strings.Contains(err.Error(), "max_nodes_per_dir") {
		t.Fatalf("the error should name the setting, got: %v", err)
	}
}
