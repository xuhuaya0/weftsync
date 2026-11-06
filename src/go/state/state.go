// Package state implements the local sync snapshot that lets weftsync decide
// what changed between runs.
//
// Why this exists: Feishu reports no size and no content hash, and its
// modified_time is the upload moment rather than the file's modification time
// (see PLAN.md §3). Comparing local mtime against the remote value therefore
// always says "different", which would re-upload everything on every run.
//
// Instead we remember, per synced file, what the local file looked like when we
// last uploaded it. Next run we compare the local file against that memory —
// two local values, same semantics, no timezone or clock-skew problems.
//
// The file is protected against silent corruption (CRC32), written atomically,
// and backed up, because a corrupted token would make us delete the wrong file
// and Feishu has no trash to undo that.
package state

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
)

// Version is the on-disk format version. A different value means "cannot use
// this file"; the caller must fall back to rebuild mode.
const Version = 1

// headerPrefix starts the first line of the file. The CRC32 on that line covers
// every byte after the newline.
const headerPrefix = "# weftsync state v"

// ErrCorrupt means the state file exists but cannot be trusted.
//
// Callers must react by entering repair mode: rebuild from the remote listing
// and perform no deletions this run.
var ErrCorrupt = errors.New("state: file is corrupt or incompatible")

// Entry is what we remember about one successfully synced file.
type Entry struct {
	// Path is the path relative to the sync root, always "/"-separated so the
	// same state works on Windows and Linux.
	Path string `json:"path"`
	// LocalMTime is the local file's modification time (unix seconds) at the
	// moment we uploaded it.
	LocalMTime int64 `json:"local_mtime"`
	// LocalSize is the local file's byte count at that moment. It exists to
	// catch a content change that preserved the mtime.
	LocalSize int64 `json:"local_size"`
	// RemoteToken identifies the uploaded copy, so a later replacement can
	// delete exactly that one. Feishu allows duplicate names, so deleting by
	// name could hit the wrong file.
	RemoteToken string `json:"remote_token"`
	// SyncedAt is when the upload succeeded. Diagnostics only; never compared.
	SyncedAt int64 `json:"synced_at"`
	// Rebuilt marks an entry recovered from the remote listing rather than from
	// an actual upload. Such an entry has no local fingerprint, so it never
	// matches a local file and will be re-uploaded once.
	Rebuilt bool `json:"rebuilt,omitempty"`
}

// File is the whole snapshot for one folder pair.
type File struct {
	Version int              `json:"version"`
	PairID  string           `json:"pair_id"`
	Entries map[string]Entry `json:"entries"`

	path string // where it was loaded from; not serialised
}

// Path returns the file location this snapshot was loaded from or saved to.
func (f *File) Path() string { return f.path }

// SetPath tells the snapshot where to save itself.
//
// Needed for snapshots built in memory (first run, repair), which have no file
// to inherit a location from.
func (f *File) SetPath(p string) { f.path = p }

// New returns an empty snapshot for the given pair.
func New(pairID string) *File {
	return &File{
		Version: Version,
		PairID:  pairID,
		Entries: make(map[string]Entry),
	}
}

// Lookup returns the remembered entry for rel.
func (f *File) Lookup(rel string) (Entry, bool) {
	e, ok := f.Entries[NormalizePath(rel)]
	return e, ok
}

// Put records a successful sync.
func (f *File) Put(rel string, e Entry) {
	e.Path = NormalizePath(rel)
	if f.Entries == nil {
		f.Entries = make(map[string]Entry)
	}
	f.Entries[e.Path] = e
}

// Delete forgets a path.
func (f *File) Delete(rel string) {
	delete(f.Entries, NormalizePath(rel))
}

// Len reports how many entries are remembered.
func (f *File) Len() int { return len(f.Entries) }

// NormalizePath converts a path into the canonical form used as the map key.
func NormalizePath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.Trim(p, "/")
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	return p
}

// PairID derives a stable identifier from the two roots.
//
// The roots are joined with a NUL so that ("ab","c") and ("a","bc") cannot
// collide.
func PairID(localRoot, remoteRoot string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(filepath.ToSlash(localRoot)))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(remoteRoot))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// DefaultPath returns the conventional location for a pair's state file.
func DefaultPath(pairID string) (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("state: cannot locate the user config directory: %w", err)
	}
	return filepath.Join(dir, "weftsync", "state", pairID+".state.json"), nil
}

// PairIDFromPath recovers the pair id from a state file name.
//
// Used by the backend, which is handed a path (not a pair id) in its
// configuration and must still build a valid snapshot on first run.
func PairIDFromPath(path string) string {
	base := filepath.Base(path)
	base = strings.TrimSuffix(base, ".bak")
	base = strings.TrimSuffix(base, ".tmp")
	base = strings.TrimSuffix(base, ".state.json")
	return base
}

// Load reads and validates a snapshot.
//
// Returns:
//   - a usable *File on success (possibly recovered from the .bak)
//   - ErrCorrupt when nothing trustworthy is available; the caller must then
//     rebuild and must not delete anything this run
//   - nil, nil when the file simply does not exist yet (first run)
func Load(path string) (*File, error) {
	f, err := loadOne(path)
	if err == nil {
		return f, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}

	// Primary is unusable; try the backup before giving up.
	bak, bakErr := loadOne(path + ".bak")
	if bakErr == nil {
		fmt.Fprintf(os.Stderr,
			"WARNING: state file %s was unusable (%v); recovered from %s.bak\n",
			path, err, path)
		bak.path = path
		return bak, nil
	}

	return nil, fmt.Errorf("%w: %s (%v; backup also unusable: %v)", ErrCorrupt, path, err, bakErr)
}

func loadOne(path string) (*File, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parse(path, raw)
}

// parse validates the header, the CRC, the JSON and the invariants.
func parse(path string, raw []byte) (*File, error) {
	nl := bytes.IndexByte(raw, '\n')
	if nl < 0 {
		return nil, fmt.Errorf("missing header line")
	}
	header := string(raw[:nl])
	body := raw[nl+1:]

	if !strings.HasPrefix(header, headerPrefix) {
		return nil, fmt.Errorf("unrecognised header %q", header)
	}
	ver, crcHex, ok := parseHeader(header)
	if !ok {
		return nil, fmt.Errorf("malformed header %q", header)
	}
	if ver != Version {
		return nil, fmt.Errorf("unsupported version %d (this build understands %d)", ver, Version)
	}
	if got, want := crc32.ChecksumIEEE(body), crcHex; got != want {
		return nil, fmt.Errorf("checksum mismatch (stored %08x, computed %08x)", crcHex, got)
	}

	// Strip the single trailing newline we always write.
	jsonBody := bytes.TrimSuffix(body, []byte("\n"))

	var f File
	if err := json.Unmarshal(jsonBody, &f); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	base := strings.TrimSuffix(filepath.Base(path), ".bak")
	base = strings.TrimSuffix(base, ".state.json")
	if f.PairID != "" && f.PairID != base {
		return nil, fmt.Errorf("pair_id %q does not match the filename (%q)", f.PairID, base)
	}

	if f.Entries == nil {
		f.Entries = make(map[string]Entry)
	}

	// Drop individual bad entries rather than rejecting the whole file: one
	// damaged record should cost one re-upload, not a full rebuild.
	bad := 0
	for k, e := range f.Entries {
		if !validEntry(k, e) {
			delete(f.Entries, k)
			bad++
		}
	}
	if bad > 0 {
		fmt.Fprintf(os.Stderr, "WARNING: state file %s had %d invalid entries which were dropped\n", path, bad)
	}

	f.path = path
	return &f, nil
}

func validEntry(key string, e Entry) bool {
	if NormalizePath(key) != key || key == "" {
		return false
	}
	if e.Path != key {
		return false
	}
	if e.RemoteToken == "" {
		return false
	}
	if e.Rebuilt {
		// Rebuilt entries deliberately carry no fingerprint.
		return true
	}
	if e.LocalMTime <= 0 || e.LocalSize < 0 {
		return false
	}
	return true
}

// parseHeader splits "# weftsync state v1  crc32=1a2b3c4d".
func parseHeader(header string) (version int, crc uint32, ok bool) {
	rest := strings.TrimPrefix(header, headerPrefix)
	i := strings.IndexAny(rest, " \t")
	if i < 0 {
		return 0, 0, false
	}
	if _, err := fmt.Sscanf(rest[:i], "%d", &version); err != nil {
		return 0, 0, false
	}
	crcPart := strings.TrimSpace(rest[i:])
	crcPart = strings.TrimPrefix(crcPart, "crc32=")
	if _, err := fmt.Sscanf(crcPart, "%x", &crc); err != nil {
		return 0, 0, false
	}
	return version, crc, true
}

// Save writes the snapshot atomically, keeping the previous version as .bak.
//
// Sequence (contracts/state-file-format.md §4):
//  1. write <path>.tmp with a zeroed CRC, then rewrite the header with the real one
//  2. fsync so the rename below cannot expose an empty file after a power cut
//  3. copy the current file to <path>.bak (the previous good version)
//  4. rename .tmp over the target (atomic on the same filesystem)
func Save(f *File) error {
	if f.path == "" {
		return errors.New("state: cannot save a snapshot with no path")
	}
	if f.PairID == "" {
		return errors.New("state: cannot save a snapshot with an empty pair_id")
	}

	f.Version = Version

	body, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("state: encode: %w", err)
	}
	body = append(body, '\n')

	sum := crc32.ChecksumIEEE(body)
	header := fmt.Sprintf("%s%d  crc32=%08x\n", headerPrefix, Version, sum)

	if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
		return fmt.Errorf("state: create directory: %w", err)
	}

	tmp := f.path + ".tmp"
	if err := writeFileSync(tmp, append([]byte(header), body...)); err != nil {
		_ = os.Remove(tmp)
		return err
	}

	// Keep the current file as .bak before replacing it.
	if _, err := os.Stat(f.path); err == nil {
		if err := copyFile(f.path, f.path+".bak"); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("state: back up previous version: %w", err)
		}
	}

	if err := os.Rename(tmp, f.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("state: replace %s: %w", f.path, err)
	}

	return nil
}

func writeFileSync(path string, data []byte) error {
	fh, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("state: create %s: %w", path, err)
	}
	if _, err := fh.Write(data); err != nil {
		_ = fh.Close()
		return fmt.Errorf("state: write %s: %w", path, err)
	}
	// fsync before the rename: without it a power loss can leave a renamed but
	// empty file.
	if err := fh.Sync(); err != nil {
		_ = fh.Close()
		return fmt.Errorf("state: sync %s: %w", path, err)
	}
	if err := fh.Close(); err != nil {
		return fmt.Errorf("state: close %s: %w", path, err)
	}
	return nil
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return writeFileSync(dst, data)
}

// Repair builds a snapshot from what the remote currently holds.
//
// Used when the old state is missing or corrupt. Repaired entries carry no
// local fingerprint — we cannot know what the local file looked like when it
// was uploaded — and are marked Rebuilt so they survive validation and are
// distinguishable from real records.
//
// The consequence is deliberate: a rebuilt entry never matches a local file, so
// the next run re-uploads everything once and then records real fingerprints.
// That run performs no deletions (see Caller contract in the package doc).
func Repair(pairID string, remotePaths map[string]string) *File {
	f := New(pairID)
	for path, token := range remotePaths {
		p := NormalizePath(path)
		if p == "" || token == "" {
			continue
		}
		f.Entries[p] = Entry{
			Path:        p,
			RemoteToken: token,
			Rebuilt:     true,
		}
	}
	return f
}
