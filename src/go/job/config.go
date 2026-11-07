// Package job turns a configuration file into a series of guarded sync runs.
//
// Why a separate layer: the backend can upload and delete, but it cannot know
// whether deleting 900 files is a legitimate cleanup or a mistyped local path.
// That judgement needs a whole-pair view, which is what this package adds. The
// safety gate (see run.go) exists because the destination is Feishu, where a
// delete is permanent and there is no trash.
package job

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
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

// Defaults. max_delete comes from the user's decision (PLAN.md A13); the variant
// default is update because a sync tool that silently deletes is a worse failure
// than one that keeps too much.
const (
	DefaultVariant        = VariantUpdate
	DefaultDeletion       = DeletionPermanent
	DefaultVersioningDir  = "_weftsync_trash"
	DefaultVersioningName = NamingTimestampDir
	DefaultMaxDelete      = int64(50)
)

// Config is one job file: a job, its defaults, and the pairs it covers.
type Config struct {
	Job  Job    `toml:"job"`
	Pair []Pair `toml:"pair"`
}

// Job is the job-level header and the defaults every pair inherits.
type Job struct {
	Name    string   `toml:"name"`
	Enabled *bool    `toml:"enabled"`
	Variant string   `toml:"variant"`
	Sync    SyncSpec `toml:"sync"`
	Pair    []Pair   `toml:"pair"`
	// Schedule is parsed but unused until the scheduler exists (F5). It is
	// accepted now so an existing file is not rejected for carrying it.
	Schedule map[string]any `toml:"schedule"`
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
	// MaxDelete is a pointer so "unset" and "0" stay distinguishable: 0 means
	// "delete nothing at all", which is a legitimate choice.
	MaxDelete *int64 `toml:"max_delete"`

	// MaxDeleteSize is not implemented yet; see docs/roadmap.md F3.
	MaxDeleteSize string `toml:"max_delete_size"`
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

	var cfg Config
	dec := toml.NewDecoder(strings.NewReader(string(raw)))
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
	case VariantUpdate, VariantMirror:
		return v, nil
	case VariantTwoWay, VariantCustom:
		return "", fmt.Errorf(
			"pair %q: variant %q is recognised but not implemented yet. "+
				"Use %q or %q", p.Name, v, VariantUpdate, VariantMirror)
	default:
		return "", fmt.Errorf(
			"pair %q: unknown variant %q (expected %q or %q)",
			p.Name, v, VariantUpdate, VariantMirror)
	}
}

// EffectiveSync merges the pair's settings over the job's defaults.
func (p *Pair) EffectiveSync(c *Config) (ResolvedSync, error) {
	out := ResolvedSync{
		Deletion:         firstNonEmpty(p.Sync.Deletion, c.Job.Sync.Deletion, DefaultDeletion),
		VersioningDir:    firstNonEmpty(p.Sync.VersioningDir, c.Job.Sync.VersioningDir, DefaultVersioningDir),
		VersioningNaming: firstNonEmpty(p.Sync.VersioningNaming, c.Job.Sync.VersioningNaming, DefaultVersioningName),
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
	return out, nil
}

// ResolvedSync is the effective setting for one pair after merging defaults.
type ResolvedSync struct {
	Deletion         string
	VersioningDir    string
	VersioningNaming string
	MaxDelete        int64
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
