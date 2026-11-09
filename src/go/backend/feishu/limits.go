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

// SnapshotHas reports whether the sync snapshot already knows this path.
//
// The preview uses it to tell an update from a new file: rclone's dry-run log says
// "copy" for both, and only the snapshot knows whether we uploaded this path
// before. Safe on a nil snapshot (stateless pairs), which answers false.
func (f *Fs) SnapshotHas(rel string) bool {
	if f.st == nil {
		return false
	}
	f.stateMu.Lock()
	defer f.stateMu.Unlock()
	_, ok := f.st.Lookup(rel)
	return ok
}
