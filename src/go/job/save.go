package job

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Saving job files.
//
// Two things make this riskier than writing any other config: the file decides
// what gets deleted in the cloud, and it is a file people also edit by hand.
//
// So:
//   - the candidate config is validated with exactly the same rules Load uses,
//     before anything touches the disk
//   - the previous version is kept as .bak, because a bad edit that only shows up
//     after the next sync is too late to diff
//   - the write is atomic (temp + rename), so an interrupted save cannot leave a
//     half-written job file
//   - comments and formatting are LOST when round-tripping through the parsed
//     form, and that is detected rather than glossed over (CanRoundTrip)

// ErrLossyRewrite means re-encoding the config would not reproduce the file
// byte for byte, so comments, blank lines or ordering would disappear.
var ErrLossyRewrite = fmt.Errorf(
	"rewriting this file would lose its comments or formatting")

// Encode renders a config as TOML.
//
// The output is written by hand rather than by a struct encoder on purpose. A
// struct encoder emits every field including zero values, producing things like
// "pair = []" and "mode = ”" for a four-line config. That is unreadable for the
// file a human is supposed to be able to edit, and it also makes the round-trip
// check useless because a hand-written file could never match it.
//
// Only fields with a non-default value are emitted, in a fixed order. The result
// is re-parsed and validated by Save before it reaches the disk, so a mistake
// here fails loudly instead of writing something subtly different.
func Encode(cfg *Config) ([]byte, error) {
	var b bytes.Buffer

	b.WriteString("[job]\n")
	writeString(&b, "name", cfg.Job.Name)
	if cfg.Job.Enabled != nil {
		fmt.Fprintf(&b, "enabled = %t\n", *cfg.Job.Enabled)
	}
	if cfg.Job.Variant != "" {
		writeString(&b, "variant", cfg.Job.Variant)
	}
	writeSyncSection(&b, "job.sync", cfg.Job.Sync)
	writeScheduleSection(&b, "job.schedule", cfg.Job.Schedule)

	for _, p := range cfg.Pair {
		b.WriteString("\n[[pair]]\n")
		writeString(&b, "name", p.Name)
		if p.Variant != "" {
			writeString(&b, "variant", p.Variant)
		}
		writeEndpoint(&b, "pair.local", p.Local)
		writeEndpoint(&b, "pair.remote", p.Remote)
		writeSyncSection(&b, "pair.sync", p.Sync)
	}

	return b.Bytes(), nil
}

// writeString emits key = "value" with a minimal escape set.
//
// The values that reach here are paths, job names and enum constants. Escaping
// only what TOML requires keeps the output readable; anything exotic is caught by
// the re-parse in Save.
func writeString(b *bytes.Buffer, key, value string) {
	if value == "" {
		return
	}
	b.WriteString(key)
	b.WriteString(" = \"")
	b.WriteString(tomlEscape(value))
	b.WriteString("\"\n")
}

func tomlEscape(s string) string {
	var out bytes.Buffer
	for _, r := range s {
		switch r {
		case '"':
			out.WriteString(`\"`)
		case '\\':
			out.WriteString(`\\`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}

func writeSyncSection(b *bytes.Buffer, name string, s SyncSpec) {
	empty := s.Deletion == "" && s.VersioningDir == "" && s.VersioningNaming == "" &&
		s.Conflict == "" && s.MaxDelete == nil && s.MaxDeleteSize == "" &&
		!s.PropagateLocalDelete && !s.PropagateRemoteDelete && !s.PullNewFiles &&
		s.MaxPullSize == "" && s.MaxNodesPerDir == nil && s.Compare == "" &&
		len(s.Filter.Include) == 0 && len(s.Filter.Exclude) == 0
	if empty {
		return
	}
	b.WriteString("\n[" + name + "]\n")
	if s.Deletion != "" {
		writeString(b, "deletion", s.Deletion)
	}
	if s.VersioningDir != "" {
		writeString(b, "versioning_dir", s.VersioningDir)
	}
	if s.VersioningNaming != "" {
		writeString(b, "versioning_naming", s.VersioningNaming)
	}
	if s.Conflict != "" {
		writeString(b, "conflict", s.Conflict)
	}
	if s.MaxDelete != nil {
		fmt.Fprintf(b, "max_delete = %d\n", *s.MaxDelete)
	}
	if s.MaxDeleteSize != "" {
		writeString(b, "max_delete_size", s.MaxDeleteSize)
	}
	// Only written when on: these are booleans whose absent value is already
	// false, so emitting "= false" would just add noise to every file.
	if s.PropagateLocalDelete {
		b.WriteString("propagate_local_delete = true\n")
	}
	if s.PropagateRemoteDelete {
		b.WriteString("propagate_remote_delete = true\n")
	}
	if s.PullNewFiles {
		b.WriteString("pull_new_files = true\n")
	}
	if s.MaxPullSize != "" {
		writeString(b, "max_pull_size", s.MaxPullSize)
	}
	// Written even when 0: 0 is the meaningful "no limit here" and differs from
	// "absent = ask the backend". Dropping it would silently restore Feishu's
	// number on a backend that has none.
	if s.MaxNodesPerDir != nil {
		fmt.Fprintf(b, "max_nodes_per_dir = %d\n", *s.MaxNodesPerDir)
	}
	if s.Compare != "" {
		writeString(b, "compare", s.Compare)
	}
	// The filter lives in a nested table, so it must be written LAST: once a
	// [x.filter] header is emitted, every following key belongs to it and TOML
	// cannot return to the parent table without repeating its header.
	//
	// Writing them as plain keys under [x] produced a file the strict parser then
	// rejected ("fields in the document are missing in the target struct"), i.e. a
	// save that could not be loaded back.
	writeFilterSection(b, name+".filter", s.Filter)
}

// writeFilterSection emits a [....filter] table with its two pattern lists.
func writeFilterSection(b *bytes.Buffer, name string, f FilterSpec) {
	if len(f.Include) == 0 && len(f.Exclude) == 0 {
		return
	}
	b.WriteString("\n[" + name + "]\n")
	writeStringSlice(b, "include", f.Include)
	writeStringSlice(b, "exclude", f.Exclude)
}

// writeStringSlice emits a TOML array of strings, one element per line.
//
// One per line rather than a single long line: filter lists are read and edited
// by hand, and a wrapped array is the difference between a diff a person can
// review and one they scroll past.
func writeStringSlice(b *bytes.Buffer, key string, values []string) {
	if len(values) == 0 {
		return
	}
	b.WriteString(key + " = [\n")
	for _, v := range values {
		b.WriteString("  \"")
		b.WriteString(tomlEscape(v))
		b.WriteString("\",\n")
	}
	b.WriteString("]\n")
}

func writeScheduleSection(b *bytes.Buffer, name string, s ScheduleSpec) {
	empty := s.Mode == "" && s.Interval == "" && s.StartAt == "" &&
		!s.AlignToStart && s.DailyTime == "" && s.Cron == ""
	if empty {
		return
	}
	b.WriteString("\n[" + name + "]\n")
	if s.Mode != "" {
		writeString(b, "mode", s.Mode)
	}
	if s.Interval != "" {
		writeString(b, "interval", s.Interval)
	}
	if s.StartAt != "" {
		writeString(b, "start_at", s.StartAt)
	}
	if s.AlignToStart {
		b.WriteString("align_to_start = true\n")
	}
	if s.DailyTime != "" {
		writeString(b, "daily_time", s.DailyTime)
	}
	if s.Cron != "" {
		writeString(b, "cron", s.Cron)
	}
}

func writeEndpoint(b *bytes.Buffer, name string, e Endpoint) {
	empty := e.Backend == "" && e.Root == "" && len(e.Options) == 0
	if empty {
		return
	}
	b.WriteString("\n[" + name + "]\n")
	if e.Backend != "" {
		writeString(b, "backend", e.Backend)
	}
	if e.Root != "" {
		writeString(b, "root", e.Root)
	}
	if len(e.Options) > 0 {
		keys := make([]string, 0, len(e.Options))
		for k := range e.Options {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString("\n[" + name + ".options]\n")
		for _, k := range keys {
			writeString(b, k, e.Options[k])
		}
	}
}

// HasComments reports whether the file carries comment lines.
//
// Comments are the only information the structured form genuinely cannot hold.
// Blank lines and key ordering differ from the canonical layout too, but those
// are cosmetic: they carry nothing the user wrote to be read later, so treating
// them as loss made every hand-written file "lossy" and the warning worthless.
func HasComments(raw []byte) bool {
	for _, line := range bytes.Split(normalizeNewlines(raw), []byte("\n")) {
		if bytes.HasPrefix(bytes.TrimSpace(line), []byte("#")) {
			return true
		}
	}
	return false
}

func normalizeNewlines(b []byte) []byte {
	return bytes.ReplaceAll(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n")), []byte("\r"), []byte("\n"))
}

// SaveOptions controls how a config is written.
type SaveOptions struct {
	// Force allows the write even when it would drop comments.
	Force bool
}

// SaveResult reports what actually happened.
type SaveResult struct {
	// Lossy is true when the rewrite discarded comments.
	Lossy bool
	// BackupPath is the .bak file that holds the previous content, if any.
	BackupPath string
	// Bytes is the size of the new file.
	Bytes int
}

// Save validates cfg and writes it to path.
func Save(cfg *Config, path string, opt SaveOptions) (SaveResult, error) {
	var res SaveResult

	previous, readErr := os.ReadFile(path)
	existed := readErr == nil
	if readErr != nil && !os.IsNotExist(readErr) {
		return res, fmt.Errorf("read %s: %w", path, readErr)
	}

	// Detect the lossy case on the ORIGINAL file, before any change.
	if existed {
		res.Lossy = HasComments(previous)
		if res.Lossy && !opt.Force {
			return res, fmt.Errorf(
				"%w (%s). Edit the file directly, or repeat with force to accept losing them",
				ErrLossyRewrite, path)
		}
	}

	// Encode first, then re-parse what we encoded. Catching a self-inconsistent
	// result here is the difference between "the UI saved something odd" and
	// "the next unattended run deleted files".
	encoded, err := Encode(cfg)
	if err != nil {
		return res, err
	}
	check, err := parseBytes(path, encoded)
	if err != nil {
		return res, fmt.Errorf("the edited config is not valid: %w", err)
	}
	if err := check.validate(); err != nil {
		return res, fmt.Errorf("the edited config is not valid: %w", err)
	}
	// The bytes must mean exactly what the caller asked for. An encoder bug that
	// dropped a pair or reset max_delete would otherwise be invisible.
	if !sameConfig(cfg, check) {
		return res, fmt.Errorf(
			"internal error: the written form of the config differs from what was " +
				"requested, so nothing was saved (this is a bug in the job encoder)")
	}

	// Keep the previous content before replacing it.
	if existed {
		bak := path + ".bak"
		if err := writeAtomic(bak, previous); err != nil {
			return res, fmt.Errorf("back up %s: %w", path, err)
		}
		res.BackupPath = bak
	}

	if err := writeAtomic(path, encoded); err != nil {
		return res, err
	}
	res.Bytes = len(encoded)
	return res, nil
}

// sameConfig compares the fields that decide behaviour, ignoring the ones
// parseBytes normalises (derived pair names).
func sameConfig(want, got *Config) bool {
	if len(want.Pair) != len(got.Pair) {
		return false
	}
	if !sameJob(want.Job, got.Job) {
		return false
	}
	for i := range want.Pair {
		if !samePair(want.Pair[i], got.Pair[i]) {
			return false
		}
	}
	return true
}

func sameJob(a, b Job) bool {
	if a.Name != b.Name || a.Variant != b.Variant || !sameSync(a.Sync, b.Sync) {
		return false
	}
	if (a.Enabled == nil) != (b.Enabled == nil) {
		return false
	}
	if a.Enabled != nil && *a.Enabled != *b.Enabled {
		return false
	}
	if a.Schedule.Mode != b.Schedule.Mode || a.Schedule.Interval != b.Schedule.Interval ||
		a.Schedule.StartAt != b.Schedule.StartAt ||
		a.Schedule.AlignToStart != b.Schedule.AlignToStart ||
		a.Schedule.DailyTime != b.Schedule.DailyTime || a.Schedule.Cron != b.Schedule.Cron {
		return false
	}
	return true
}

func samePair(a, b Pair) bool {
	return a.Name == b.Name && a.Variant == b.Variant &&
		sameEndpoint(a.Local, b.Local) && sameEndpoint(a.Remote, b.Remote) &&
		sameSync(a.Sync, b.Sync)
}

func sameEndpoint(a, b Endpoint) bool {
	if a.Backend != b.Backend || a.Root != b.Root || len(a.Options) != len(b.Options) {
		return false
	}
	for k, v := range a.Options {
		if b.Options[k] != v {
			return false
		}
	}
	return true
}

func sameSync(a, b SyncSpec) bool {
	if a.Deletion != b.Deletion || a.VersioningDir != b.VersioningDir ||
		a.VersioningNaming != b.VersioningNaming || a.Conflict != b.Conflict ||
		a.MaxDeleteSize != b.MaxDeleteSize {
		return false
	}
	if a.PropagateLocalDelete != b.PropagateLocalDelete ||
		a.PropagateRemoteDelete != b.PropagateRemoteDelete ||
		a.PullNewFiles != b.PullNewFiles || a.MaxPullSize != b.MaxPullSize {
		return false
	}
	// Pointer semantics: absent, 0 and n are three different settings here, so a
	// dropped field cannot be caught by comparing the dereferenced values.
	if (a.MaxNodesPerDir == nil) != (b.MaxNodesPerDir == nil) {
		return false
	}
	if a.MaxNodesPerDir != nil && *a.MaxNodesPerDir != *b.MaxNodesPerDir {
		return false
	}
	if a.Compare != b.Compare {
		return false
	}
	if !sameStrings(a.Filter.Include, b.Filter.Include) ||
		!sameStrings(a.Filter.Exclude, b.Filter.Exclude) {
		return false
	}
	if (a.MaxDelete == nil) != (b.MaxDelete == nil) {
		return false
	}
	if a.MaxDelete != nil && *a.MaxDelete != *b.MaxDelete {
		return false
	}
	return true
}

// writeAtomic writes via a temporary file in the same directory, then renames.
//
// A partially written job file would be read by the next run as "corrupt" at
// best; at worst a truncated max_delete could change what the guard permits.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}

	tmp := path + ".tmp"
	fh, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}
	if _, err := fh.Write(data); err != nil {
		_ = fh.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := fh.Sync(); err != nil {
		_ = fh.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("sync %s: %w", tmp, err)
	}
	if err := fh.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close %s: %w", tmp, err)
	}

	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// sameStrings compares two string slices element by element.
//
// Order matters here: filter patterns are additive and order-independent to
// rclone, but a rewrite that reordered them would still be a change the user did
// not ask for, and a save that silently reorders is one more thing to explain.
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
