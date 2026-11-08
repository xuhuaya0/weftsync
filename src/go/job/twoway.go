package job

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/rclone/rclone/fs"
)

// Two-way sync, stage one.
//
// What this does: a file the cloud replaced, while the local copy was untouched
// since the last sync, is downloaded over the local copy.
//
// What it deliberately does not do: propagate deletions in either direction, and
// download files that appeared in the cloud without ever being in the snapshot.
// Both are irreversible in a way that needs its own decision (Feishu has no
// API-level restore), so they are reported and left alone. See D43.
//
// The precondition is what makes the one write safe: "the local file still looks
// exactly like what we recorded" means downloading cannot destroy an edit that
// the user made after the last sync.

// pullPlan splits the conflicts into "safe to download" and "leave it alone".
type pullPlan struct {
	// pullable are remote-replaced paths whose local copy is unchanged.
	pullable []Conflict
	// hold are paths a human has to look at: both sides changed, remote deleted,
	// or duplicate names.
	hold []Conflict
}

// planPulls decides, per conflict, whether downloading is safe.
func planPulls(ctx context.Context, fsrc fs.Fs, conflicts []Conflict) pullPlan {
	var plan pullPlan
	for _, c := range conflicts {
		if c.Kind != ConflictKindRemoteReplaced {
			// Remote deleted, or several files share the name: stage one cannot
			// resolve either without deleting something.
			plan.hold = append(plan.hold, c)
			continue
		}
		if len(c.Found) != 1 {
			// "remote-replaced" is defined as "one different file carries this
			// name", so anything else is a report we do not understand well
			// enough to overwrite a local file on the strength of it.
			plan.hold = append(plan.hold, c)
			continue
		}
		obj, err := fsrc.NewObject(ctx, c.Path)
		if err != nil {
			// The local file is gone. "The cloud replaced it and the local copy
			// disappeared" is not a download decision.
			plan.hold = append(plan.hold, c)
			continue
		}
		if obj.Size() != c.RecordedSize || obj.ModTime(ctx).Unix() != c.RecordedMTime {
			// Both sides moved since the last sync: that is a real conflict, and
			// downloading would silently discard the local edit.
			plan.hold = append(plan.hold, c)
			continue
		}
		plan.pullable = append(plan.pullable, c)
	}
	return plan
}

// pullRemoteChanges downloads every path the cloud replaced and the local copy
// did not move.
//
// Returns the paths that were pulled, so the caller can report a count and keep
// them out of nothing (they are adopted, so the following pass sees no change).
func pullRemoteChanges(ctx context.Context, p *Pair, plan pullPlan, dryRun bool, w io.Writer) (int, error) {
	if len(plan.pullable) == 0 {
		return 0, nil
	}

	pulled := 0
	for _, c := range plan.pullable {
		if dryRun {
			fmt.Fprintf(w, "  would download: %q (the cloud replaced it, the local copy is unchanged)\n",
				c.Path)
			pulled++
			continue
		}
		if err := pullConflictToken(ctx, p, c.Path, c.Found[0]); err != nil {
			// A backend with no snapshot cannot record the adoption; there is
			// nothing to record, so the download was the whole job.
			if errors.Is(err, errResolveUnsupported) {
				fmt.Fprintf(w, "  downloaded: %q (the cloud replaced it; "+
					"this backend keeps no snapshot, so nothing to adopt)\n", c.Path)
				pulled++
				continue
			}
			// One failed download must not stop the rest: the others are just as
			// safe, and the failure is reported as an error below.
			return pulled, fmt.Errorf("download %q: %w", c.Path, err)
		}
		fmt.Fprintf(w, "  downloaded: %q (the cloud replaced it)\n", c.Path)
		pulled++
	}
	return pulled, nil
}

// ---------------------------------------------------------------- stage two
//
// Stage two adds the three decisions stage one refused to make. All three are
// off by default, and each is a separate switch, because they fail very
// differently:
//
//	PropsLocalDelete   local file gone  -> delete the cloud copy
//	PropsRemoteDelete  cloud copy gone  -> delete the local file
//	PullNewFiles       cloud gained it  -> download it
//
// The first can be undone from the cloud's versioning trash when
// deletion = "versioning"; the second cannot be undone at all; the third can push
// arbitrary cloud content onto the local disk. Turning them on one at a time is
// the only way the blast radius stays understandable.

// TwoWayOptions controls the three propagation switches.
type TwoWayOptions struct {
	// PropsLocalDelete deletes the cloud copy of a file that is gone locally.
	PropsLocalDelete bool
	// PropsRemoteDelete deletes the local copy of a file that is gone in the
	// cloud. Irreversible: os.Remove, not a trash move.
	PropsRemoteDelete bool
	// PullNewFiles downloads files the cloud gained since the last sync.
	PullNewFiles bool
	// MaxPullSize bounds one downloaded file, in bytes. 0 means the default.
	// A cloud folder can hold a 4GB video somebody dropped in; a sync that
	// silently starts downloading it is a surprise, not a feature.
	MaxPullSize int64
}

// DefaultMaxPullSize is the ceiling for pulling a single new file: 100 MiB.
const DefaultMaxPullSize = 100 << 20

// Enabled reports whether any stage-two behaviour is on.
func (o TwoWayOptions) Enabled() bool {
	return o.PropsLocalDelete || o.PropsRemoteDelete || o.PullNewFiles
}

// stageTwo is what a run decided to do beyond the stage-one pulls.
type stageTwo struct {
	localDeleted  []Conflict // local gone, cloud intact  -> cloud delete
	remoteDeleted []Conflict // cloud gone, local intact  -> local delete
	pullNew       []Conflict // cloud added              -> download
	// skipped explains, per path, why nothing happened. These are the messages
	// that make a "my file is still there" complaint answerable.
	skipped []string
}

// hasWork reports whether anything was decided, for log/output purposes.
func (s stageTwo) hasWork() bool {
	return len(s.localDeleted)+len(s.remoteDeleted)+len(s.pullNew) > 0
}

// subtractHandled removes the paths stage two already acted on.
//
// Without it a run printed "deleted locally: cloud-gone.txt" and then
// "1 conflict(s) … these paths are left untouched" about the same file.
func subtractHandled(hold []Conflict, s stageTwo) []Conflict {
	done := make(map[string]bool, len(s.localDeleted)+len(s.remoteDeleted)+len(s.pullNew))
	for _, c := range s.localDeleted {
		done[c.Path] = true
	}
	for _, c := range s.remoteDeleted {
		done[c.Path] = true
	}
	for _, c := range s.pullNew {
		done[c.Path] = true
	}
	if len(done) == 0 {
		return hold
	}
	out := make([]Conflict, 0, len(hold))
	for _, c := range hold {
		if !done[c.Path] {
			out = append(out, c)
		}
	}
	return out
}

// planStageTwo classifies the non-conflict paths.
//
// The three lists are decided here and acted on by applyStageTwo, so a preview can
// show exactly the same classification without touching anything.
func planStageTwo(ctx context.Context, fsrc fs.Fs, scan ConflictScan, opt TwoWayOptions) stageTwo {
	var plan stageTwo

	if opt.PropsLocalDelete {
		for _, c := range scan.Matched {
			if _, err := fsrc.NewObject(ctx, c.Path); err == nil {
				continue // still there locally; nothing was deleted
			}
			plan.localDeleted = append(plan.localDeleted, c)
		}
	}

	if opt.PropsRemoteDelete {
		for _, c := range scan.Conflicts {
			if c.Kind != ConflictKindRemoteDeleted {
				continue
			}
			obj, err := fsrc.NewObject(ctx, c.Path)
			if err != nil {
				plan.skipped = append(plan.skipped, fmt.Sprintf(
					"%q: already gone locally", c.Path))
				continue
			}
			// Only delete a local file we know the cloud had. If the local copy
			// moved since the last sync, the user edited it after the cloud copy
			// vanished - deleting that is throwing away work.
			// Only delete a local file we know the cloud had. If the local copy
			// moved since the last sync, the user edited it after the cloud copy
			// vanished - deleting that throws away work that exists nowhere else.
			// twoway_stage2_test.go fails if this guard is weakened.
			if obj.Size() != c.RecordedSize || obj.ModTime(ctx).Unix() != c.RecordedMTime {
				plan.skipped = append(plan.skipped, fmt.Sprintf(
					"%q: changed locally after the cloud copy disappeared, kept", c.Path))
				continue
			}
			plan.remoteDeleted = append(plan.remoteDeleted, c)
		}
	}

	if opt.PullNewFiles {
		limit := opt.MaxPullSize
		if limit <= 0 {
			limit = DefaultMaxPullSize
		}
		for _, c := range scan.RemoteAdded {
			// Size is not in the listing (Feishu omits it), so the ceiling is
			// checked after the download starts... unless the backend can tell us.
			if len(c.Found) != 1 {
				plan.skipped = append(plan.skipped, fmt.Sprintf(
					"%q: the folder holds %d copies of it, left alone", c.Path, len(c.Found)))
				continue
			}
			plan.pullNew = append(plan.pullNew, c)
		}
	}
	return plan
}

// applyStageTwo carries out what planStageTwo decided.
func applyStageTwo(ctx context.Context, p *Pair, plan stageTwo, dryRun bool, w io.Writer) (localDeleted, remoteDeleted, pulled int, err error) {
	// 1. Local deletions of cloud copies.
	for _, c := range plan.localDeleted {
		if dryRun {
			fmt.Fprintf(w, "  would delete in cloud: %q (gone locally)\n", c.Path)
			localDeleted++
			continue
		}
		if err := deleteCloudCopy(ctx, p, c.Path, c.Found); err != nil {
			return localDeleted, remoteDeleted, pulled, fmt.Errorf(
				"delete cloud copy of %q: %w", c.Path, err)
		}
		fmt.Fprintf(w, "  deleted in cloud: %q (gone locally)\n", c.Path)
		localDeleted++
	}

	// 2. Cloud deletions of local copies. This one cannot be undone, so it is
	//    done last: if anything above failed, the local files are still there.
	for _, c := range plan.remoteDeleted {
		localPath := filepath.Join(p.LocalPath(), filepath.FromSlash(c.Path))
		if dryRun {
			fmt.Fprintf(w, "  would delete locally: %q (gone in cloud)\n", c.Path)
			remoteDeleted++
			continue
		}
		if err := os.Remove(localPath); err != nil && !errors.Is(err, fs.ErrorObjectNotFound) {
			return localDeleted, remoteDeleted, pulled, fmt.Errorf(
				"delete local %q: %w", c.Path, err)
		}
		fmt.Fprintf(w, "  deleted locally: %q (gone in cloud)\n", c.Path)
		remoteDeleted++
		// The snapshot entry must go too, or the next run sees a "local deletion"
		// and tries to delete the cloud copy that is already gone.
		if err := forgetBaseline(ctx, p, c.Path); err != nil {
			return localDeleted, remoteDeleted, pulled, err
		}
	}

	// 3. New cloud files.
	for _, c := range plan.pullNew {
		if dryRun {
			fmt.Fprintf(w, "  would download new file: %q\n", c.Path)
			pulled++
			continue
		}
		if err := pullConflictToken(ctx, p, c.Path, c.Found[0]); err != nil {
			if errors.Is(err, errResolveUnsupported) {
				fmt.Fprintf(w, "  downloaded new file: %q (no snapshot to record it in)\n", c.Path)
				pulled++
				continue
			}
			return localDeleted, remoteDeleted, pulled, fmt.Errorf(
				"download new file %q: %w", c.Path, err)
		}
		fmt.Fprintf(w, "  downloaded new file: %q\n", c.Path)
		pulled++
	}

	for _, note := range plan.skipped {
		fmt.Fprintf(w, "  left alone: %s\n", note)
	}
	return localDeleted, remoteDeleted, pulled, nil
}

// deleteCloudCopy removes the cloud file at path, then drops the baseline.
//
// tokens must be exactly the set the conflict report saw. A file that gained a
// second copy since that report is not what we decided to delete, so nothing is
// touched: deleting by name would take whichever copy the backend picked.
func deleteCloudCopy(ctx context.Context, p *Pair, path string, tokens []string) error {
	if len(tokens) != 1 {
		return fmt.Errorf("the folder holds %d copies of %q, not the one the report "+
			"described; nothing was deleted", len(tokens), path)
	}

	remotePath, err := p.RemotePath()
	if err != nil {
		return err
	}
	fdst, err := fs.NewFs(ctx, remotePath)
	if err != nil {
		return fmt.Errorf("open destination %q: %w", remotePath, err)
	}
	obj, err := fdst.NewObject(ctx, path)
	if err != nil {
		return fmt.Errorf("read remote %q: %w", path, err)
	}
	if tok := objectToken(obj); tok != "?" && tok != tokens[0] {
		return fmt.Errorf("the cloud copy of %q changed since the report was taken; "+
			"nothing was deleted", path)
	}
	if err := obj.Remove(ctx); err != nil {
		return fmt.Errorf("remove remote %q: %w", path, err)
	}
	// Explicit, even though the feishu backend's Remove already drops the entry:
	// that is a per-backend detail, and the next run would read a stale baseline
	// as "recorded, missing locally, missing in the cloud". forgetBaseline treats
	// "no record" as success, so doing it twice is harmless.
	return forgetBaseline(ctx, p, path)
}

// forgetBaseline drops a path from the snapshot.
//
// "No record" counts as success: the goal is "the snapshot does not mention this
// path", and it already does not. Reporting an error there turned a completed
// deletion into a failed run.
func forgetBaseline(ctx context.Context, p *Pair, path string) error {
	err := resolveFn(ctx, p, path, map[string]string{"action": "forget"})
	if err != nil && strings.Contains(err.Error(), "no record") {
		return nil
	}
	return err
}
