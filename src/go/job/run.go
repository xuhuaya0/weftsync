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
	"github.com/rclone/rclone/fs/operations"
	rsync "github.com/rclone/rclone/fs/sync"

	"weftsync/plan"
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
	// KeptBoth marks the policy that handles the conflict instead of reporting
	// it: the copies were kept, so the message must not claim nothing happened.
	KeptBoth bool
}

func (e *ConflictError) Error() string {
	// keep-both DID upload: saying "nothing was uploaded" in the same run that
	// printed "also uploaded as ..." is a contradiction the user has no way to
	// resolve.
	if e.KeptBoth {
		return fmt.Sprintf(
			"pair %q has %d conflict(s) between the folder and the sync state. "+
				"Your version was uploaded under a new name and the cloud version was "+
				"left untouched; nothing was deleted or overwritten. "+
				"This is the conflict = \"keep-both\" behaviour, so the run exits "+
				"non-zero to make the conflicts visible",
			e.Pair, e.Count)
	}
	return fmt.Sprintf(
		"pair %q has %d conflict(s) between the folder and the sync state. "+
			"NOTHING was uploaded, overwritten or deleted. "+
			"Review the report above, then either fix the folder in the destination, "+
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
	// NewPulled counts files downloaded because the cloud gained them.
	NewPulled int `json:"new_pulled"`
	// LocalDeleted counts cloud copies removed because the local file was gone.
	LocalDeleted int `json:"local_deleted"`
	// RemoteDeleted counts local files removed because the cloud copy was gone.
	RemoteDeleted int `json:"remote_deleted"`
	// Held counts conflicts a two-way run left for a human: deletions and files
	// that only exist in the cloud.
	Held int `json:"held"`
	// OverLimit names directories holding more direct children than the backend
	// accepts, so the run was refused before anything was uploaded.
	OverLimit []string `json:"over_limit,omitempty"`

	// Plan is what a preview would do, one row per path, read back out of
	// rclone's own dry-run logging. Only filled when Options.CollectPlan is set.
	//
	// Deliberately not a second comparison implementation: the plan and the run
	// have to agree, and two implementations drift.
	Plan []plan.Row `json:"plan,omitempty"`
	// PlanSummary counts Plan by action, so the page can show the header before
	// rendering thousands of rows.
	PlanSummary plan.Summary `json:"plan_summary"`

	// ContentIdentical lists paths the content comparison proved identical, so the
	// run skipped them (compare = "content"). The page shows them as "same"
	// rather than leaving the user to wonder where they went.
	ContentIdentical []string `json:"content_identical,omitempty"`
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
	// The per-directory child cap is deliberately NOT here. It lives in the job
	// file (sync.max_nodes_per_dir) and otherwise comes from the backend, because
	// a Go-level default was how Feishu's number ended up applied to a local disk.
	// See ResolvedSync.MaxNodesPerDir for the three states.

	// CollectPlan records what the dry pass would do, path by path (see
	// PairResult.Plan). Meaningful only with DryRun, which is the only mode that
	// must not change anything.
	CollectPlan bool

	// OnlyPaths restricts this run to the listed paths, relative to the pair root.
	//
	// This is how "untick a row in the comparison, then synchronise" works: the
	// daemon turns the ticked rows into an include list. Empty means "everything".
	//
	// Worth knowing what it implies: an include list also limits what counts as an
	// extra file, so a path the user unticked cannot be deleted by this run either.
	// That is the behaviour the hand-edited list asks for - unticked means "do not
	// touch" - and it fails safe.
	OnlyPaths []string

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
//
// The results are named so a deferred plan collection can fill res.Plan on every
// return path. There are several of them (guard trips, conflict aborts, backend
// errors) and a plan that is missing exactly when the run went wrong is the least
// useful version of it.
func RunPair(ctx context.Context, cfg *Config, p *Pair, w io.Writer, opt Options) (res PairResult, err error) {
	res = PairResult{Pair: p.Name}

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

	// Pre-flight: refuse a directory holding more direct children than the
	// destination accepts. Those backends answer with an error only on the call
	// that creates or uploads into the folder - by which time part of the run has
	// happened. A local walk costs nothing in comparison, and it lets the run stop
	// with a message naming the directory instead of a bare API error.
	//
	// This runs AFTER the destination is open, because the limit belongs to the
	// backend: Feishu caps a folder at 1500, a local disk does not cap it at all.
	// Running it before (with Feishu's number compiled in) refused a local ->
	// local sync of 1401 files with a message about Feishu.
	fdst, err := fs.NewFs(ctx, remotePath)
	if err != nil {
		return res, fmt.Errorf("pair %q: open destination %q: %w", p.Name, remotePath, err)
	}
	// Flush whatever the backend buffered before returning, whichever way this
	// pair ends. The backend throttles snapshot writes (see feishu.FlushState),
	// so the last few uploads of a run would otherwise never reach the disk and
	// the next run would upload them a second time.
	defer flushStateOf(fdst, w)

	// A pass that has hit a systemic failure must stop, not grind through the
	// rest of the tree. rclone keeps going after individual failures - correct
	// for one bad file, useless when every call is being refused.
	ctx, abortPass := context.WithCancel(ctx)
	defer abortPass()
	installAbortHook(fdst, abortPass)

	refuseAt, backendLimit, backend := nodeLimit(fdst, spec, p)
	if refuseAt > 0 {
		// A preview does not fail on this: its job is to report what a real run
		// would do, and the answer is "be refused". The directories are listed
		// either way so the plan panel and the error agree.
		_, depth := split.LimitOf(fdst)
		findings, err := split.Precheck(p.LocalPath(), split.PrecheckOptions{
			RefuseAt: refuseAt,
			MaxDepth: depth,
		})
		if err != nil {
			return res, fmt.Errorf("pair %q: pre-flight check: %w", p.Name, err)
		}
		if len(findings) > 0 {
			for _, f := range findings {
				res.OverLimit = append(res.OverLimit, f.RelPath)
			}
			limitErr := &split.OverLimitError{
				Root:         p.LocalPath(),
				Limit:        refuseAt,
				Backend:      backend,
				BackendLimit: backendLimit,
				Findings:     findings,
			}
			if opt.DryRun {
				fmt.Fprintf(w, "note: pair %q: %v\n", p.Name, limitErr)
			} else {
				return res, fmt.Errorf("pair %q: %w", p.Name, limitErr)
			}
		}
	}

	fsrc, err := fs.NewFs(ctx, p.LocalPath())
	if err != nil {
		return res, fmt.Errorf("pair %q: open source %q: %w", p.Name, p.LocalPath(), err)
	}

	// The per-path plan comes from rclone's own dry-run logging, so it is collected
	// around the passes rather than computed separately - see package plan for why.
	// The deferred fill runs on every return path, including the guard and conflict
	// aborts: "the run was refused" is exactly when the user wants the list.
	if opt.CollectPlan && opt.DryRun {
		if c, perr := plan.Begin(fdst, fsrc); perr == nil {
			defer func() {
				rows := plan.ApplySnapshot(c.End(), snapshotLookup(fdst))
				// Anything still marked "create" that the destination actually has
				// is an update. The snapshot answers this for a backend that keeps
				// one, but a local -> local pair has no snapshot at all, and without
				// this second question every changed file was labelled "new" while
				// sitting on both sides - visibly wrong, and it also blanked the
				// destination pane for files that were right there.
				rows = markExistingAsUpdates(ctx, fdst, rows)
				res.Plan = rows
				res.PlanSummary = plan.Summarise(rows)
			}()
		} else {
			// A second preview of a different job is running. Say so instead of
			// returning rows that belong to both.
			fmt.Fprintf(w, "note: pair %q: %v\n", p.Name, perr)
		}
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
			fmt.Fprintf(w, "note: pair %q: conflicts cannot be checked%s; "+
				"two-way needs a state_file, so this run can only upload\n",
				p.Name, parenIf(scan.Summary))
		} else {
			plan := planPulls(ctx, fsrc, scan.Conflicts)
			pulled, pullErr := pullRemoteChanges(ctx, p, plan, opt.DryRun, w)
			res.Pulled = pulled
			res.Conflicts = plan.hold
			res.Held = len(plan.hold)
			if pullErr != nil {
				return res, fmt.Errorf("pair %q: %w", p.Name, pullErr)
			}

			// Stage two: the three propagation switches. They come from the job file
			// and nothing else - a second source of truth (a flag, an API field)
			// would let `weftsync run` and the daemon disagree about whether
			// deletions propagate, which is the one thing that must never be
			// ambiguous here.
			twoOpt := TwoWayOptions{
				PropsLocalDelete:  spec.PropagateLocalDelete,
				PropsRemoteDelete: spec.PropagateRemoteDelete,
				PullNewFiles:      spec.PullNewFiles,
				MaxPullSize:       spec.MaxPullSize,
			}
			stage := planStageTwo(ctx, fsrc, scan, twoOpt)
			if stage.hasWork() || len(stage.skipped) > 0 {
				dl, dr, np, applyErr := applyStageTwo(ctx, p, stage, opt.DryRun, w)
				res.LocalDeleted, res.RemoteDeleted, res.NewPulled = dl, dr, np
				res.Pulled += np
				if applyErr != nil {
					return res, fmt.Errorf("pair %q: %w", p.Name, applyErr)
				}
			}

			if len(plan.hold) > 0 {
				// A path stage two just acted on is no longer something a human
				// has to decide about. Reporting it as "left alone" in the same
				// run that deleted or downloaded it was a contradiction
				// (measured: "deleted locally: cloud-gone.txt" immediately above
				// "1 conflict … left alone").
				remaining := subtractHandled(plan.hold, stage)
				if len(remaining) > 0 {
					reportConflicts(w, p.Name, remaining)
				}
				// Held paths must leave the sync set as well, otherwise the upload
				// pass resurrects a file somebody deleted in the cloud - which is
				// the exact opposite of "left alone", and would make the report a
				// lie. Same rule the report policy already applies.
				paths := make([]string, 0, len(remaining))
				for _, c := range remaining {
					paths = append(paths, c.Path)
				}
				if len(paths) > 0 {
					excluded, err := excludePaths(ctx, paths)
					if err != nil {
						return res, fmt.Errorf("pair %q: %w", p.Name, err)
					}
					ctx = excluded
				}
				if len(remaining) > 0 {
					fmt.Fprintf(w, "  two-way: %d path(s) need a decision and were left alone "+
						"(see docs/decisions.md D43/D49 for which switches change this)\n",
						len(remaining))
				}
				res.Held = len(remaining)
			}
			// Paths whose cloud copy was just deleted must not be re-uploaded by
			// the pass below, or the delete is undone immediately.
			var gone []string
			for _, c := range stage.localDeleted {
				gone = append(gone, c.Path)
			}
			if len(gone) > 0 && !opt.DryRun {
				excluded, err := excludePaths(ctx, gone)
				if err != nil {
					return res, fmt.Errorf("pair %q: %w", p.Name, err)
				}
				ctx = excluded
			}
			if pulled > 0 || stage.hasWork() {
				// replaced is tracked separately rather than derived by subtraction:
				// renaming the two counts went negative as soon as stage two pulled
				// more files than stage one did ("-1 replaced, 1 new downloaded").
				fmt.Fprintf(w, "  two-way: %d replaced, %d new downloaded; "+
					"%d cloud and %d local deletion(s)\n",
					pulled, res.NewPulled, res.LocalDeleted, res.RemoteDeleted)
			}
		}
	} else {
		handled, err := handleConflicts(ctx, p, spec, fdst, fsrc, w)
		res.Conflicts = handled.reported
		if err != nil {
			return res, err
		}
		if handled.abort {
			return res, &ConflictError{
				Pair: p.Name, Count: len(handled.reported),
				KeptBoth: spec.Conflict == ConflictKeepBoth,
			}
		}
		ctx = handled.ctx
	}

	// The trash folder name must be identical across both passes, otherwise the
	// dry run would be measuring a different plan from the one executed.
	//
	// The filter layers are combined here rather than inside runPass so the dry
	// pass and the real pass cannot end up with different patterns.
	opts := passOptions{
		backupDir: backupDirFor(remotePath, spec),
		suffix:    suffixFor(spec, time.Now()),
		include:   append(append([]string{}, spec.Include...), onlyInclude(opt.OnlyPaths)...),
		exclude:   append(append([]string{}, spec.Exclude...), opt.Exclude...),
		sizeOnly:  spec.Compare == CompareSize,
	}

	if opts.backupDir != "" {
		var err error
		if ctx, err = withTrashExcluded(ctx, spec.VersioningDir); err != nil {
			return res, fmt.Errorf("pair %q: %w", p.Name, err)
		}
	}

	// compare = "content": read the destination copies back before deciding. Only
	// reachable through an actual comparison, so it runs before the real pass and
	// its findings are honoured by excluding those paths.
	if spec.Compare == CompareContent {
		identical, err := verifyIdenticalPaths(ctx, fdst, fsrc, opts, w)
		if err != nil {
			return res, fmt.Errorf("pair %q: %w", p.Name, err)
		}
		if len(identical) > 0 {
			excluded, err := excludePaths(ctx, identical)
			if err != nil {
				return res, fmt.Errorf("pair %q: %w", p.Name, err)
			}
			ctx = excluded
			res.ContentIdentical = identical
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
		// A systemic stop is not a threshold trip: re-measuring would just fail
		// again, and the caller needs the real reason.
		if IsSystemic(err) {
			return res, err
		}
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

// nodeLimit resolves the per-directory child threshold for this run.
//
// The order is: the job file wins, otherwise the backend declares its own hard
// limit and we refuse split.Headroom below it. A backend that declares nothing
// reports 0, which means no limit at all, and the caller must skip the check.
//
// This is the whole of F12: the limit is a property of the destination, so it is
// asked of the destination. Baking Feishu's number in here is what refused a
// local -> local sync of 1401 files.
func nodeLimit(f fs.Fs, spec ResolvedSync, p *Pair) (refuseAt, backendLimit int, backend string) {
	backendLimit, _ = split.LimitOf(f)
	backend = RemoteKind(p)
	if spec.MaxNodesPerDir != nil {
		return *spec.MaxNodesPerDir, backendLimit, backend
	}
	return split.RefuseAtFor(backendLimit), backendLimit, backend
}

// RemoteKind names the destination backend for messages, without leaking paths or
// options.
//
// fs.ParseRemote resolves "<remote>:<path>" to its backend type, so a pair
// pointing at a remote called "test" is reported as "feishu" - the word that
// explains which limit applies. fs.Find would NOT work here: it only matches a
// backend type name, not a remote path (verified against rclone v1.71.2).
//
// Falling back to the remote name, then the inline scheme, keeps it working when
// the config is not loaded (unit tests with an on-the-fly :local: path).
//
// Exported because the split wizard needs the same word in its response, and two
// implementations would eventually disagree.
func RemoteKind(p *Pair) string {
	if disp := strings.TrimSpace(p.RemoteDisplay()); disp != "" {
		if ri, _, _, _, err := fs.ParseRemote(disp); err == nil && ri != nil && ri.Name != "" {
			return ri.Name
		}
	}
	if b := strings.TrimSpace(p.Remote.Backend); b != "" {
		return b
	}
	root := strings.TrimSpace(p.Remote.Root)
	if strings.HasPrefix(root, ":") {
		if i := strings.Index(root[1:], ":"); i >= 0 {
			return root[1 : 1+i]
		}
	}
	return "local"
}

type passOptions struct {
	backupDir string
	suffix    string
	// exclude holds the daemon-level filter patterns; see Options.Exclude.
	exclude []string
	// include is the pair's whitelist (sync.filter.include). Empty means "all".
	include []string
	// sizeOnly makes rclone compare sizes and ignore modification times
	// (compare = "size-only").
	sizeOnly bool
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
		fmt.Fprintf(w, "note: pair %q: conflicts cannot be checked%s\n",
			p.Name, parenIf(conflicts.Summary))
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

			// Record the cloud copy as the baseline for the original path.
			//
			// Without this the snapshot still holds the token we uploaded two
			// runs ago, so the very next run reports the same conflict and
			// keep-both uploads ANOTHER copy. Measured: two consecutive runs left
			// two extra files, and nothing would stop it until the folder hit
			// Feishu's 1500-node limit.
			//
			// Adopting is deliberate even though the baseline now says "the local
			// file is this cloud token" about a file whose content is somebody
			// else's: keep-both means "both copies are kept, and I do not want to
			// hear about it again". Our version lives at newPath and is in the
			// cloud; the original stays untouched.
			token := ""
			if len(c.Found) > 0 {
				token = c.Found[0]
			}
			if err := adoptCloudCopy(ctx, p, c.Path, token); err != nil {
				// A backend with no resolve command has no snapshot to record a
				// baseline in; the conflict will simply be reported again next
				// run, which is the best that backend can do.
				if !isResolveUnsupported(err) {
					return out, fmt.Errorf("pair %q: keep-both: %w", p.Name, err)
				}
				fmt.Fprintf(w, "  kept both: %q has no recorded baseline because this "+
					"backend keeps no snapshot\n", c.Path)
			}
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

// verifyIdenticalPaths downloads the destination copy of every file this run
// would transfer and reports the ones whose bytes already match.
//
// compare = "content" (R23). It is expensive by nature - the only way to compare
// content on a backend without hashes (Feishu returns hash.None, verified in
// M0/E3) is to read both copies - so it runs over the transfer set only, never
// the whole tree, and the console says what it costs before it starts.
//
// The result is used to exclude those paths from the real pass. Excluding rather
// than merely relabelling is deliberate: a comparison that reports "in sync"
// while the run then re-uploads the file is exactly the drift this project
// refuses to ship.
func verifyIdenticalPaths(ctx context.Context, fdst, fsrc fs.Fs, opts passOptions, w io.Writer) ([]string, error) {
	collector, err := plan.Begin(fdst, fsrc)
	if err != nil {
		// Another preview owns the log hook. Its rows are not ours, so this run
		// cannot verify anything; say so rather than comparing the wrong files.
		return nil, fmt.Errorf("content compare needs the log hook, which another "+
			"preview is using: %w", err)
	}
	dry, passErr := runPass(ctx, fdst, fsrc, opts, true, false)
	rows := collector.End()
	if passErr != nil {
		return nil, passErr
	}

	var total, same int
	var identical []string
	for _, row := range rows {
		if row.Action != plan.Create && row.Action != plan.Update {
			continue
		}
		if !row.HasRight {
			continue // nothing on the destination: nothing to compare against
		}
		dstObj, err := fdst.NewObject(ctx, row.Path)
		if err != nil {
			continue // vanished or unreadable: let the normal pass report it
		}
		srcObj, err := fsrc.NewObject(ctx, row.Path)
		if err != nil {
			continue
		}
		total++
		differ, err := operations.CheckIdenticalDownload(ctx, dstObj, srcObj)
		if err != nil {
			fmt.Fprintf(w, "  content compare: %q could not be read back: %v\n", row.Path, err)
			continue
		}
		if !differ {
			identical = append(identical, row.Path)
			same++
		}
	}
	if total > 0 {
		fmt.Fprintf(w, "  content compare: %d file(s) read back, %d already identical\n", total, same)
	}
	_ = dry
	return identical, nil
}

// markExistingAsUpdates relabels plan rows the destination already holds.
//
// It only asks about rows still marked Create, so the cost is one existence check
// per planned upload - not a walk of either tree. Backends that keep a snapshot
// have already answered, which is why those rows are skipped.
//
// A failure to check is not a failure of the preview: the row keeps its label and
// the run reports whatever is really wrong with it.
func markExistingAsUpdates(ctx context.Context, fdst fs.Fs, rows []plan.Row) []plan.Row {
	for i := range rows {
		if rows[i].Action != plan.Create || rows[i].HasRight {
			continue
		}
		if _, err := fdst.NewObject(ctx, rows[i].Path); err != nil {
			continue
		}
		rows[i].Action = plan.Update
		rows[i].HasRight = true
	}
	return rows
}

// onlyInclude turns a path selection into rclone include patterns.
//
// The paths come from the comparison table, so they are exact files. rclone's
// filter treats a bare glob as "match at any depth", which would be wrong here:
// "data/config.ini" must name that file, not every file with that name. A leading
// "/" anchors the pattern to the root, which is what "this path" means.
//
// A selection with nothing in it returns nil rather than an empty list, because
// "no patterns" and "no paths selected" have opposite meanings downstream.
func onlyInclude(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		p = strings.TrimSpace(strings.TrimPrefix(p, "/"))
		if p == "" {
			continue
		}
		out = append(out, "/"+p)
	}
	return out
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
	// compare = "size-only": ignore the timestamp, treat same-size as unchanged.
	// This is a real change of decision, not just a display option, because it is
	// what the user asked for and rclone supports it natively.
	ci.SizeOnly = opts.sizeOnly

	// Filters, in two layers: the pair's own whitelist and removals (from the job
	// file), then the daemon's global excludes. AddConfig appends to the filter
	// already on the context rather than replacing it, which matters: the conflict
	// and trash exclusions are already there, and building a fresh filter would
	// silently drop them.
	//
	// Include is a whitelist: rclone treats any include pattern as "only these",
	// so adding one can only narrow what syncs. That is why the daemon's global
	// list is exclude-only - a global include would fight every pair's own.
	if len(opts.include) > 0 || len(opts.exclude) > 0 {
		var fi *filter.Filter
		ctx, fi = filter.AddConfig(ctx)
		for _, pattern := range opts.include {
			if err := fi.Add(true, pattern); err != nil {
				return PassResult{}, fmt.Errorf("include %q: %w", pattern, err)
			}
		}
		for _, pattern := range opts.exclude {
			if err := fi.Add(false, pattern); err != nil {
				return PassResult{}, fmt.Errorf("exclude %q: %w", pattern, err)
			}
		}
		// The catch-all that turns an include list into a whitelist.
		//
		// rclone's rule set returns "include" when nothing matches, so a bare
		// include rule re-includes rather than restricts: adding include = ["*.xlsx"]
		// alone still transferred every file (measured). rclone's own CLI appends
		// this same rule after parsing --include (fs/filter/rules.go, the
		// addImplicitExclude branch), which is why --include works there.
		//
		// Appended last on purpose: first match wins, so the user's excludes above
		// can carve holes out of the whitelist before this drops the rest.
		if len(opts.include) > 0 {
			if err := fi.Add(false, "/**"); err != nil {
				return PassResult{}, fmt.Errorf("apply the include whitelist: %w", err)
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

	// A pass that stopped because the backend can no longer make progress has to
	// say so: rclone reports a cancelled transfer as an ordinary per-file failure,
	// so the counts above look like scattered problems rather than one cause.
	if serr := systemicFailureOf(fdst); serr != nil {
		return out, &SystemicError{Transferred: out.Transferred, Errors: out.Errors, Cause: serr}
	}
	return out, err
}

// SystemicError reports that a pass stopped early because the destination backend
// kept refusing calls after every retry.
//
// The distinction matters to the operator: this is not "some files failed", it is
// "give up for now and run again later", and the counts say how far the run got.
type SystemicError struct {
	Transferred int64
	Errors      int64
	Cause       error
}

func (e *SystemicError) Error() string {
	return fmt.Sprintf("stopped after %d transfer(s): the backend could not make progress; "+
		"run again later and it will continue from the saved state (%v)", e.Transferred, e.Cause)
}

func (e *SystemicError) Unwrap() error { return e.Cause }

// IsSystemic reports whether err means the run was stopped by the backend rather
// than by ordinary per-file failures.
func IsSystemic(err error) bool {
	var s *SystemicError
	return errors.As(err, &s)
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

// stateFlusher is implemented by backends that buffer sync records in memory
// instead of writing them on every change.
//
// *feishu.Fs.FlushState is the only implementation today.
type stateFlusher interface {
	FlushState()
}

// snapshotHaser is implemented by backends that keep a sync snapshot and can report
// what it remembers about a path.
type snapshotHaser interface {
	// SnapshotEntry returns the size we uploaded for rel, and whether the snapshot
	// knows the path at all.
	SnapshotEntry(rel string) (size int64, ok bool)
}

// snapshotLookup adapts a backend to the lookup function plan.ApplySnapshot wants.
//
// It answers the two questions the two-pane view asks: whether the destination
// already holds this path (an update rather than a new file) and, if so, how big
// our recorded copy is - the cloud listing carries no size. Returning nil when the
// backend has no snapshot is correct: nothing is known, so every copy is a create
// and the destination cell stays blank instead of showing a made-up number.
func snapshotLookup(f fs.Fs) plan.Lookup {
	if h, ok := f.(snapshotHaser); ok {
		return h.SnapshotEntry
	}
	return nil
}

// flushStateOf writes any buffered sync records to disk.
//
// It reports a type-assertion miss instead of staying silent: if the assertion
// ever stops matching because the backend got wrapped by rclone, the symptom is
// a run that looks fine but silently re-uploads its tail next time, and that is
// far harder to notice than one line here.
func flushStateOf(f fs.Fs, w io.Writer) {
	s, ok := f.(stateFlusher)
	if !ok {
		// Debug, not a note: most backends have no snapshot at all, so this is
		// the normal case for a local disk or union, not something to explain on
		// every run. -vv still surfaces it when the assertion is suspected of
		// drifting.
		fs.Debugf(f, "no sync-state flush (this backend keeps no snapshot)")
		return
	}
	s.FlushState()
}

// abortHooker is implemented by backends that can tell us they are done for now.
type abortHooker interface {
	SetAbortHook(func())
}

// systemicReporter is implemented by backends that can explain a systemic stop.
type systemicReporter interface {
	SystemicFailure() error
}

// installAbortHook lets the backend cancel the pass.
//
// It reports a type-assertion miss instead of staying silent: a backend that
// cannot do this still works, but a systemic failure then costs a full walk of
// the tree instead of stopping within seconds.
func installAbortHook(f fs.Fs, abort func()) {
	if h, ok := f.(abortHooker); ok {
		h.SetAbortHook(abort)
		return
	}
	// Debug, not a note: a backend without the hook works fine, it just stops
	// slower in the rare systemic-failure case. Printing this on every run of
	// every non-Feishu pair would train the user to ignore notes.
	fs.Debugf(f, "no abort hook; a systemic failure will not stop the pass early")
}

// systemicFailureOf returns the backend's latched failure, or nil.
func systemicFailureOf(f fs.Fs) error {
	if r, ok := f.(systemicReporter); ok {
		return r.SystemicFailure()
	}
	return nil
}

// parenIf wraps a non-empty reason in parentheses, so a message does not end in
// an empty "()" when the backend gave no explanation.
func parenIf(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	return " (" + s + ")"
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
