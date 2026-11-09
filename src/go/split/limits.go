package split

import (
	"github.com/rclone/rclone/fs"
)

// Per-directory capacity limits.
//
// Feishu caps a folder at 1500 direct children (documented in the create_folder
// Limits section and in error code 1062507) and at 15 levels of depth (1062506).
// A local disk, an SMB share or an S3 bucket has no such cap. That difference
// belongs to the backend, not to the shared sync code.
//
// An earlier version got this wrong: the pre-flight check ran for every pair with
// Feishu's number compiled in, so a local -> local sync of 1401 files was refused
// with a message about a Feishu limit that does not exist on a local disk.
//
// rclone's fs.Features has nowhere to put this. It describes operations (Mkdir,
// Move, Purge) and capability flags (CaseInsensitive, DuplicateFiles,
// CanHaveEmptyDirectories ...), not capacity. So the declaration is a small
// optional interface of our own, asserted exactly like job's stateFlusher and
// abortHooker: a backend that does not implement it simply has no limit.

// LimitReporter is implemented by backends that cap how much one directory holds.
//
// A backend that does not implement it has no limit, and callers must skip the
// pre-flight check entirely rather than substituting a default. Substituting one
// is what produced the bug described above.
type LimitReporter interface {
	// MaxChildrenPerDir is the documented maximum number of direct children
	// (files plus subdirectories) one folder may hold. 0 means no limit.
	MaxChildrenPerDir() int
	// MaxDirDepth is the documented maximum directory depth, counting the pair
	// root as level 1. 0 means no limit.
	MaxDirDepth() int
}

// Headroom is how far below a backend's hard limit we refuse.
//
// A directory sitting exactly on the limit starts failing the moment one file is
// added, and a run that dies halfway is worse than an early refusal that names
// the directory. Be honest about what this buys: nothing, if the directory grows
// by more than Headroom between the check and the sync. The real protection is
// that the check refuses the whole run before anything is uploaded.
const Headroom = 100

// LimitOf asks a backend for its per-directory limits.
//
// A backend without LimitReporter reports (0, 0), meaning no limit: skip the
// check. A nil Fs reports the same, so callers do not need a special case.
func LimitOf(f fs.Fs) (children, depth int) {
	if f == nil {
		return 0, 0
	}
	if r, ok := f.(LimitReporter); ok {
		return r.MaxChildrenPerDir(), r.MaxDirDepth()
	}
	return 0, 0
}

// RefuseAtFor turns a backend's hard limit into the threshold we refuse at.
//
// A backend limit of 0 or less means "no limit", so the result is 0 and the
// caller must not run the check at all.
func RefuseAtFor(backendLimit int) int {
	if backendLimit <= 0 {
		return 0
	}
	if backendLimit <= Headroom {
		return backendLimit
	}
	return backendLimit - Headroom
}

// MaxPerBucketFor is the largest bucket a split may create for this backend.
//
// It equals the refuse threshold, and for the same reason: a bucket that lands
// exactly on the hard limit fails again as soon as one more file arrives, which
// would make the split pointless.
func MaxPerBucketFor(backendLimit int) int {
	return RefuseAtFor(backendLimit)
}
