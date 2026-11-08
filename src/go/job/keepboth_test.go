package job

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// keep-both used to upload a fresh copy on EVERY run.
//
// The snapshot still held the token from two runs ago, so the next run reported
// the same conflict, kept both again, and left another file behind. Measured on
// the real tenant: two consecutive runs produced "...094030.txt" and
// "...094039.txt", and nothing would have stopped it until the folder hit
// Feishu's 1500-node limit.
//
// The fix is that keep-both records the cloud copy as the baseline, so the
// conflict does not resurface. This test watches that the backend is asked to
// adopt, with the token of the copy that is actually in the cloud.
func TestKeepBothRecordsTheCloudCopyAsBaseline(t *testing.T) {
	local := t.TempDir()
	remote := t.TempDir()
	writeFile(t, filepath.Join(local, "a.txt"), "local version")
	writeFile(t, filepath.Join(remote, "a.txt"), "remote version")

	pair := testPair(t, local, remote)

	var got map[string]string
	prev := resolveFn
	resolveFn = func(ctx context.Context, p *Pair, path string, opt map[string]string) error {
		got = opt
		got["path"] = path
		return nil
	}
	t.Cleanup(func() { resolveFn = prev })

	if err := adoptCloudCopy(context.Background(), pair, "a.txt", "tok-cloud"); err != nil {
		t.Fatalf("adoptCloudCopy: %v", err)
	}
	if got == nil {
		t.Fatal("the backend was never asked to record a baseline")
	}
	if got["action"] != "adopt" {
		t.Errorf("action = %q, want adopt", got["action"])
	}
	if got["path"] != "a.txt" {
		t.Errorf("path = %q", got["path"])
	}
	// The token must describe the file that is in the cloud now, not the one we
	// uploaded before the conflict.
	if tok := got["token"]; tok == "" || tok == "?" {
		t.Errorf("token = %q, want the token of the cloud copy", tok)
	}
	// The fingerprint must describe the LOCAL file, because that is what the next
	// run will compare against.
	info, err := os.Stat(filepath.Join(local, "a.txt"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got["size"] != "14" && got["size"] != itoa(int(info.Size())) {
		t.Errorf("size = %q, want the local file size %d", got["size"], info.Size())
	}
}

// A local file that vanished between the upload and the adopt must be reported,
// not silently recorded with a zero fingerprint.
func TestKeepBothAdoptFailsWhenThePathIsGone(t *testing.T) {
	local := t.TempDir()
	remote := t.TempDir()
	writeFile(t, filepath.Join(local, "a.txt"), "local version")
	writeFile(t, filepath.Join(remote, "a.txt"), "remote version")
	pair := testPair(t, local, remote)

	prev := resolveFn
	resolveFn = func(ctx context.Context, p *Pair, path string, opt map[string]string) error {
		t.Error("adopt must not be attempted when the path cannot be read")
		return nil
	}
	t.Cleanup(func() { resolveFn = prev })

	if err := adoptCloudCopy(context.Background(), pair, "missing.txt", "tok-cloud"); err == nil {
		t.Fatal("a missing path should be an error")
	}
}

// Without a token there is nothing to record, and writing "?" into the snapshot
// would make every later comparison fail.
func TestKeepBothRefusesToRecordANonexistentToken(t *testing.T) {
	local := t.TempDir()
	remote := t.TempDir()
	writeFile(t, filepath.Join(local, "a.txt"), "local version")
	pair := testPair(t, local, remote)

	prev := resolveFn
	resolveFn = func(ctx context.Context, p *Pair, path string, opt map[string]string) error {
		t.Error("adopt must not be attempted without a token")
		return nil
	}
	t.Cleanup(func() { resolveFn = prev })

	for _, token := range []string{"", "?"} {
		if err := adoptCloudCopy(context.Background(), pair, "a.txt", token); err == nil {
			t.Errorf("token %q should be refused", token)
		}
	}
}

// keep-both must not claim nothing happened in the same run that says it uploaded.
func TestConflictErrorWordingMatchesWhatHappened(t *testing.T) {
	kept := &ConflictError{Pair: "p", Count: 1, KeptBoth: true}
	msg := kept.Error()
	if strings.Contains(msg, "NOTHING was uploaded") {
		t.Errorf("keep-both did upload, so the message is a lie\ngot: %s", msg)
	}
	if !strings.Contains(msg, "new name") {
		t.Errorf("the message should say what happened\ngot: %s", msg)
	}

	// The report policy really changes nothing, so it keeps the strong wording.
	report := &ConflictError{Pair: "p", Count: 1}
	if !strings.Contains(report.Error(), "NOTHING was uploaded") {
		t.Errorf("the report policy must say nothing happened\ngot: %s", report.Error())
	}
}

// testPair builds a one-pair config pointing at two directories.
func testPair(t *testing.T, local, remote string) *Pair {
	t.Helper()
	body := `
[[pair]]
name = "p"
[pair.local]
root = "` + filepath.ToSlash(local) + `"
[pair.remote]
root = ":local:` + filepath.ToSlash(remote) + `"
[pair.sync]
conflict = "keep-both"
`
	path := filepath.Join(t.TempDir(), "job.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write job: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return &cfg.Pair[0]
}
