// Package job turns a configuration file into a series of guarded sync runs.
//
// Why a separate layer: the backend can upload and delete, but it cannot know
// whether deleting 900 files is a legitimate cleanup or a mistyped local path.
// That judgement needs a whole-pair view, which is what this package adds. The
// safety gate (see run.go) exists because the destination is Feishu, where a
// delete is permanent and there is no trash.
package job

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/rclone/rclone/fs"
)

// Sync variants, named after FreeFileSync's (docs/decisions.md D14).
const (
	// VariantUpdate only adds and overwrites; it never deletes.
	VariantUpdate = "update"
	// VariantMirror makes the destination an exact copy, deleting extras.
	VariantMirror = "mirror"
	// VariantTwoWay and VariantCustom are recognised so the error message can be
	// specific, but they are not implemented yet.
	VariantTwoWay = "two-way"
	VariantCustom = "custom"
)

// Deletion strategies (FreeFileSync's "Delete and overwrite").
const (
	// DeletionPermanent removes the file outright. Irreversible on Feishu.
	DeletionPermanent = "permanent"
	// DeletionVersioning moves the old copy into a trash folder first.
	DeletionVersioning = "versioning"
)

// Versioning naming conventions (FreeFileSync's three).
const (
	NamingReplace       = "replace"
	NamingTimestampFile = "timestamp-file"
	NamingTimestampDir  = "timestamp-folder"
)

// Conflict policies (PLAN.md §4.2, decided by the user 2026-11-05).
const (
	// ConflictReport touches nothing on the remote and exits non-zero.
	ConflictReport = "report"
	// ConflictOverwrite lets the sync replace the remote copy (share links change).
	ConflictOverwrite = "overwrite"
	// ConflictKeepBoth uploads the local copy under a new name and leaves the
	// remote original alone.
	ConflictKeepBoth = "keep-both"
)

// Compare strategies (FreeFileSync's three, R23).
//
// The sync itself always decides with the state snapshot, because Feishu reports
// no size and no hash for a listing (PLAN.md §4.2) - comparing remote metadata is
// not possible there at all. These settings exist for the *comparison view*, so the
// user can choose how much certainty to buy before pressing sync:
//
//	CompareTimeSize  the default: what the snapshot remembers (fast, local only)
//	CompareSize      ignore the timestamp, treat same-size as unchanged
//	CompareContent   download each differing file and compare bytes
//
// CompareContent is expensive on a cloud backend - it downloads every file it
// checks. It is offered anyway because the choice belongs to the user, not to us;
// the console says what it costs so the decision is informed.
const (
	CompareTimeSize = "time-size"
	CompareSize     = "size-only"
	CompareContent  = "content"
)

// DefaultCompare is the cheapest strategy that is still correct for a snapshot.
const DefaultCompare = CompareTimeSize

// CompareStrategies lists the valid values, in the order a form should show them.
var CompareStrategies = []string{CompareTimeSize, CompareSize, CompareContent}

// Defaults. max_delete comes from the user's decision (PLAN.md A13); the variant
// default is update because a sync tool that silently deletes is a worse failure
// than one that keeps too much.
const (
	DefaultVariant        = VariantUpdate
	DefaultDeletion       = DeletionPermanent
	DefaultVersioningDir  = "_weftsync_trash"
	DefaultVersioningName = NamingTimestampDir
	DefaultMaxDelete      = int64(50)
	DefaultConflict       = ConflictReport
)

// Config is one job file: a job, its defaults, and the pairs it covers.
type Config struct {
	Job  Job    `toml:"job"`
	Pair []Pair `toml:"pair"`
}

// NewConfig returns an empty config for a new job, with the safe defaults.
func NewConfig(name string) *Config {
	return &Config{
		Job: Job{
			Name:    name,
			Variant: DefaultVariant,
		},
	}
}

// Job is the job-level header and the defaults every pair inherits.
type Job struct {
	Name    string   `toml:"name"`
	Enabled *bool    `toml:"enabled"`
	Variant string   `toml:"variant"`
	Sync    SyncSpec `toml:"sync"`
	Pair    []Pair   `toml:"pair"`
	// Schedule is parsed and validated but only acted on by the daemon (F5).
	Schedule ScheduleSpec `toml:"schedule"`
}

// Pair maps one local directory onto one remote directory.
type Pair struct {
	Name    string   `toml:"name"`
	Variant string   `toml:"variant"`
	Local   Endpoint `toml:"local"`
	Remote  Endpoint `toml:"remote"`
	Sync    SyncSpec `toml:"sync"`
}

// Endpoint is one side of a pair.
type Endpoint struct {
	// Backend is the rclone remote name, e.g. "feishu". Optional: leave it empty
	// and put a complete rclone path in Root instead.
	Backend string `toml:"backend"`
	// Root is the directory. Combined with Backend it becomes "<backend>:<root>".
	Root string `toml:"root"`
	// Options are extra backend options applied to this pair only, e.g.
	// state_file. They are restricted to a whitelist: see allowedRemoteOptions.
	Options map[string]string `toml:"options"`
}

// SyncSpec holds the settings that decide whether and how much gets deleted.
type SyncSpec struct {
	Deletion         string `toml:"deletion"`
	VersioningDir    string `toml:"versioning_dir"`
	VersioningNaming string `toml:"versioning_naming"`
	// Conflict decides what happens when the remote no longer matches the
	// snapshot. Defaults to report, which changes nothing.
	Conflict string `toml:"conflict"`
	// MaxDelete is a pointer so "unset" and "0" stay distinguishable: 0 means
	// "delete nothing at all", which is a legitimate choice.
	MaxDelete *int64 `toml:"max_delete"`

	// MaxDeleteSize caps the total bytes one run may delete, e.g. "1G". An
	// empty string means no byte limit.
	MaxDeleteSize string `toml:"max_delete_size"`

	// The three two-way propagation switches. All default to false, and each is
	// separate because they fail very differently:
	//
	//	PropagateLocalDelete   local gone -> delete the cloud copy; undoable when
	//	                       deletion = "versioning", otherwise permanent.
	//	PropagateRemoteDelete  cloud gone -> delete the local file; NOT undoable.
	//	PullNewFiles           cloud gained it -> download; writes cloud content
	//	                       onto the local disk.
	//
	// Turning one on at a time is the only way the blast radius stays readable.
	PropagateLocalDelete  bool `toml:"propagate_local_delete"`
	PropagateRemoteDelete bool `toml:"propagate_remote_delete"`
	PullNewFiles          bool `toml:"pull_new_files"`
	// MaxPullSize bounds one downloaded new file, e.g. "100M". Empty means
	// DefaultMaxPullSize.
	MaxPullSize string `toml:"max_pull_size"`

	// MaxNodesPerDir overrides how many direct children one destination directory
	// may hold before the run is refused.
	//
	// A pointer because there are three distinct meanings and two of them are 0-ish:
	//
	//	absent  -> ask the backend (Feishu: 1500, refused at 1400)
	//	0       -> no limit at all; skip the pre-flight check
	//	n > 0   -> refuse above n
	//
	// The third state is the point. Without it there was no way to say "this
	// backend has no limit", so the Feishu number applied to every pair - and a
	// local -> local sync of 1401 files was refused for exceeding a limit that
	// does not exist on a local disk.
	MaxNodesPerDir *int `toml:"max_nodes_per_dir"`

	// Compare decides how the comparison view decides "unchanged" (R23).
	// Empty means DefaultCompare.
	Compare string `toml:"compare"`

	// Filter holds this pair's own filter patterns, on top of the job-level ones.
	//
	// Include is a whitelist: when non-empty, only matching paths are considered.
	// Exclude removes paths. Both use rclone filter syntax, and the daemon's global
	// patterns apply on top.
	//
	// Kept per pair because "which files matter" is a property of the folder, not
	// of the job: one pair may sync only *.xlsx while another syncs everything.
	Filter FilterSpec `toml:"filter"`
}

// FilterSpec is one pair's (or the job's) filter.
//
// Separate slices rather than one ordered list: rclone's filter object applies
// include-then-exclude regardless of the order the patterns were added, so
// preserving an interleaved order would promise a precedence that does not exist.
type FilterSpec struct {
	Include []string `toml:"include"`
	Exclude []string `toml:"exclude"`
}

// Load reads and validates a job file.
//
// Validation is deliberately strict: an unknown variant is an error rather than
// a fallback, because falling back from "mirorr" to update would silently stop
// syncing deletions, and falling back to mirror would start deleting.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read job file: %w", err)
	}
	return parseBytes(path, raw)
}

// parseBytes decodes and validates job file content.
//
// Shared by Load and Save so that what the UI writes is checked by exactly the
// same rules as what the daemon reads. A second, slightly different validator is
// how a bad config slips through.
func parseBytes(path string, raw []byte) (*Config, error) {
	var cfg Config
	dec := toml.NewDecoder(bytes.NewReader(raw))
	// Reject unknown keys so a typo cannot silently disable a safety setting.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	// Pairs written directly under [[pair]] at the top level are accepted as a
	// convenience; fold them in so callers only look at one slice.
	if len(cfg.Pair) == 0 {
		cfg.Pair = cfg.Job.Pair
	}
	cfg.Job.Pair = nil

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &cfg, nil
}

func (c *Config) validate() error {
	if len(c.Pair) == 0 {
		return fmt.Errorf("no [[pair]] defined")
	}

	// The schedule must be resolvable here, not only when the daemon starts.
	// Load and Save both route through validate, so checking it once keeps a
	// file we accept from being one the daemon refuses to boot on.
	if _, err := c.EffectiveSchedule(); err != nil {
		return err
	}

	seen := map[string]bool{}
	for i := range c.Pair {
		p := &c.Pair[i]
		if p.Name == "" {
			p.Name = fmt.Sprintf("pair-%d", i+1)
		}
		if seen[p.Name] {
			return fmt.Errorf("duplicate pair name %q", p.Name)
		}
		seen[p.Name] = true

		if strings.TrimSpace(p.Local.Root) == "" {
			return fmt.Errorf("pair %q: local.root is required", p.Name)
		}
		if strings.TrimSpace(p.Remote.Root) == "" && strings.TrimSpace(p.Remote.Backend) == "" {
			return fmt.Errorf("pair %q: remote.root is required", p.Name)
		}
		if _, err := p.EffectiveVariant(c); err != nil {
			return err
		}
		if _, err := p.EffectiveSync(c); err != nil {
			return err
		}
		if err := p.validateOptions(); err != nil {
			return err
		}
	}
	return nil
}

// EffectiveVariant resolves the variant for a pair, job default first.
func (p *Pair) EffectiveVariant(c *Config) (string, error) {
	v := p.Variant
	if v == "" {
		v = c.Job.Variant
	}
	if v == "" {
		v = DefaultVariant
	}
	switch v {
	case VariantUpdate, VariantMirror, VariantTwoWay:
		return v, nil
	case VariantCustom:
		return "", fmt.Errorf(
			"pair %q: variant %q is recognised but not implemented yet. "+
				"Use %q, %q or %q", p.Name, v, VariantUpdate, VariantMirror, VariantTwoWay)
	default:
		return "", fmt.Errorf(
			"pair %q: unknown variant %q (expected %q, %q or %q)",
			p.Name, v, VariantUpdate, VariantMirror, VariantTwoWay)
	}
}

// EffectiveSync merges the pair's settings over the job's defaults.
func (p *Pair) EffectiveSync(c *Config) (ResolvedSync, error) {
	out := ResolvedSync{
		Deletion:         firstNonEmpty(p.Sync.Deletion, c.Job.Sync.Deletion, DefaultDeletion),
		VersioningDir:    firstNonEmpty(p.Sync.VersioningDir, c.Job.Sync.VersioningDir, DefaultVersioningDir),
		VersioningNaming: firstNonEmpty(p.Sync.VersioningNaming, c.Job.Sync.VersioningNaming, DefaultVersioningName),
		Conflict:         firstNonEmpty(p.Sync.Conflict, c.Job.Sync.Conflict, DefaultConflict),
		MaxDelete:        DefaultMaxDelete,
	}
	if p.Sync.MaxDelete != nil {
		out.MaxDelete = *p.Sync.MaxDelete
	} else if c.Job.Sync.MaxDelete != nil {
		out.MaxDelete = *c.Job.Sync.MaxDelete
	}

	switch out.Deletion {
	case DeletionPermanent:
	case DeletionVersioning:
		dir := strings.Trim(out.VersioningDir, "/")
		if dir == "" {
			return out, fmt.Errorf("pair %q: versioning_dir must not be empty", p.Name)
		}
		if strings.Contains(dir, ":") || filepath.IsAbs(dir) {
			return out, fmt.Errorf(
				"pair %q: versioning_dir %q must be a relative directory inside the "+
					"destination; an outside path would break the guarantee that the trash "+
					"travels with the pair", p.Name, out.VersioningDir)
		}
		switch out.VersioningNaming {
		case NamingReplace, NamingTimestampFile, NamingTimestampDir:
		default:
			return out, fmt.Errorf(
				"pair %q: unknown versioning_naming %q (expected %q, %q or %q)",
				p.Name, out.VersioningNaming, NamingReplace, NamingTimestampFile, NamingTimestampDir)
		}
	default:
		return out, fmt.Errorf(
			"pair %q: unknown deletion %q (expected %q or %q)",
			p.Name, out.Deletion, DeletionPermanent, DeletionVersioning)
	}

	if out.MaxDelete < 0 {
		return out, fmt.Errorf(
			"pair %q: max_delete must be >= 0 (0 refuses every deletion); "+
				"negative values are not a way to disable the guard", p.Name)
	}

	// The byte guard is the count guard's sibling: 5000 tiny files and one 40 GiB
	// file are both "a few deletions", but only one of them is survivable.
	// The value is parsed here so a typo is rejected when the file is saved
	// rather than discovered by whichever pass would have deleted things.
	size := firstNonEmpty(p.Sync.MaxDeleteSize, c.Job.Sync.MaxDeleteSize)
	if size != "" {
		var parsed fs.SizeSuffix
		if err := parsed.Set(size); err != nil {
			return out, fmt.Errorf(
				"pair %q: max_delete_size %q is not a size (try \"500M\", \"1G\" or \"0\"): %w",
				p.Name, size, err)
		}
		if parsed < 0 {
			return out, fmt.Errorf(
				"pair %q: max_delete_size must be >= 0 (0 refuses every deletion)", p.Name)
		}
		out.MaxDeleteSize = int64(parsed)
		out.MaxDeleteSizeText = size
	}

	switch out.Conflict {
	case ConflictReport, ConflictOverwrite, ConflictKeepBoth:
	default:
		return out, fmt.Errorf(
			"pair %q: unknown conflict policy %q (expected %q, %q or %q)",
			p.Name, out.Conflict, ConflictReport, ConflictOverwrite, ConflictKeepBoth)
	}

	// The three two-way switches. A pair can set them; the job-level values act
	// as defaults, like every other sync setting.
	out.PropagateLocalDelete = pickBool(p.Sync.PropagateLocalDelete, c.Job.Sync.PropagateLocalDelete)
	out.PropagateRemoteDelete = pickBool(p.Sync.PropagateRemoteDelete, c.Job.Sync.PropagateRemoteDelete)
	out.PullNewFiles = pickBool(p.Sync.PullNewFiles, c.Job.Sync.PullNewFiles)

	if size := firstNonEmpty(p.Sync.MaxPullSize, c.Job.Sync.MaxPullSize); size != "" {
		var parsed fs.SizeSuffix
		if err := parsed.Set(size); err != nil {
			return out, fmt.Errorf(
				"pair %q: max_pull_size %q is not a size (try \"100M\" or \"1G\"): %w",
				p.Name, size, err)
		}
		if parsed <= 0 {
			return out, fmt.Errorf(
				"pair %q: max_pull_size must be > 0; leave it unset to use the default", p.Name)
		}
		out.MaxPullSize = int64(parsed)
		out.MaxPullSizeText = size
	}

	// These switches only mean something for a two-way pair. Silently ignoring
	// them would leave somebody believing deletions are propagating when they are
	// not, which is the worst possible misunderstanding for this feature.
	if out.PropagateLocalDelete || out.PropagateRemoteDelete || out.PullNewFiles {
		if v, _ := p.EffectiveVariant(c); v != VariantTwoWay {
			return out, fmt.Errorf(
				"pair %q: propagate_local_delete / propagate_remote_delete / pull_new_files "+
					"only apply to variant = %q, but this pair is %q. "+
					"Either change the variant or remove the switches",
				p.Name, VariantTwoWay, v)
		}
	}

	// The per-directory cap. Negative is rejected rather than clamped: it would
	// otherwise read as "no limit" (any <= 0 hits that branch), which is the least
	// safe possible interpretation of a typo in a safety setting.
	out.MaxNodesPerDir = p.Sync.MaxNodesPerDir
	if out.MaxNodesPerDir == nil {
		out.MaxNodesPerDir = c.Job.Sync.MaxNodesPerDir
	}
	if out.MaxNodesPerDir != nil && *out.MaxNodesPerDir < 0 {
		return out, fmt.Errorf(
			"pair %q: max_nodes_per_dir must be >= 0 (0 means \"no limit\"; "+
				"leave it unset to use the backend's own limit)", p.Name)
	}

	// The comparison strategy. Validated here so a typo is rejected when the file
	// is saved rather than when a preview silently falls back to the default.
	out.Compare = firstNonEmpty(p.Sync.Compare, c.Job.Sync.Compare, DefaultCompare)
	switch out.Compare {
	case CompareTimeSize, CompareSize, CompareContent:
	default:
		return out, fmt.Errorf(
			"pair %q: unknown compare %q (expected %q, %q or %q)",
			p.Name, out.Compare, CompareTimeSize, CompareSize, CompareContent)
	}

	// Filters: the job-level patterns come first, then the pair's own, because the
	// pair is the more specific scope. Both are additive within a level - an
	// include list is a whitelist, so adding one never widens what is synced.
	out.Include = append(append([]string{}, c.Job.Sync.Filter.Include...),
		p.Sync.Filter.Include...)
	out.Exclude = append(append([]string{}, c.Job.Sync.Filter.Exclude...),
		p.Sync.Filter.Exclude...)
	for _, pat := range append(append([]string{}, out.Include...), out.Exclude...) {
		if strings.TrimSpace(pat) == "" {
			return out, fmt.Errorf(
				"pair %q: empty filter pattern (remove the blank line or write the pattern)",
				p.Name)
		}
	}

	return out, nil
}

// pickBool lets a pair override a job-level boolean; the pair value wins only
// when it is true, because "false" is indistinguishable from "unset" in a TOML
// bool. That is the same trade-off max_delete avoids with a pointer, and here it
// is the right one: these switches turn behaviour ON, so failing to see one must
// mean "off".
func pickBool(pair, jobLevel bool) bool { return pair || jobLevel }

// ResolvedSync is the effective setting for one pair after merging defaults.
type ResolvedSync struct {
	Deletion         string
	VersioningDir    string
	VersioningNaming string
	Conflict         string
	MaxDelete        int64
	// MaxDeleteSize is the byte ceiling for one run, 0 for "no ceiling".
	MaxDeleteSize int64
	// MaxDeleteSizeText is the same value as the user wrote it, so the
	// configuration form can show "1G" instead of 1073741824.
	MaxDeleteSizeText string

	// The two-way propagation switches; see SyncSpec for what each one does.
	PropagateLocalDelete  bool
	PropagateRemoteDelete bool
	PullNewFiles          bool
	// MaxPullSize is the per-file download ceiling in bytes, 0 for the default.
	MaxPullSize int64
	// MaxPullSizeText is the same value as the user wrote it, so the form shows
	// "250M" instead of "250Mi".
	MaxPullSizeText string

	// MaxNodesPerDir is the per-directory child cap resolved from the job file.
	// nil means "ask the backend"; a non-nil 0 means "no limit, skip the check".
	MaxNodesPerDir *int

	// Compare is the resolved comparison strategy (R23).
	Compare string

	// Include / Exclude are the resolved filter patterns: the job-level ones plus
	// this pair's own. Include is a whitelist; both use rclone filter syntax.
	Include []string
	Exclude []string
}

// LocalPath is the rclone path for the local side.
func (p *Pair) LocalPath() string { return p.Local.Root }

// allowedRemoteOptions is the whitelist of backend options a job file may set.
//
// The reason for a whitelist rather than "anything goes": options are folded
// into the remote's name, and rclone prints names in logs and error messages. A
// secret put here would therefore end up in plain text in logs and bug reports.
// Credentials belong in rclone.conf or the documented environment variables.
//
// state_file is here because it is the one option that MUST vary per pair: the
// rclone global flag applies to every remote in the process, so several pairs in
// one job would otherwise share a single snapshot and corrupt each other's
// incremental decisions.
var allowedRemoteOptions = map[string]string{
	"state_file": "path to the incremental-sync snapshot for this pair",
}

// secretOptionHints catches the mistake even when the key is spelled unusually.
var secretOptionHints = []string{"secret", "password", "passwd", "token", "key", "credential"}

// validateOptions rejects unknown or secret-looking backend options.
func (p *Pair) validateOptions() error {
	for k, v := range p.Remote.Options {
		if _, ok := allowedRemoteOptions[k]; !ok {
			lower := strings.ToLower(k)
			for _, hint := range secretOptionHints {
				if strings.Contains(lower, hint) {
					return fmt.Errorf(
						"pair %q: remote.options[%q] looks like a credential. "+
							"Credentials must not go in a job file: rclone prints remote names "+
							"in logs, and this file is easy to commit by accident. "+
							"Put it in rclone.conf or use the matching environment variable",
						p.Name, k)
				}
			}
			keys := make([]string, 0, len(allowedRemoteOptions))
			for name := range allowedRemoteOptions {
				keys = append(keys, name)
			}
			sort.Strings(keys)
			return fmt.Errorf(
				"pair %q: remote.options[%q] is not settable from a job file. "+
					"Allowed: %s", p.Name, k, strings.Join(keys, ", "))
		}
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("pair %q: remote.options[%q] is empty", p.Name, k)
		}
	}
	if len(p.Remote.Options) > 0 && strings.TrimSpace(p.Remote.Backend) == "" {
		return fmt.Errorf(
			"pair %q: remote.options requires remote.backend to name an rclone remote; "+
				"a bare remote.root path cannot carry options", p.Name)
	}
	return nil
}

// quoteOptionValue renders a value for rclone's connection-string parser.
//
// Values are always quoted: an unquoted Windows path such as C:\x parses as
// value "C" followed by garbage, because ':' ends an unquoted value. Embedded
// quotes are doubled, which is what the parser undoes.
func quoteOptionValue(v string) string {
	return `"` + strings.ReplaceAll(v, `"`, `""`) + `"`
}

// RemotePath is the rclone path for the remote side.
//
// A bare backend name plus a root becomes "<backend>:<root>", matching the way
// rclone remotes are addressed. If Backend is empty the root has to be a
// complete path.
//
// When per-pair options are present they are folded in as
// "<backend>,key="value":<root>". Verified: that form still reads every other
// option from the same-named config section, so credentials keep coming from
// rclone.conf (probe: overriding root_token changed only that key while
// authentication still succeeded).
func (p *Pair) RemotePath() (string, error) {
	root := strings.TrimSpace(p.Remote.Root)
	backend := strings.TrimSpace(p.Remote.Backend)

	if backend == "" {
		if !strings.Contains(root, ":") {
			return "", fmt.Errorf(
				"pair %q: remote.root %q is not an rclone path and remote.backend is empty; "+
					"set backend, or write root as \"<remote>:<path>\"", p.Name, root)
		}
		return root, nil
	}
	if strings.Contains(backend, ":") {
		return "", fmt.Errorf("pair %q: remote.backend %q must be a remote name, not a path",
			p.Name, backend)
	}
	if len(p.Remote.Options) == 0 {
		return backend + ":" + root, nil
	}
	return buildRemotePath(p.Name, backend, root, p.Remote.Options)
}

// RemoteDisplay is RemotePath without the backend options.
//
// The result is what a human should see: options are folded into the connection
// string, which turns a perfectly ordinary remote into
// `test,state_file="C:/Users/…/state.json":folder` - unreadable in a table, and
// it publishes a local absolute path into a web page. Options are an
// implementation detail of how the Fs is opened, not something to show.
func (p *Pair) RemoteDisplay() string {
	root := strings.TrimSpace(p.Remote.Root)
	backend := strings.TrimSpace(p.Remote.Backend)
	if backend == "" {
		return root
	}
	return backend + ":" + root
}

// buildRemotePath assembles "<backend>,k="v",k2="v2":<root>" in a stable order.
func buildRemotePath(pairName, backend, root string, opts map[string]string) (string, error) {
	keys := make([]string, 0, len(opts))
	for k := range opts {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic output keeps tests and logs stable

	var b strings.Builder
	b.WriteString(backend)
	for _, k := range keys {
		b.WriteString(",")
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(quoteOptionValue(opts[k]))
	}
	b.WriteString(":")

	// The root is appended verbatim; it is never quoted. A root containing ':'
	// is fine here because the parser is already in its final state.
	b.WriteString(root)
	return b.String(), nil
}

// Enabled reports whether the job should run.
func (c *Config) Enabled() bool {
	return c.Job.Enabled == nil || *c.Job.Enabled
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
