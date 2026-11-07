package split

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Executing a split.
//
// This moves the user's files on both sides, so the order and the bookkeeping are
// chosen for recoverability rather than speed:
//
//  1. write the plan to a snapshot file (every move, with size and mtime)
//  2. move locally      - os.Rename, same disk, atomic, seconds
//  3. create the remote directories
//  4. move remotely     - the remote is rate limited, so this is the slow part
//  5. verify both sides against the plan
//  6. rewrite the sync state so the next run does not re-upload everything
//
// Local first is deliberate. A local rename cannot half-succeed in a way that
// costs data, and a failure there can be rolled back before any remote call is
// made. Remote first would mean the cloud is reorganised while the local tree is
// not, which is the harder state to recover from.
//
// Every step records its progress in the snapshot. A split over a slow link can
// take minutes, and "it failed" with no record of how far it got is not something
// a user can act on. Resume picks up where it stopped.

// Remote is the part of a backend a split needs.
//
// An interface rather than fs.Fs so the whole execution path can be tested against
// a fake, including the failure cases, without a network or a rate limiter.
type Remote interface {
	// Mkdir creates one directory, relative to the pair's remote root.
	Mkdir(ctx context.Context, rel string) error
	// Move moves one file or directory.
	Move(ctx context.Context, fromRel, toRel string) error
	// List returns the direct children's names.
	List(ctx context.Context, rel string) ([]string, error)
	// Exists reports whether a path is present. Used only to tell "already moved"
	// from "never uploaded" when a source has disappeared, so it is not called on
	// the normal path.
	Exists(ctx context.Context, rel string) (bool, error)
}

// PathMove is one state entry moving.
type PathMove struct {
	From string
	To   string
}

// StateMover rewrites the sync state after paths change.
//
// It is an interface, and the implementation goes through the backend, because the
// state file is not the split's to edit. The backend keeps that snapshot in memory
// for the lifetime of its cached Fs, and rclone's Fs cache has no per-entry eviction
// (ClearMappingsPrefix only drops the name mapping). Writing the file directly would
// therefore be overwritten by the next flush from the stale in-memory copy - and
// that flush would also re-introduce the very old paths this rewrite exists to fix.
//
// A nil StateMover means "nothing to rewrite", which is the stateless pair.
type StateMover interface {
	// Repath rewrites each From to its To and reports how many entries changed.
	Repath(ctx context.Context, moves []PathMove) (int, error)
}

// ErrRemoteSourceMissing means the file has never been uploaded, so there is
// nothing to move on the remote side.
//
// This is a normal state, not a failure: the directory that needs splitting is
// often exactly the one whose first upload just failed on the entry limit, so most
// of its files are local only. Treating it as an error would leave the pair half
// moved - local reorganised, remote untouched - for no reason, because the next sync
// will upload the file straight into its new bucket.
var ErrRemoteSourceMissing = errors.New("the remote has no copy of this file yet")

// SnapshotMove is one planned move plus how far it got.
type SnapshotMove struct {
	From string `json:"from"`
	To   string `json:"to"`
	// IsDir means the entry is a directory and moves whole.
	IsDir     bool  `json:"is_dir,omitempty"`
	Size      int64 `json:"size,omitempty"`
	MTimeUnix int64 `json:"mtime,omitempty"`
	// LocalDone and RemoteDone are written as the work proceeds, so an interrupted
	// split can be resumed instead of restarted.
	LocalDone  bool `json:"local_done"`
	RemoteDone bool `json:"remote_done"`
	// RemoteAbsent records that the remote had no copy, so RemoteDone means "settled"
	// rather than "moved". Verification and undo both need to know the difference:
	// the file exists locally and must be checked there, but not on the remote.
	RemoteAbsent bool `json:"remote_absent,omitempty"`
}

// Snapshot is the on-disk record of a split. It is the undo ledger: without it
// there is no way to put the files back.
type Snapshot struct {
	Version   int       `json:"version"`
	Dir       string    `json:"dir"`
	Scheme    Scheme    `json:"scheme"`
	CreatedAt time.Time `json:"created_at"`
	LocalRoot string    `json:"local_root"`
	// Remote is the rclone path of the pair's destination, recorded for the
	// operator's benefit; the undo uses the live remote, not this.
	Remote string         `json:"remote"`
	Moves  []SnapshotMove `json:"moves"`
	// Buckets is the directories the split creates.
	Buckets []string `json:"buckets"`
	// Completed is set only after verification passed.
	Completed   bool      `json:"completed"`
	CompletedAt time.Time `json:"completed_at,omitempty"`
	// Undone marks a split that was rolled back on request. Without it a fully
	// undone split is indistinguishable from one that never finished: both have
	// every move flag false and Completed unset, so the console would tell the
	// operator to "resume" a split they deliberately cancelled.
	Undone   bool      `json:"undone"`
	UndoneAt time.Time `json:"undone_at,omitempty"`
}

// Pending counts moves that still need work on either side. An undone split has
// no pending work left by definition, so it reports zero rather than "every move
// is still to do".
func (s *Snapshot) Pending() int {
	if s.Undone {
		return 0
	}
	n := 0
	for _, m := range s.Moves {
		if !m.LocalDone || !m.RemoteDone {
			n++
		}
	}
	return n
}

// Executor runs a plan.
type Executor struct {
	// LocalRoot is the pair's local root, absolute.
	LocalRoot string
	// Remote is the destination side.
	Remote Remote
	// State rewrites the sync state after the moves. Nil when the pair has none.
	State StateMover
	// SnapshotPath is where the undo ledger is written.
	SnapshotPath string
	// RemoteLabel is put in the snapshot for the operator.
	RemoteLabel string
	// Now is injectable for tests.
	Now func() time.Time
	// OnStep is called after every completed move. It must not block for long:
	// the remote is rate limited and this runs between calls.
	OnStep func(done, total int, what string)

	snap *Snapshot
}

func (e *Executor) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// Result summarises a completed or resumed split.
type Result struct {
	SnapshotPath   string   `json:"snapshot_path"`
	Moved          int      `json:"moved"`
	Buckets        []string `json:"buckets"`
	Verified       bool     `json:"verified"`
	StateRewritten int      `json:"state_rewritten"`
	Notes          []string `json:"notes,omitempty"`
}

// Execute runs a plan from the beginning.
func (e *Executor) Execute(ctx context.Context, plan *Plan) (*Result, error) {
	if plan == nil || len(plan.Moves) == 0 {
		return nil, fmt.Errorf("the plan moves nothing")
	}
	if e.Remote == nil {
		return nil, fmt.Errorf("no remote configured")
	}

	snap := &Snapshot{
		Version:   1,
		Dir:       plan.Dir,
		Scheme:    plan.Scheme,
		CreatedAt: e.now(),
		LocalRoot: e.LocalRoot,
		Remote:    e.RemoteLabel,
		Buckets:   bucketNames(plan),
	}
	for _, m := range plan.Moves {
		sm := SnapshotMove{
			From: m.From, To: m.To, IsDir: m.IsDir, Size: m.Size,
		}
		if !m.ModTime.IsZero() {
			sm.MTimeUnix = m.ModTime.Unix()
		}
		snap.Moves = append(snap.Moves, sm)
	}
	e.snap = snap

	// Step 1: the ledger goes to disk before anything moves. If the process dies
	// between here and the first rename, the worst case is an empty ledger.
	if err := e.saveSnapshot(); err != nil {
		return nil, fmt.Errorf("write the split snapshot: %w", err)
	}

	res, err := e.run(ctx)
	if err != nil {
		return res, err
	}
	res.Notes = append(res.Notes, plan.Notes...)
	return res, nil
}

// Resume continues an interrupted split from its snapshot.
func (e *Executor) Resume(ctx context.Context, snapshotPath string) (*Result, error) {
	snap, err := LoadSnapshot(snapshotPath)
	if err != nil {
		return nil, err
	}
	if snap.Completed {
		return nil, fmt.Errorf("this split already completed on %s; nothing to resume",
			snap.CompletedAt.Format(time.RFC3339))
	}
	if snap.Version != 1 {
		return nil, fmt.Errorf("unsupported snapshot version %d", snap.Version)
	}
	// The snapshot names its own local root; a resume against a different root
	// would move files in the wrong tree.
	if e.LocalRoot != "" && snap.LocalRoot != "" &&
		absClean(e.LocalRoot) != absClean(snap.LocalRoot) {
		return nil, fmt.Errorf(
			"this snapshot belongs to %q, not %q", snap.LocalRoot, e.LocalRoot)
	}
	e.snap = snap
	e.SnapshotPath = snapshotPath
	return e.run(ctx)
}

// run performs the phases in order.
func (e *Executor) run(ctx context.Context) (*Result, error) {
	res := &Result{SnapshotPath: e.SnapshotPath, Buckets: e.snap.Buckets}

	// Step 2: local moves. Rolled back on failure, because at this point the cloud
	// has not been touched at all and returning to the original tree is exact.
	if err := e.moveLocalPhase(ctx); err != nil {
		rollbackErr := e.rollbackLocal()
		if rollbackErr != nil {
			return res, fmt.Errorf(
				"%w\n\n已尝试回滚本地移动但未完全成功：%v\n"+
					"快照保留在 %s，可用「撤销拆分」再试一次",
				err, rollbackErr, e.SnapshotPath)
		}
		return res, fmt.Errorf("%w\n\n本地已回滚到原状，云端未做任何改动", err)
	}

	// Step 3: creating directories. A failure here still leaves the cloud
	// untouched apart from possibly empty directories, so the local tree is
	// restored and the empty directories are reported.
	if err := e.mkdirPhase(ctx); err != nil {
		if rbErr := e.rollbackLocal(); rbErr != nil {
			return res, fmt.Errorf("%w\n\n本地回滚未完全成功：%v\n快照：%s",
				err, rbErr, e.SnapshotPath)
		}
		return res, fmt.Errorf(
			"%w\n\n本地已回滚。云端可能留下了空目录（不会丢数据），可重新拆分或手动删除", err)
	}

	// Step 4: remote moves. From here a failure must NOT roll back: the local tree
	// is already correct, and undoing it would double the work for nothing. The
	// snapshot records where it stopped so a resume continues.
	if err := e.moveRemotePhase(ctx); err != nil {
		return res, fmt.Errorf(
			"%w\n\n本地已移动完成，云端移动到此处中断。快照记录了进度：%s\n"+
				"重新执行同一拆分（或「继续」）会接着做，不会重复已完成的移动",
			err, e.SnapshotPath)
	}

	// Step 5: verify.
	verified, notes, err := e.verify(ctx)
	res.Verified = verified
	res.Notes = append(res.Notes, notes...)
	if err != nil {
		return res, fmt.Errorf("校验失败：%w\n快照：%s", err, e.SnapshotPath)
	}
	if !verified {
		return res, fmt.Errorf(
			"两侧内容不一致，未标记为完成。差异见上。快照保留：%s", e.SnapshotPath)
	}

	// Step 6: rewrite the sync state, so the next run does not treat every moved
	// file as new and upload it again.
	rewritten, err := e.migrateState(ctx, false)
	res.StateRewritten = rewritten
	if err != nil {
		return res, fmt.Errorf(
			"文件已移动并校验通过，但同步状态无法更新：%w\n"+
				"下一轮同步会把文件重新上传一遍（云端会出现同名副本）。"+
				"可删除该 pair 的状态文件让下一轮重建", err)
	}

	res.Moved = len(e.snap.Moves)
	e.snap.Completed = true
	e.snap.CompletedAt = e.now()
	if err := e.saveSnapshot(); err != nil {
		res.Notes = append(res.Notes, "完成标记写入快照失败："+err.Error())
	}
	return res, nil
}

// moveLocalPhase renames local entries, creating the bucket directories.
//
// Idempotent on purpose. Both "already moved" and "needs moving" look the same on a
// second run after an interruption, and re-deriving that from the filesystem is far
// more reliable than trusting a progress record that may have missed the last few
// writes.
func (e *Executor) moveLocalPhase(ctx context.Context) error {
	total := len(e.snap.Moves)
	for i := range e.snap.Moves {
		m := &e.snap.Moves[i]
		if m.LocalDone {
			continue
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("已取消：%w", err)
		}

		src := filepath.Join(e.LocalRoot, filepath.FromSlash(m.From))
		dst := filepath.Join(e.LocalRoot, filepath.FromSlash(m.To))

		srcExists := pathExists(src)
		dstExists := pathExists(dst)
		switch {
		case !srcExists && dstExists:
			// Already moved by an earlier attempt whose progress record was lost.
			m.LocalDone = true
			continue
		case !srcExists && !dstExists:
			return fmt.Errorf("本地文件 %q 不存在（既不在原位置也不在新位置）", m.From)
		case srcExists && dstExists:
			// Both present: overwriting is the one thing this must never do.
			return fmt.Errorf("本地目标 %q 已存在，拒绝覆盖", dst)
		}

		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("创建本地目录 %q: %w", filepath.Dir(dst), err)
		}
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("移动本地文件 %q -> %q: %w", m.From, m.To, err)
		}
		m.LocalDone = true
		if e.OnStep != nil {
			e.OnStep(i+1, total, "本地 "+m.From)
		}
	}
	// One durable write for the whole phase. Writing the full snapshot after every
	// move meant 200KB times the entry count - hundreds of megabytes for a real
	// directory, which on a synced folder fails outright (measured: "Access is
	// denied" from OneDrive on the second write).
	return e.saveSnapshot()
}

// mkdirPhase creates the remote bucket directories.
func (e *Executor) mkdirPhase(ctx context.Context) error {
	for _, b := range e.snap.Buckets {
		rel := relJoin(e.snap.Dir, b)
		if err := e.Remote.Mkdir(ctx, rel); err != nil {
			return fmt.Errorf("创建云端目录 %q: %w", rel, err)
		}
	}
	return nil
}

// moveRemotePhase moves the files on the remote side.
func (e *Executor) moveRemotePhase(ctx context.Context) error {
	total := len(e.snap.Moves)
	done := 0
	for i := range e.snap.Moves {
		m := &e.snap.Moves[i]
		if m.RemoteDone {
			done++
			continue
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("已取消：%w", err)
		}
		if err := e.Remote.Move(ctx, m.From, m.To); err != nil {
			if errors.Is(err, ErrRemoteSourceMissing) {
				// Either it was never uploaded, or an earlier attempt moved it and the
				// progress record did not survive. One extra call tells them apart, and
				// only on this rare path.
				atTarget, existsErr := e.Remote.Exists(ctx, m.To)
				if existsErr != nil {
					return fmt.Errorf("检查云端 %q: %w", m.To, existsErr)
				}
				m.RemoteDone = true
				m.RemoteAbsent = !atTarget
				done++
				continue
			}
			return fmt.Errorf("移动云端文件 %q -> %q: %w", m.From, m.To, err)
		}
		m.RemoteDone = true
		done++
		if e.OnStep != nil {
			e.OnStep(done, total, "云端 "+m.From)
		}
		// Persist occasionally rather than per move: see moveLocalPhase. A lost tail
		// costs one Exists call on resume, which is why the phase is idempotent.
		if done%progressEveryMoves == 0 {
			if err := e.saveSnapshot(); err != nil {
				return fmt.Errorf("记录进度到快照: %w", err)
			}
		}
	}
	return e.saveSnapshot()
}

// progressEveryMoves is how often the ledger is persisted during the remote phase.
//
// The remote is rate limited to a handful of calls per second, so this is at most a
// write every several seconds - cheap - while bounding what an interruption can lose
// to a handful of entries.
const progressEveryMoves = 25

// rollbackLocal moves back everything this execution moved locally.
func (e *Executor) rollbackLocal() error {
	var firstErr error
	for i := len(e.snap.Moves) - 1; i >= 0; i-- {
		m := &e.snap.Moves[i]
		if !m.LocalDone {
			continue
		}
		src := filepath.Join(e.LocalRoot, filepath.FromSlash(m.To))
		dst := filepath.Join(e.LocalRoot, filepath.FromSlash(m.From))
		if err := os.Rename(src, dst); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%q -> %q: %w", m.To, m.From, err)
			}
			continue
		}
		m.LocalDone = false
		_ = e.saveSnapshot()
		// Remove the bucket directory if it is now empty; an empty leftover would
		// show up as a change in the next sync.
		if dir := filepath.Dir(src); dir != e.LocalRoot {
			_ = os.Remove(dir)
		}
	}
	return firstErr
}

// verify compares both sides against the plan.
//
// It checks names rather than sizes: a name list is cheap to fetch and catches the
// failures this operation can actually produce (a move that silently did nothing,
// or one that landed in the wrong bucket).
func (e *Executor) verify(ctx context.Context) (bool, []string, error) {
	var notes []string
	ok := true

	for _, b := range e.snap.Buckets {
		rel := relJoin(e.snap.Dir, b)

		// Two sets, not one. The local side must hold every moved entry; the remote
		// side must hold only the ones it had before the split. Sharing one map here
		// meant excluding a remote-absent file also removed it from the local
		// expectation, which reported a correct local tree as wrong.
		wantLocal := map[string]bool{}
		wantRemote := map[string]bool{}
		for _, m := range e.snap.Moves {
			if parentOf(m.To) != rel {
				continue
			}
			wantLocal[baseOf(m.To)] = true
			if !m.RemoteAbsent {
				wantRemote[baseOf(m.To)] = true
			}
		}

		gotLocal, err := listLocal(filepath.Join(e.LocalRoot, filepath.FromSlash(rel)))
		if err != nil {
			return false, notes, fmt.Errorf("列出本地 %q: %w", rel, err)
		}
		gotRemote, err := e.Remote.List(ctx, rel)
		if err != nil {
			return false, notes, fmt.Errorf("列出云端 %q: %w", rel, err)
		}

		if d := diffNames(wantLocal, gotLocal); len(d) > 0 {
			ok = false
			notes = append(notes, fmt.Sprintf("本地 %q 与计划不符：%s", rel, strings.Join(d, "; ")))
		}
		if d := diffNames(wantRemote, gotRemote); len(d) > 0 {
			ok = false
			notes = append(notes, fmt.Sprintf("云端 %q 与计划不符：%s", rel, strings.Join(d, "; ")))
		}
	}
	return ok, notes, nil
}

// diffNames reports the differences between two name sets.
func diffNames(want map[string]bool, got []string) []string {
	have := map[string]bool{}
	for _, n := range got {
		have[n] = true
	}
	var out []string
	var missing, extra []string
	for n := range want {
		if !have[n] {
			missing = append(missing, n)
		}
	}
	for n := range have {
		if !want[n] {
			extra = append(extra, n)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		out = append(out, "缺少 "+strings.Join(limit(missing, 5), ", "))
	}
	if len(extra) > 0 {
		out = append(out, "多出 "+strings.Join(limit(extra, 5), ", "))
	}
	return out
}

func limit(names []string, n int) []string {
	if len(names) <= n {
		return names
	}
	return append(names[:n:n], fmt.Sprintf("…共 %d 项", len(names)))
}

// listLocal reads one local directory's entry names.
func listLocal(abs string) ([]string, error) {
	items, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Name())
	}
	return out, nil
}

// migrateState asks the backend to move the state entries to their new paths.
//
// Without this the next run finds no record for any moved file, treats each as new
// and uploads it again - which on Feishu creates a duplicate rather than
// overwriting, so the "split" would leave two copies of everything.
func (e *Executor) migrateState(ctx context.Context, back bool) (int, error) {
	if e.State == nil {
		return 0, nil
	}
	moves := make([]PathMove, 0, len(e.snap.Moves))
	for _, m := range e.snap.Moves {
		if back {
			moves = append(moves, PathMove{From: m.To, To: m.From})
		} else {
			moves = append(moves, PathMove{From: m.From, To: m.To})
		}
	}
	return e.State.Repath(ctx, moves)
}

// Undo moves everything back, using a snapshot.
//
// The remote side is restored first, because it is the slow part and the most
// likely to fail; if it fails, the local tree is still the split one and matches
// the cloud, so nothing is inconsistent. The snapshot keeps its per-move flags, so
// re-running undo continues rather than restarting.
func (e *Executor) Undo(ctx context.Context, snapshotPath string) (*Result, error) {
	snap, err := LoadSnapshot(snapshotPath)
	if err != nil {
		return nil, err
	}
	if e.Remote == nil {
		return nil, fmt.Errorf("no remote configured")
	}
	e.snap = snap
	e.SnapshotPath = snapshotPath
	if e.LocalRoot == "" {
		e.LocalRoot = snap.LocalRoot
	}

	res := &Result{SnapshotPath: snapshotPath}

	// Remote back first.
	for i := len(snap.Moves) - 1; i >= 0; i-- {
		m := &snap.Moves[i]
		if !m.RemoteDone || m.RemoteAbsent {
			continue
		}
		if err := ctx.Err(); err != nil {
			return res, fmt.Errorf("已取消：%w", err)
		}
		if err := e.Remote.Move(ctx, m.To, m.From); err != nil {
			return res, fmt.Errorf(
				"移回云端文件 %q -> %q 失败：%w\n进度已记录，可再次执行撤销继续", m.To, m.From, err)
		}
		m.RemoteDone = false
		if err := e.saveSnapshot(); err != nil {
			return res, err
		}
	}

	// Then local.
	for i := len(snap.Moves) - 1; i >= 0; i-- {
		m := &snap.Moves[i]
		if !m.LocalDone {
			continue
		}
		src := filepath.Join(e.LocalRoot, filepath.FromSlash(m.To))
		dst := filepath.Join(e.LocalRoot, filepath.FromSlash(m.From))
		if _, err := os.Lstat(dst); err == nil {
			return res, fmt.Errorf("本地 %q 已存在，拒绝覆盖", dst)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return res, err
		}
		if err := os.Rename(src, dst); err != nil {
			return res, fmt.Errorf("移回本地文件 %q -> %q 失败：%w\n可再次执行撤销继续",
				m.To, m.From, err)
		}
		m.LocalDone = false
		if err := e.saveSnapshot(); err != nil {
			return res, err
		}
		if dir := filepath.Dir(src); dir != e.LocalRoot {
			_ = os.Remove(dir) // only removes it when empty
		}
	}

	// Remove the now-empty remote bucket directories, so the pair looks exactly as
	// it did before the split. Empty leftovers would be harmless but confusing.
	for _, b := range snap.Buckets {
		rel := relJoin(snap.Dir, b)
		if names, err := e.Remote.List(ctx, rel); err == nil && len(names) == 0 {
			if remover, ok := e.Remote.(interface {
				Remove(ctx context.Context, rel string) error
			}); ok {
				_ = remover.Remove(ctx, rel)
			}
		}
	}

	// Put the state entries back where they were.
	n, err := e.migrateState(ctx, true)
	if err != nil {
		return res, fmt.Errorf("文件已移回，但同步状态无法恢复：%w", err)
	}
	res.StateRewritten = n
	res.Moved = len(snap.Moves)
	snap.Completed = false
	snap.CompletedAt = time.Time{}
	snap.Undone = true
	snap.UndoneAt = e.now()
	if err := e.saveSnapshot(); err != nil {
		res.Notes = append(res.Notes, "快照更新失败："+err.Error())
	}
	return res, nil
}

// rewriteStateBack is gone: both directions now go through migrateState.

// pathExists reports whether anything is at abs.
func pathExists(abs string) bool {
	_, err := os.Lstat(abs)
	return err == nil
}

// saveSnapshot writes the ledger atomically, so a crash mid-write cannot leave a
// half-parsed file that would block a resume.
//
// The rename is retried: the ledger often lives next to the synced files, and a
// folder synced by OneDrive or a virus scanner can hold the target open for a
// moment. Measured: the second write in such a folder failed with "Access is
// denied" until the write was made both rarer and retried.
func (e *Executor) saveSnapshot() error {
	raw, err := json.MarshalIndent(e.snap, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(e.SnapshotPath), 0o755); err != nil {
		return err
	}
	tmp := e.SnapshotPath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}

	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if lastErr = os.Rename(tmp, e.SnapshotPath); lastErr == nil {
			return nil
		}
		time.Sleep(time.Duration(20*(attempt+1)) * time.Millisecond)
	}
	_ = os.Remove(tmp)
	return lastErr
}

// LoadSnapshot reads a split ledger.
func LoadSnapshot(path string) (*Snapshot, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", path, err)
	}
	var s Snapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("parse %q: %w", path, err)
	}
	if len(s.Moves) == 0 {
		return nil, fmt.Errorf("%q contains no moves", path)
	}
	return &s, nil
}

// SnapshotName builds the ledger's file name for a start time.
func SnapshotName(dir string, t time.Time) string {
	label := "root"
	if dir != "" {
		label = strings.ReplaceAll(dir, "/", "-")
	}
	return fmt.Sprintf("split-%s-%s.json", label, t.Format("20060102-150405"))
}

// bucketNames extracts the bucket directory names from a plan.
func bucketNames(p *Plan) []string {
	out := make([]string, 0, len(p.Buckets))
	for _, b := range p.Buckets {
		out = append(out, b.Name)
	}
	return out
}

func parentOf(rel string) string {
	i := strings.LastIndex(rel, "/")
	if i < 0 {
		return ""
	}
	return rel[:i]
}

func baseOf(rel string) string {
	i := strings.LastIndex(rel, "/")
	if i < 0 {
		return rel
	}
	return rel[i+1:]
}

// absClean normalises a path for comparison.
func absClean(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	return filepath.Clean(abs)
}
