package job

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/accounting"
	"github.com/rclone/rclone/fs/filter"
	rsync "github.com/rclone/rclone/fs/sync"

	"weftsync/split"
)

// GuardError means the run was stopped because it wanted to delete more than the
// pair allows.
//
// Nothing was deleted: the count comes from a dry-run pass, so the whole run is
// abandoned before any destructive call. That is deliberate and differs from
// rclone's own --max-delete, which deletes up to the limit and only then aborts
// (measured: --max-delete 1 against 2 pending deletions removed one file).
type GuardError struct {
	Pair  string
	Files int64
	Dirs  int64
	Limit int64
}

func (e *GuardError) Error() string {
	return fmt.Sprintf(
		"pair %q wanted to delete %d file(s) and %d empty director(ies), "+
			"but max_delete is %d. NOTHING was deleted. "+
			"If this is expected, raise max_delete for this pair; "+
			"if it is not, check that local.root points at the intended folder",
		e.Pair, e.Files, e.Dirs, e.Limit)
}

// SizeGuardError means the run was stopped because it wanted to free more bytes
// than the pair allows.
//
// rclone counts the bytes it deletes but does not expose the total through any
// read interface (StatsInfo.deletesSize is unexported and absent from
// RemoteStats), so the only way to measure it is to let rclone's own threshold
// do the comparing. That check runs before the destructive call, and this guard
// uses it on the dry pass only - which is why nothing is deleted.
type SizeGuardError struct {
	Pair  string
	Files int64
	Limit int64
}

func (e *SizeGuardError) Error() string {
	return fmt.Sprintf(
		"pair %q wanted to delete %d file(s), more than max_delete_size allows (%s). "+
			"NOTHING was deleted. If this is expected, raise max_delete_size for "+
			"this pair; if it is not, check that local.root points at the intended folder",
		e.Pair, e.Files, fs.SizeSuffix(e.Limit).ByteUnit())
}

// PassResult is what one sync pass did (or, for a dry run, would have done).
type PassResult struct {
	Transferred int64 `json:"transferred"`
	Deleted     int64 `json:"deleted"`
	DeletedDirs int64 `json:"deleted_dirs"`
	Errors      int64 `json:"errors"`
	// Bytes is how much was transferred, for showing a plan's size rather than
	// only its file count.
	Bytes int64 `json:"bytes"`
}

// Destructive reports the total number of destructive operations in the pass.
func (r PassResult) Destructive() int64 { return r.Deleted + r.DeletedDirs }

// ErrDeletionBlocked is unused here; deletions are gated by GuardError.

// ConflictError means the run stopped because the remote no longer matches the
// snapshot and the policy is to report rather than act.
type ConflictError struct {
	Pair  string
	Count int
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf(
		"pair %q has %d conflict(s) between the folder and the sync state. "+
			"NOTHING was uploaded, overwritten or deleted. "+
			"Review the report above, then either fix the folder in Feishu, "+
			"run `weftsync backend dedupe` for duplicate copies, "+
			"or set conflict = \"overwrite\" / \"keep-both\" for this pair",
		e.Pair, e.Count)
}

// PairResult is the outcome for one pair.
type PairResult struct {
	Pair      string     `json:"pair"`
	Variant   string     `json:"variant"`
	Local     string     `json:"local"`
	Remote    string     `json:"remote"`
	DryRun    PassResult `json:"dry_run"`
	Actual    PassResult `json:"actual"`
	GuardHit  bool       `json:"guard_hit"`
	Conflicts []Conflict `json:"conflicts,omitempty"`
	// Pulled counts files downloaded because the cloud replaced them (two-way).
	Pulled int `json:"pulled"`
	// Held counts conflicts a two-way run left for a human: deletions and files
	// that only exist in the cloud.
	Held int `json:"held"`
	// OverLimit names directories holding more direct children than the backend
	// accepts, so the run was refused before anything was uploaded.
	OverLimit []string `json:"over_limit,omitempty"`
}

// Options tunes a run.
type Options struct {
	// DryRun measures and reports without changing anything. In this mode the
	// guard still runs, so a plan can show that a real run would be blocked.
	DryRun bool
	// StatsGroup routes this run's counters into a named accounting group
	// instead of the global one.
	//
	// Without it, two jobs running at once report the sum of both, and "current
	// file" can name a file from the other job. Empty means global, which is what
	// the command line wants.
	StatsGroup string
	// MaxNodesPerDir is the direct-child count that makes a directory over-limit.
	// Zero means split.DefaultRefuseAt.
	MaxNodesPerDir int
	// Exclude holds extra filter patterns applied to every pair in this run.
	//
	// The daemon fills this from its settings so a user can exclude "*.tmp" or
	// ".git/**" once instead of per job. Patterns only ever exclude: there is no
	// way for a global pattern to re-include something a job's own filters
	// dropped, because "the job file is the authority" is easier to reason about
	// than a precedence table.
	Exclude []string
}

// Run executes every enabled pair in the job, in order.
//
// Pairs are independent: one failing does not stop the others, but the error is
// reported so the caller can exit non-zero.
func Run(ctx context.Context, cfg *Config, w io.Writer, opt Options) ([]PairResult, error) {
	if !cfg.Enabled() {
		fmt.Fprintf(w, "job %q is disabled; nothing to do\n", cfg.Job.Name)
		return nil, nil
	}
	if opt.StatsGroup != "" {
		// Everything below counts into this group instead of the global one.
		ctx = accounting.WithStatsGroup(ctx, opt.StatsGroup)
	}

	var (
		results []PairResult
		errs    []error
	)
	for i := range cfg.Pair {
		res, err := RunPair(ctx, cfg, &cfg.Pair[i], w, opt)
		results = append(results, res)
		if err != nil {
			errs = append(errs, err)
		}
	}
	return results, errors.Join(errs...)
}

// RunPair runs one pair, applying the delete guard when the variant deletes.
func RunPair(ctx context.Context, cfg *Config, p *Pair, w io.Writer, opt Options) (PairResult, error) {
	res := PairResult{Pair: p.Name}

	variant, err := p.EffectiveVariant(cfg)
	if err != nil {
		return res, err
	}
	spec, err := p.EffectiveSync(cfg)
	if err != nil {
		return res, err
	}
	remotePath, err := p.RemotePath()
	if err != nil {
		return res, err
	}
	res.Variant = variant
	res.Local = p.LocalPath()
	// Display form, not the connection string: the options folded into
	// RemotePath are an implementation detail and would publish a local path
	// into the web console.
	res.Remote = p.RemoteDisplay()

	// Pre-flight: refuse a directory holding more direct children than the backend
	// accepts. Feishu answers 1062507 for those, but only on the call that creates
	// or uploads into the folder - by which time part of the run has happened. A
	// local walk costs nothing in comparison, and it lets the run stop with a
	// message naming the directory instead of a bare API error.
	//
	// A preview does not fail on this: its job is to report what a real run would
	// do, and the answer is "be refused". The directories are listed either way so
	// the plan panel and the error agree.
	findings, err := split.Precheck(p.LocalPath(), split.PrecheckOptions{
		RefuseAt: opt.MaxNodesPerDir,
	})
	if err != nil {
		return res, fmt.Errorf("pair %q: pre-flight check: %w", p.Name, err)
	}
	if len(findings) > 0 {
		for _, f := range findings {
			res.OverLimit = append(res.OverLimit, f.RelPath)
		}
		limitErr := &split.OverLimitError{
			Root: p.LocalPath(), Limit: refuseAt(opt.MaxNodesPerDir), Findings: findings,
		}
		if opt.DryRun {
			fmt.Fprintf(w, "note: pair %q: %v\n", p.Name, limitErr)
		} else {
			return res, fmt.Errorf("pair %q: %w", p.Name, limitErr)
		}
	}

	fdst, err := fs.NewFs(ctx, remotePath)
	if err != nil {
		return res, fmt.Errorf("pair %q: open destination %q: %w", p.Name, remotePath, err)
	}
	fsrc, err := fs.NewFs(ctx, p.LocalPath())
	if err != nil {
		return res, fmt.Errorf("pair %q: open source %q: %w", p.Name, p.LocalPath(), err)
	}

	// Conflicts come first: if the remote no longer matches the snapshot, running
	// either pass would push our version over somebody else's change.
	if variant == VariantTwoWay {
		// Two-way takes the conflicts apart instead of aborting on all of them:
		// a file the cloud replaced while the local copy sat still is exactly the
		// case this variant exists for. Everything else is reported and held.
		scan, scanErr := detectConflictsFn(ctx, fdst, "")
		if scanErr != nil {
			return res, fmt.Errorf("pair %q: %w", p.Name, scanErr)
		}
		if !scan.Available {
			fmt.Fprintf(w, "note: pair %q: conflicts cannot be checked (%s); "+
				"two-way needs a state_file, so this run can only upload\n",
				p.Name, scan.Summary)
		} else {
			plan := planPulls(ctx, fsrc, scan.Conflicts)
			pulled, pullErr := pullRemoteChanges(ctx, p, plan, opt.DryRun, w)
			res.Pulled = pulled
			res.Conflicts = plan.hold
			res.Held = len(plan.hold)
			if pullErr != nil {
				return res, fmt.Errorf("pair %q: %w", p.Name, pullErr)
			}
			if len(plan.hold) > 0 {
				reportConflicts(w, p.Name, plan.hold)
				// Held paths must leave the sync set as well, otherwise the upload
				// pass resurrects a file somebody deleted in the cloud - which is
				// the exact opposite of "left alone", and would make the report a
				// lie. Same rule the report policy already applies.
				paths := make([]string, 0, len(plan.hold))
				for _, c := range plan.hold {
					paths = append(paths, c.Path)
				}
				excluded, err := excludePaths(ctx, paths)
				if err != nil {
					return res, fmt.Errorf("pair %q: %w", p.Name, err)
				}
				ctx = excluded
				fmt.Fprintf(w, "  two-way: %d path(s) need a decision and were left alone "+
					"(deletions and cloud-only files are not propagated; see docs/decisions.md D43)\n",
					len(plan.hold))
			}
			if len(plan.pullable) > 0 {
				fmt.Fprintf(w, "  two-way: downloaded %d replaced file(s)\n", pulled)
			}
		}
	} else {
		handled, err := handleConflicts(ctx, p, spec, fdst, fsrc, w)
		res.Conflicts = handled.reported
		if err != nil {
			return res, err
		}
		if handled.abort {
			return res, &ConflictError{Pair: p.Name, Count: len(handled.reported)}
		}
		ctx = handled.ctx
	}

	// The trash folder name must be identical across both passes, otherwise the
	// dry run would be measuring a different plan from the one executed.
	opts := passOptions{
		backupDir: backupDirFor(remotePath, spec),
		suffix:    suffixFor(spec, time.Now()),
		exclude:   opt.Exclude,
	}

	if opts.backupDir != "" {
		var err error
		if ctx, err = withTrashExcluded(ctx, spec.VersioningDir); err != nil {
			return res, fmt.Errorf("pair %q: %w", p.Name, err)
		}
	}

	// Two-way runs no deletions at all in this stage: it behaves like update once
	// the cloud-side replacements have been downloaded. Propagating deletions is
	// the part that needs its own decision, so reaching the mirror branch here
	// would quietly turn "two-way" into "delete on both sides".
	if variant == VariantUpdate || variant == VariantTwoWay {
		// update never deletes, so there is nothing to guard against.
		actual, err := runPass(ctx, fdst, fsrc, opts, opt.DryRun, false)
		res.Actual = actual
		if opt.DryRun {
			res.DryRun = actual
		}
		return res, err
	}

	// Mirror: measure first, decide, then act.
	//
	// The byte ceiling is measured on this same pass, by leaving rclone's own
	// threshold switched on: its check runs before the destructive call and
	// aborts the pass, so a dry pass that trips it has still deleted nothing.
	dryOpts := opts
	dryOpts.maxDeleteSize = spec.MaxDeleteSize
	dry, err := runPass(ctx, fdst, fsrc, dryOpts, true, true)
	res.DryRun = dry
	if err != nil {
		// rclone reports a byte-ceiling trip as a plain "failed to delete N
		// files" and keeps the real cause in the log, so the error says nothing
		// about which threshold stopped it. Re-measure with the ceiling off: a
		// pass that then counts more deletions was stopped by the ceiling, one
		// that fails again was stopped by something real.
		if spec.MaxDeleteSize > 0 {
			if again, againErr := runPass(ctx, fdst, fsrc, opts, true, true); againErr == nil &&
				again.Deleted > dry.Deleted {
				res.DryRun = again
				res.GuardHit = true
				guard := &SizeGuardError{Pair: p.Name, Files: again.Deleted, Limit: spec.MaxDeleteSize}
				if opt.DryRun {
					res.Actual = again
					fmt.Fprintf(w, "note: pair %q: %v\n", p.Name, guard)
					return res, nil
				}
				return res, guard
			}
		}
		return res, fmt.Errorf(
			"pair %q: the pre-flight pass failed, so nothing was changed: %w", p.Name, err)
	}

	if dry.Destructive() > spec.MaxDelete {
		res.GuardHit = true
		if opt.DryRun {
			// A plan should report, not fail: the point is to show what a real
			// run would do.
			res.Actual = dry
			return res, nil
		}
		return res, &GuardError{
			Pair: p.Name, Files: dry.Deleted, Dirs: dry.DeletedDirs, Limit: spec.MaxDelete,
		}
	}

	if opt.DryRun {
		res.Actual = dry
		return res, nil
	}

	actual, err := runPass(ctx, fdst, fsrc, opts, false, true)
	res.Actual = actual
	if err != nil {
		return res, fmt.Errorf("pair %q: %w", p.Name, err)
	}
	if actual.Destructive() > spec.MaxDelete {
		// The plan changed between the two passes (someone else wrote to the
		// destination). Say so loudly instead of silently over-deleting.
		return res, fmt.Errorf(
			"pair %q: the destination changed between the pre-flight pass and the real "+
				"run: it deleted %d item(s) although max_delete is %d. "+
				"Raise max_delete or re-run once the destination is quiet",
			p.Name, actual.Destructive(), spec.MaxDelete)
	}
	return res, nil
}

// refuseAt resolves the configured threshold.
func refuseAt(configured int) int {
	if configured > 0 {
		return configured
	}
	return split.DefaultRefuseAt
}

type passOptions struct {
	backupDir string
	suffix    string
	// exclude holds the daemon-level filter patterns; see Options.Exclude.
	exclude []string
	// maxDeleteSize is the byte ceiling rclone checks before deleting, 0 for
	// none. It is only ever set on the dry pass; see SizeGuardError.
	maxDeleteSize int64
}

// conflictOutcome is what handleConflicts decided.
func handleConflicts(
	ctx context.Context,
	p *Pair,
	spec ResolvedSync,
	fdst, fsrc fs.Fs,
	w io.Writer,
) (conflictOutcome, error) {
	conflicts, err := detectConflicts(ctx, fdst, "")
	if err != nil {
		// Detection is best effort: a backend without the command returns none.
		// A real failure here must not silently become "no conflicts".
		return conflictOutcome{}, fmt.Errorf("pair %q: %w", p.Name, err)
	}
	if !conflicts.Available {
		// Not being able to check is not a failure. Say so once and carry on;
		// blocking the run here would make the default configuration (no
		// state_file) unusable.
		fmt.Fprintf(w, "note: pair %q: conflicts cannot be checked (%s)\n",
			p.Name, conflicts.Summary)
		return conflictOutcome{ctx: ctx}, nil
	}
	if len(conflicts.Conflicts) == 0 {
		return conflictOutcome{ctx: ctx}, nil
	}

	paths := reportConflicts(w, p.Name, conflicts.Conflicts)
	out := conflictOutcome{ctx: ctx, reported: conflicts.Conflicts}

	switch spec.Conflict {
	case ConflictOverwrite:
		// Let the engine proceed; it will upload over the conflicting names.
		fmt.Fprintf(w, "  conflict policy: overwrite - the local copy will replace the remote one\n")
		return out, nil

	case ConflictKeepBoth:
		// Upload our copy under a new name, leave the remote original alone, and
		// keep the original path out of the sync set so nothing overwrites it.
		host, _ := os.Hostname()
		seen := map[string]int{}
		for _, c := range conflicts.Conflicts {
			seen[c.Path]++
			newPath, err := keepBoth(ctx, fdst, fsrc, c.Path, host, seen[c.Path])
			if err != nil {
				return out, fmt.Errorf("pair %q: keep-both: %w", p.Name, err)
			}
			fmt.Fprintf(w, "  kept both: %q also uploaded as %q\n", c.Path, newPath)
		}
		ctx, err := excludePaths(ctx, paths)
		if err != nil {
			return out, err
		}
		out.ctx = ctx
		out.abort = true // the conflicts themselves were handled; do not sync them
		return out, nil

	default: // ConflictReport
		ctx, err := excludePaths(ctx, paths)
		if err != nil {
			return out, err
		}
		out.ctx = ctx
		out.abort = true
		fmt.Fprintf(w, "  conflict policy: report - these paths are left untouched\n")
		return out, nil
	}
}

type conflictOutcome struct {
	ctx      context.Context
	reported []Conflict
	// abort stops the run: with report the point is to change nothing at all,
	// and with keep-both the conflicts are already resolved.
	abort bool
}

// runPass performs one sync pass and reports what it did.
func runPass(ctx context.Context, fdst, fsrc fs.Fs, opts passOptions, dryRun, deleteExtras bool) (PassResult, error) {
	ctx, ci := fs.AddConfig(ctx)
	ci.DryRun = dryRun
	ci.Suffix = opts.suffix
	ci.SuffixKeepExtension = opts.suffix != ""
	ci.BackupDir = opts.backupDir

	// rclone's own gate deletes up to the limit before aborting, which is the
	// opposite of what this project needs. Ours runs above, on the dry pass.
	ci.MaxDelete = -1
	// Same reason, but the byte total is only ever measured on the dry pass:
	// rclone stops at the threshold, so leaving this on for the real pass would
	// turn a refusal into a partial deletion.
	ci.MaxDeleteSize = -1
	if opts.maxDeleteSize > 0 {
		ci.MaxDeleteSize = fs.SizeSuffix(opts.maxDeleteSize)
	}

	// Daemon-level excludes, on top of whatever filter the caller already put on
	// the context. AddConfig appends to the existing filter rather than replacing
	// it, which matters: the conflict and trash exclusions are already there, and
	// building a fresh filter would silently drop them.
	if len(opts.exclude) > 0 {
		var fi *filter.Filter
		ctx, fi = filter.AddConfig(ctx)
		for _, pattern := range opts.exclude {
			if err := fi.Add(false, pattern); err != nil {
				return PassResult{}, fmt.Errorf("exclude %q: %w", pattern, err)
			}
		}
	}

	// accounting.Stats honours the group on the context and falls back to the
	// global counters, so the command line keeps its old behaviour while the
	// daemon gets per-job numbers.
	stats := accounting.Stats(ctx)
	stats.ResetCounters()

	var err error
	if deleteExtras {
		// Mirror: make the destination an exact copy.
		err = rsync.Sync(ctx, fdst, fsrc, true)
	} else {
		// Update: add and overwrite only.
		err = rsync.CopyDir(ctx, fdst, fsrc, true)
	}

	out := PassResult{
		Transferred: stats.GetTransfers(),
		Deleted:     stats.GetDeletes(),
		Errors:      stats.GetErrors(),
		Bytes:       stats.GetBytes(),
	}
	if raw, err := stats.RemoteStats(true); err == nil && raw != nil {
		if v, ok := raw["deletedDirs"]; ok {
			if n, ok := v.(int64); ok {
				out.DeletedDirs = n
			}
		}
	}
	return out, err
}

// withTrashExcluded keeps the versioning folder out of the sync set.
//
// Two reasons, both required for correctness:
//   - rclone refuses a --backup-dir that overlaps the destination unless a
//     filter excludes it, because a backup dir inside the destination would
//     otherwise be treated as "extra files" and deleted on the next run.
//   - a mirror run would genuinely delete the trash on the following pass, so
//     the versioning history would only ever survive one cycle.
//
// The rule is appended to a copy of the existing filter. Building a fresh filter
// instead (filter.NewFilter + filter.ReplaceConfig) excludes everything: measured
// against local directories, that form planned zero transfers and zero deletions
// for a pair that must do both.
func withTrashExcluded(ctx context.Context, versioningDir string) (context.Context, error) {
	ctx, fi := filter.AddConfig(ctx)
	glob := "/" + strings.Trim(versioningDir, "/") + "/**"
	if err := fi.Add(false, glob); err != nil {
		return ctx, fmt.Errorf("exclude %q: %w", glob, err)
	}
	return ctx, nil
}

// backupDirFor returns the trash folder path, or "" when deletion is permanent.
//
// The result must live on the same remote as the destination; rclone refuses
// anything else. Because the backend now has a server-side Move, retiring a file
// costs one API call instead of a download plus an upload.
func backupDirFor(dstPath string, spec ResolvedSync) string {
	if spec.Deletion != DeletionVersioning {
		return ""
	}
	base := strings.TrimSuffix(dstPath, "/") + "/" + strings.Trim(spec.VersioningDir, "/")
	if spec.VersioningNaming == NamingTimestampDir {
		base += "/" + time.Now().Format("20060102-150405")
	}
	return base
}

// suffixFor returns the suffix added to retired files, per the naming convention.
func suffixFor(spec ResolvedSync, now time.Time) string {
	if spec.Deletion != DeletionVersioning {
		return ""
	}
	if spec.VersioningNaming == NamingTimestampFile {
		return "." + now.Format("20060102-150405")
	}
	return ""
}
