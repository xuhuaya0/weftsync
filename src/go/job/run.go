package job

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/accounting"
	"github.com/rclone/rclone/fs/filter"
	rsync "github.com/rclone/rclone/fs/sync"
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

// PassResult is what one sync pass did (or, for a dry run, would have done).
type PassResult struct {
	Transferred int64
	Deleted     int64
	DeletedDirs int64
	Errors      int64
}

// Destructive reports the total number of destructive operations in the pass.
func (r PassResult) Destructive() int64 { return r.Deleted + r.DeletedDirs }

// PairResult is the outcome for one pair.
type PairResult struct {
	Pair     string
	Variant  string
	Local    string
	Remote   string
	DryRun   PassResult // what the gate measured
	Actual   PassResult
	GuardHit bool
}

// Options tunes a run.
type Options struct {
	// DryRun measures and reports without changing anything. In this mode the
	// guard still runs, so a plan can show that a real run would be blocked.
	DryRun bool
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
	res.Remote = remotePath

	fdst, err := fs.NewFs(ctx, remotePath)
	if err != nil {
		return res, fmt.Errorf("pair %q: open destination %q: %w", p.Name, remotePath, err)
	}
	fsrc, err := fs.NewFs(ctx, p.LocalPath())
	if err != nil {
		return res, fmt.Errorf("pair %q: open source %q: %w", p.Name, p.LocalPath(), err)
	}

	// The trash folder name must be identical across both passes, otherwise the
	// dry run would be measuring a different plan from the one executed.
	opts := passOptions{
		backupDir: backupDirFor(remotePath, spec),
		suffix:    suffixFor(spec, time.Now()),
	}

	if opts.backupDir != "" {
		var err error
		if ctx, err = withTrashExcluded(ctx, spec.VersioningDir); err != nil {
			return res, fmt.Errorf("pair %q: %w", p.Name, err)
		}
	}

	if variant == VariantUpdate {
		// update never deletes, so there is nothing to guard against.
		actual, err := runPass(ctx, fdst, fsrc, opts, opt.DryRun, false)
		res.Actual = actual
		if opt.DryRun {
			res.DryRun = actual
		}
		return res, err
	}

	// Mirror: measure first, decide, then act.
	dry, err := runPass(ctx, fdst, fsrc, opts, true, true)
	res.DryRun = dry
	if err != nil {
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

type passOptions struct {
	backupDir string
	suffix    string
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

	stats := accounting.GlobalStats()
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
