package job

import (
	"context"
	"errors"
	"fmt"
	"io"

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
