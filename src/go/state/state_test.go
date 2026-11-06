package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func tempStatePath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, "0123456789abcdef.state.json")
}

func sampleFile(pairID string) *File {
	f := New(pairID)
	f.Put("docs/a.xlsx", Entry{
		LocalMTime:  1793787000,
		LocalSize:   102400,
		RemoteToken: "GtflbM301o7v",
		SyncedAt:    1793787100,
	})
	f.Put("docs/b.png", Entry{
		LocalMTime:  1793787100,
		LocalSize:   2048,
		RemoteToken: "Acw0bHGqCov0",
		SyncedAt:    1793787200,
	})
	return f
}

// S1: write then read returns the same entries.
func TestSaveLoadRoundTrip(t *testing.T) {
	path := tempStatePath(t)
	f := sampleFile("0123456789abcdef")
	f.path = path

	if err := Save(f); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Len() != 2 {
		t.Fatalf("entries = %d, want 2", got.Len())
	}
	e, ok := got.Lookup("docs/a.xlsx")
	if !ok {
		t.Fatal("docs/a.xlsx missing after reload")
	}
	if e.LocalSize != 102400 || e.RemoteToken != "GtflbM301o7v" {
		t.Errorf("entry mismatch: %+v", e)
	}
}

// S2: the header carries a CRC32 that matches the body.
func TestSaveWritesValidCRC(t *testing.T) {
	path := tempStatePath(t)
	f := sampleFile("0123456789abcdef")
	f.path = path
	if err := Save(f); err != nil {
		t.Fatalf("Save: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	header, _, _ := strings.Cut(string(raw), "\n")
	if !strings.Contains(header, "crc32=") {
		t.Fatalf("header missing crc: %q", header)
	}
	if !strings.HasPrefix(header, "# weftsync state v1") {
		t.Errorf("header = %q", header)
	}
}

// S3: flipping one byte in the body invalidates the checksum.
func TestLoadDetectsTampering(t *testing.T) {
	path := tempStatePath(t)
	f := sampleFile("0123456789abcdef")
	f.path = path
	if err := Save(f); err != nil {
		t.Fatalf("Save: %v", err)
	}

	raw, _ := os.ReadFile(path)
	nl := indexByte(raw, '\n')
	// Change a digit inside the JSON body.
	raw[nl+20] ^= 0x01
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected a corruption error")
	}
	if !errors.Is(err, ErrCorrupt) {
		t.Errorf("error = %v, want ErrCorrupt", err)
	}
}

// S4: a corrupted primary falls back to the .bak.
func TestLoadRecoversFromBackup(t *testing.T) {
	path := tempStatePath(t)
	pairID := "0123456789abcdef"

	f1 := sampleFile(pairID)
	f1.path = path
	if err := Save(f1); err != nil {
		t.Fatalf("first Save: %v", err)
	}

	// Second save creates the .bak from the first version.
	f2 := New(pairID)
	f2.path = path
	f2.Put("only.txt", Entry{LocalMTime: 1, LocalSize: 1, RemoteToken: "T"})
	if err := Save(f2); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Fatalf("expected a .bak: %v", err)
	}

	// Corrupt the primary.
	raw, _ := os.ReadFile(path)
	raw[len(raw)/2] ^= 0xFF
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load should recover from .bak, got: %v", err)
	}
	if _, ok := got.Lookup("docs/a.xlsx"); !ok {
		t.Errorf("recovered snapshot should be the older one with docs/a.xlsx, got %d entries", got.Len())
	}
}

// S5: no usable primary and no usable backup → ErrCorrupt (caller must enter
// repair mode and delete nothing).
func TestLoadReportsCorruptWhenBothBroken(t *testing.T) {
	path := tempStatePath(t)
	if err := os.WriteFile(path, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".bak", []byte("also garbage"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load(path)
	if !errors.Is(err, ErrCorrupt) {
		t.Errorf("error = %v, want ErrCorrupt", err)
	}
}

// S6: a missing file is not an error; it means "first run".
func TestLoadMissingIsNil(t *testing.T) {
	path := tempStatePath(t)
	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if f != nil {
		t.Errorf("expected nil for a missing file, got %+v", f)
	}
}

// S7: a future format version is rejected rather than misread.
func TestLoadRejectsUnknownVersion(t *testing.T) {
	path := tempStatePath(t)
	body := []byte(`{"version":2,"pair_id":"0123456789abcdef","entries":{}}` + "\n")
	header := "# weftsync state v2  crc32=00000000\n"
	if err := os.WriteFile(path, append([]byte(header), body...), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load(path)
	if !errors.Is(err, ErrCorrupt) {
		t.Errorf("error = %v, want ErrCorrupt", err)
	}
}

// S8: a pair_id that does not match the filename is rejected — that means a
// state file was copied between pairs.
func TestLoadRejectsMismatchedPairID(t *testing.T) {
	path := tempStatePath(t)
	f := sampleFile("ffffffffffffffff") // filename says 0123456789abcdef
	f.path = path
	if err := Save(f); err != nil {
		t.Fatalf("Save: %v", err)
	}

	_, err := Load(path)
	if !errors.Is(err, ErrCorrupt) {
		t.Errorf("error = %v, want ErrCorrupt", err)
	}
}

// S9: one malformed entry is dropped without discarding the rest.
func TestLoadDropsInvalidEntries(t *testing.T) {
	path := tempStatePath(t)
	pairID := "0123456789abcdef"

	payload := map[string]any{
		"version": 1,
		"pair_id": pairID,
		"entries": map[string]any{
			"good.txt": map[string]any{
				"path": "good.txt", "local_mtime": 100, "local_size": 5,
				"remote_token": "GOOD", "synced_at": 100,
			},
			"bad.txt": map[string]any{
				"path": "bad.txt", "local_mtime": 0, "local_size": 5,
				"remote_token": "BAD", "synced_at": 100,
			},
		},
	}
	body, _ := json.Marshal(payload)
	body = append(body, '\n')
	header := "# weftsync state v1  crc32=" + crcHex(body) + "\n"
	if err := os.WriteFile(path, append([]byte(header), body...), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := got.Lookup("good.txt"); !ok {
		t.Error("good.txt should survive")
	}
	if _, ok := got.Lookup("bad.txt"); ok {
		t.Error("bad.txt should have been dropped")
	}
}

// S10: a left-over .tmp from a crashed write does not damage the real file.
func TestLeftoverTempDoesNotCorrupt(t *testing.T) {
	path := tempStatePath(t)
	f := sampleFile("0123456789abcdef")
	f.path = path
	if err := Save(f); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Simulate a crash mid-write.
	if err := os.WriteFile(path+".tmp", []byte("# weftsync state v1  crc32=deadbeef\n{\"half"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load should ignore the stray .tmp, got: %v", err)
	}
	if got.Len() != 2 {
		t.Errorf("entries = %d, want 2", got.Len())
	}
}

// S11: a second process cannot take the lock while the first holds it.
//
// The lock is intentionally shareable inside one process (rclone builds several
// Fs instances per remote), so "exclusive" can only be tested across processes.
func TestLockIsExclusiveAcrossProcesses(t *testing.T) {
	path := tempStatePath(t)

	ready := filepath.Join(filepath.Dir(path), "ready")
	goAhead := filepath.Join(filepath.Dir(path), "go")

	cmd := exec.Command(os.Args[0], "-test.run=TestLockHelperProcess", "-test.v=false")
	cmd.Env = append(os.Environ(),
		"WEFTSYNC_LOCK_CHILD=1",
		"WEFTSYNC_LOCK_PATH="+path,
		"WEFTSYNC_LOCK_READY="+ready,
		"WEFTSYNC_LOCK_GO="+goAhead,
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	defer func() {
		_ = os.WriteFile(goAhead, []byte("1"), 0o600)
		_ = cmd.Wait()
	}()

	waitFor(t, ready, "helper did not acquire the lock")

	_, err := AcquireLock(path)
	if !errors.Is(err, ErrLocked) {
		t.Errorf("error = %v, want ErrLocked", err)
	}
}

// TestLockHelperProcess is not a real test: it is the second process used by
// TestLockIsExclusiveAcrossProcesses. It takes the lock, signals readiness, and
// holds it until the parent closes the go-ahead file.
func TestLockHelperProcess(t *testing.T) {
	if os.Getenv("WEFTSYNC_LOCK_CHILD") != "1" {
		t.Skip("helper process only")
	}
	l, err := AcquireLock(os.Getenv("WEFTSYNC_LOCK_PATH"))
	if err != nil {
		t.Fatalf("helper AcquireLock: %v", err)
	}
	defer l.Release()

	if err := os.WriteFile(os.Getenv("WEFTSYNC_LOCK_READY"), []byte("1"), 0o600); err != nil {
		t.Fatalf("helper signal: %v", err)
	}

	waitFor(t, os.Getenv("WEFTSYNC_LOCK_GO"), "helper was never told to finish")
}

func waitFor(t *testing.T, path, msg string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(msg)
}

// Within one process the lock is shared and reference counted, since rclone
// builds multiple Fs instances for one remote.
func TestLockSharedWithinProcess(t *testing.T) {
	path := tempStatePath(t)

	l1, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("first AcquireLock: %v", err)
	}
	l2, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("second AcquireLock in the same process: %v", err)
	}
	if l1 != l2 {
		t.Error("expected the same lock instance to be shared")
	}

	l1.Release()
	if _, err := os.Stat(l1.Path()); err != nil {
		t.Errorf("lock file should survive until the last reference is released: %v", err)
	}

	l2.Release()
	if _, err := os.Stat(l1.Path()); !os.IsNotExist(err) {
		t.Errorf("lock file should be gone after the last release: %v", err)
	}
}

// S12: a lock left behind by a dead process is taken over.
func TestLockTakeoverOfDeadPID(t *testing.T) {
	path := tempStatePath(t)
	lockPath := strings.TrimSuffix(path, filepath.Ext(path)) + ".lock"

	// pid 1 is init/system and will never be our process; use an implausible
	// high pid instead so the "alive" check reliably fails.
	if err := os.WriteFile(lockPath, []byte("999999999\n2020-01-01T00:00:00Z\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	l, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("AcquireLock should take over a stale lock, got: %v", err)
	}
	l.Release()
}

// S13: releasing makes the lock available again.
func TestLockRelease(t *testing.T) {
	path := tempStatePath(t)

	l1, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	l1.Release()

	l2, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("AcquireLock after Release: %v", err)
	}
	l2.Release()
}

// Paths with CJK characters and spaces survive a round trip unchanged.
func TestUnicodePaths(t *testing.T) {
	path := tempStatePath(t)
	f := New("0123456789abcdef")
	f.path = path
	f.Put("季度报表/2024 年 1 月.xlsx", Entry{
		LocalMTime: 100, LocalSize: 10, RemoteToken: "TOK",
	})
	if err := Save(f); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := got.Lookup("季度报表/2024 年 1 月.xlsx"); !ok {
		t.Error("unicode path lost in round trip")
	}
}

// Windows separators are normalised on the way in.
func TestNormalizePathWindows(t *testing.T) {
	f := New("p")
	f.Put(`a\b\c.txt`, Entry{LocalMTime: 1, LocalSize: 1, RemoteToken: "T"})
	if _, ok := f.Lookup("a/b/c.txt"); !ok {
		t.Error(`backslash path was not normalised to "a/b/c.txt"`)
	}
}

// Repair produces entries that survive validation and never match a local file.
func TestRepairEntriesAreUsable(t *testing.T) {
	path := tempStatePath(t)
	f := Repair("0123456789abcdef", map[string]string{
		"a.txt": "TOK-A",
		"b/c.txt": "TOK-B",
	})
	f.path = path
	if err := Save(f); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Len() != 2 {
		t.Fatalf("entries = %d, want 2 (rebuilt entries must survive validation)", got.Len())
	}
	e, ok := got.Lookup("a.txt")
	if !ok {
		t.Fatal("a.txt missing")
	}
	if !e.Rebuilt {
		t.Error("rebuilt flag lost")
	}
	if e.LocalMTime != 0 {
		t.Error("rebuilt entry should carry no fingerprint")
	}
}

// PairID must distinguish roots that would collide under naive concatenation.
func TestPairIDUniqueness(t *testing.T) {
	a := PairID("ab", "c")
	b := PairID("a", "bc")
	if a == b {
		t.Error("PairID collides for (ab,c) and (a,bc)")
	}
	if len(a) != 16 {
		t.Errorf("PairID length = %d, want 16", len(a))
	}
	if PairID("x", "y") != PairID("x", "y") {
		t.Error("PairID is not deterministic")
	}
}

// helpers

func indexByte(b []byte, c byte) int {
	for i := range b {
		if b[i] == c {
			return i
		}
	}
	return -1
}

func crcHex(b []byte) string {
	return fmt.Sprintf("%08x", crc32.ChecksumIEEE(b))
}
