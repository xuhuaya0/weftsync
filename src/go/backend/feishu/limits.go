package feishu

// Feishu's per-directory capacity limits, declared here because they are a
// property of this backend and of nothing else.
//
// The shared sync code asks a backend for these through split.LimitReporter and
// skips its pre-flight check when the answer is "no limit". Putting the number in
// that shared code instead is what made a local -> local sync of 1401 files fail
// with a message about Feishu.

const (
	// FeishuMaxChildrenPerDir is the documented maximum number of direct
	// children (files plus subdirectories) in one folder.
	//
	// Sources, both checked 2026-11-09:
	//   - create_folder, "Limits": "The maximum number of single-layer nodes in
	//     the root directory or folders in the cloud space is 1,500"
	//   - upload_all, error 1062507: "the limit of 1,500 nodes per level"
	//
	// Not measured: no run has reached it. The largest directory seen on the real
	// data is 6,399 files, refused locally before any upload was attempted.
	FeishuMaxChildrenPerDir = 1500

	// FeishuMaxDirDepth is the documented maximum directory depth, counting the
	// root as level 1. Source: upload_all, error 1062506: "My Space supports up to
	// 15 levels of directories".
	FeishuMaxDirDepth = 15
)

// MaxChildrenPerDir implements split.LimitReporter.
func (f *Fs) MaxChildrenPerDir() int { return FeishuMaxChildrenPerDir }

// MaxDirDepth implements split.LimitReporter.
func (f *Fs) MaxDirDepth() int { return FeishuMaxDirDepth }

// SnapshotEntry reports what the sync snapshot remembers about a path: the size we
// uploaded, and whether it is known at all.
//
// The preview uses both. The "known" flag separates an update from a new file
// (rclone's dry-run log says "copy" for either), and the size fills the
// destination pane, which the cloud listing cannot: Feishu reports no size for a
// folder listing. Safe on a nil snapshot (stateless pairs), which answers false.
func (f *Fs) SnapshotEntry(rel string) (int64, bool) {
	if f.st == nil {
		return 0, false
	}
	f.stateMu.Lock()
	defer f.stateMu.Unlock()
	e, ok := f.st.Lookup(rel)
	if !ok {
		return 0, false
	}
	return e.LocalSize, true
}
