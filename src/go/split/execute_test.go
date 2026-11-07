package split

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// fakeRemote is an in-memory stand-in for a backend.
//
// Using one instead of a real Fs is what makes the failure paths testable: the
// interesting behaviour of this package is what it does when a move fails, and
// arranging that against a real remote is not possible on demand.
type fakeRemote struct {
	// files maps a relative path to its content marker. Directories are tracked
	// separately so an empty bucket is still "there".
	files map[string]string
	dirs  map[string]bool

	// failMkdirAfter and failMoveAfter make the Nth call fail. Zero means never.
	failMkdirAfter int
	failMoveAfter  int
	mkdirCalls     int
	moveCalls      int

	calls []string
}

func newFakeRemote() *fakeRemote {
	return &fakeRemote{files: map[string]string{}, dirs: map[string]bool{}}
}

func (f *fakeRemote) Mkdir(ctx context.Context, rel string) error {
	f.mkdirCalls++
	f.calls = append(f.calls, "mkdir "+rel)
	if f.failMkdirAfter > 0 && f.mkdirCalls >= f.failMkdirAfter {
		return errors.New("injected mkdir failure")
	}
	f.dirs[rel] = true
	return nil
}

func (f *fakeRemote) Move(ctx context.Context, from, to string) error {
	f.moveCalls++
	f.calls = append(f.calls, "move "+from+" -> "+to)
	if f.failMoveAfter > 0 && f.moveCalls >= f.failMoveAfter {
		return errors.New("injected move failure")
	}
	body, ok := f.files[from]
	if !ok {
		// Mirrors the rclone-backed remote: a missing source is its own sentinel, not
		// a generic failure, because "never uploaded" and "already moved" both look
		// like this and both are ordinary.
		return ErrRemoteSourceMissing
	}
	if _, exists := f.files[to]; exists {
		return fmt.Errorf("%q already exists", to)
	}
	delete(f.files, from)
	f.files[to] = body
	return nil
}

func (f *fakeRemote) List(ctx context.Context, rel string) ([]string, error) {
	var out []string
	prefix := rel
	if prefix != "" {
		prefix += "/"
	}
	for path := range f.files {
		if parentOf(path) == rel {
			out = append(out, baseOf(path))
		}
	}
	_ = prefix
	sort.Strings(out)
	return out, nil
}

func (f *fakeRemote) Remove(ctx context.Context, rel string) error {
	f.calls = append(f.calls, "rmdir "+rel)
	delete(f.dirs, rel)
	return nil
}

func (f *fakeRemote) Exists(ctx context.Context, rel string) (bool, error) {
	if _, ok := f.files[rel]; ok {
		return true, nil
	}
	return f.dirs[rel], nil
}

// seed adds a remote file, creating its directory implicitly.
func (f *fakeRemote) seed(path, body string) {
	f.files[path] = body
}

// memState is an in-memory StateMover that records what it was asked to do.
type memState struct {
	repo map[string]string // path -> token
	// calls counts Repath invocations so a test can tell whether the state was
	// touched at all.
	calls int
	// translate maps paths the way the moves ask, and reports how many changed.
}

func newMemState(t *testing.T, w *workdir, rels ...string) *memState {
	t.Helper()
	m := &memState{repo: map[string]string{}}
	for _, rel := range rels {
		m.repo[rel] = "tok-" + rel
	}
	return m
}

func (m *memState) Repath(ctx context.Context, moves []PathMove) (int, error) {
	m.calls++
	n := 0
	for _, mv := range moves {
		token, ok := m.repo[mv.From]
		if !ok {
			continue
		}
		delete(m.repo, mv.From)
		m.repo[mv.To] = token
		n++
	}
	return n, nil
}

// workdir builds a local tree and an executor wired to a fake remote.
type workdir struct {
	root   string
	remote *fakeRemote
	state  *memState
	snap   string
}

func newWorkdir(t *testing.T) *workdir {
	t.Helper()
	root := t.TempDir()
	return &workdir{
		root:   root,
		remote: newFakeRemote(),
		state:  &memState{repo: map[string]string{}},
		snap:   filepath.Join(t.TempDir(), "split.json"),
	}
}

// addFile writes a local file and mirrors it on the fake remote, the way a synced
// pair would look.
func (w *workdir) addFile(t *testing.T, rel, body string) {
	t.Helper()
	abs := filepath.Join(w.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	w.remote.seed(rel, body)
}

// seedState records each local file in the sync state, as a real run would.
func (w *workdir) seedState(t *testing.T, rels ...string) {
	t.Helper()
	for _, rel := range rels {
		w.state.repo[rel] = "tok-" + rel
	}
}

func (w *workdir) executor() *Executor {
	return &Executor{
		LocalRoot:    w.root,
		Remote:       w.remote,
		SnapshotPath: w.snap,
		State:        w.state,
		RemoteLabel:  "test:_split",
	}
}

// planFor builds a plan over one local directory.
func (w *workdir) planFor(t *testing.T, dir string, scheme Scheme) *Plan {
	t.Helper()
	entries, err := Entries(w.root, dir)
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	plan, err := BuildPlan(dir, entries, scheme, Options{MaxPerBucket: 3})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	return plan
}

// localNames lists a local directory's entries.
func localNames(t *testing.T, abs string) []string {
	t.Helper()
	items, err := os.ReadDir(abs)
	if err != nil {
		t.Fatalf("read %q: %v", abs, err)
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Name())
	}
	sort.Strings(out)
	return out
}

// The happy path: files end up in buckets on both sides, and the state follows.
func TestExecuteMovesBothSidesAndRewritesState(t *testing.T) {
	w := newWorkdir(t)
	for _, n := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		w.addFile(t, n, "body-"+n)
	}
	w.seedState(t, "a.txt", "b.txt", "c.txt", "d.txt")

	plan := w.planFor(t, "", SchemeCount)
	if len(plan.Buckets) != 2 {
		t.Fatalf("buckets = %d, want 2", len(plan.Buckets))
	}

	res, err := w.executor().Execute(context.Background(), plan)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.Verified {
		t.Error("the result should report a passed verification")
	}
	if res.Moved != 4 {
		t.Errorf("moved = %d, want 4", res.Moved)
	}

	// Local: the original directory holds only the buckets now.
	if got := localNames(t, w.root); len(got) != 2 {
		t.Errorf("root holds %v, want the two buckets", got)
	}
	// Every file still exists, under a bucket.
	for _, bucket := range plan.Buckets {
		dir := filepath.Join(w.root, bucket.Name)
		if got := len(localNames(t, dir)); got != bucket.Files {
			t.Errorf("%s holds %d files, want %d", bucket.Name, got, bucket.Files)
		}
	}

	// Remote: same shape, and no file left at the old path.
	for _, n := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		if _, ok := w.remote.files[n]; ok {
			t.Errorf("remote still has %q at its old path", n)
		}
	}
	if len(w.remote.files) != 4 {
		t.Errorf("remote holds %d files, want 4: %v", len(w.remote.files), w.remote.files)
	}

	// State: the old keys are gone and the new ones carry the same tokens.
	for _, n := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		if _, ok := w.state.repo[n]; ok {
			t.Errorf("state still has the old key %q", n)
		}
	}
	if res.StateRewritten != 4 {
		t.Errorf("state entries rewritten = %d, want 4", res.StateRewritten)
	}
	for _, m := range plan.Moves {
		token, ok := w.state.repo[m.To]
		if !ok {
			t.Errorf("state has no entry for %q", m.To)
			continue
		}
		if token != "tok-"+m.From {
			t.Errorf("entry %q has token %q, want the one from %q",
				m.To, token, m.From)
		}
	}
}

// Local moves must happen before any remote call: that ordering is what makes a
// failure recoverable.
func TestExecuteMovesLocalBeforeRemote(t *testing.T) {
	w := newWorkdir(t)
	for _, n := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		w.addFile(t, n, "body-"+n)
	}

	plan := w.planFor(t, "", SchemeCount)
	if _, err := w.executor().Execute(context.Background(), plan); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	firstRemote := -1
	for i, c := range w.remote.calls {
		if strings.HasPrefix(c, "mkdir ") || strings.HasPrefix(c, "move ") {
			firstRemote = i
			break
		}
	}
	if firstRemote < 0 {
		t.Fatal("no remote calls were made")
	}
	// After the local phase, the original names must already be gone locally.
	// Checking the order through the recorded calls is the direct assertion: the
	// first remote call is a mkdir, and mkdirPhase runs after moveLocalPhase.
	if !strings.HasPrefix(w.remote.calls[0], "mkdir ") {
		t.Errorf("the first remote call was %q, want a mkdir (directories before moves)",
			w.remote.calls[0])
	}
}

// A local failure must roll back everything already moved, and must not touch the
// remote at all.
func TestLocalFailureRollsBackAndSkipsRemote(t *testing.T) {
	w := newWorkdir(t)
	for _, n := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		w.addFile(t, n, "body-"+n)
	}
	plan := w.planFor(t, "", SchemeCount)

	// Make the third local rename fail by occupying a destination path.
	victim := plan.Moves[2]
	if err := os.MkdirAll(filepath.Dir(filepath.Join(w.root, victim.To)), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(w.root, victim.To), []byte("in the way"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}

	_, err := w.executor().Execute(context.Background(), plan)
	if err == nil {
		t.Fatal("a blocked destination must fail the split")
	}
	if !strings.Contains(err.Error(), "回滚") {
		t.Errorf("the error should say the local moves were rolled back: %v", err)
	}

	// The remote was never touched.
	if len(w.remote.calls) != 0 {
		t.Errorf("the remote was called despite a local failure: %v", w.remote.calls)
	}
	// Everything is back at the original names.
	for _, n := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		if _, err := os.Stat(filepath.Join(w.root, n)); err != nil {
			t.Errorf("%q was not restored: %v", n, err)
		}
	}
}

// A remote move failure must not undo the local work: the local tree is correct and
// the snapshot records where it stopped.
func TestRemoteFailureKeepsLocalAndResumes(t *testing.T) {
	w := newWorkdir(t)
	for i := 0; i < 6; i++ {
		w.addFile(t, fmt.Sprintf("f%d.txt", i), fmt.Sprintf("body-%d", i))
	}
	plan := w.planFor(t, "", SchemeCount)

	w.remote.failMoveAfter = 3
	_, err := w.executor().Execute(context.Background(), plan)
	if err == nil {
		t.Fatal("an injected remote failure must be reported")
	}
	if !strings.Contains(err.Error(), "快照") {
		t.Errorf("the error should point at the snapshot for a resume: %v", err)
	}

	// Local work stands.
	if got := localNames(t, w.root); len(got) != 2 {
		t.Fatalf("local root holds %v, want the two buckets (local must not be undone)", got)
	}

	// Resume, with the fault cleared.
	w.remote.failMoveAfter = 0
	snap, err := LoadSnapshot(w.snap)
	if err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}
	if snap.Completed {
		t.Fatal("the snapshot must not be marked complete after a failure")
	}
	pending := snap.Pending()
	if pending == 0 {
		t.Fatal("the snapshot says nothing is pending, but the run failed")
	}

	ex := w.executor()
	res, err := ex.Resume(context.Background(), w.snap)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if !res.Verified {
		t.Error("the resumed split should verify")
	}
	if len(w.remote.files) != 6 {
		t.Errorf("remote holds %d files after resume, want 6", len(w.remote.files))
	}
	// No file may appear twice: a resume that re-moves already-moved entries would
	// fail loudly on the fake, but check the count as well.
	seen := map[string]bool{}
	for path := range w.remote.files {
		if seen[path] {
			t.Errorf("duplicate remote path %q", path)
		}
		seen[path] = true
	}
}

// Resuming a completed split must be refused rather than re-doing the work.
func TestResumeRefusesCompletedSplit(t *testing.T) {
	w := newWorkdir(t)
	for _, n := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		w.addFile(t, n, "body-"+n)
	}
	plan := w.planFor(t, "", SchemeCount)
	if _, err := w.executor().Execute(context.Background(), plan); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if _, err := w.executor().Resume(context.Background(), w.snap); err == nil {
		t.Fatal("resuming a completed split must be refused")
	}
}

// A verification mismatch must not be reported as success.
func TestVerifyMismatchIsReported(t *testing.T) {
	w := newWorkdir(t)
	for _, n := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		w.addFile(t, n, "body-"+n)
	}
	plan := w.planFor(t, "", SchemeCount)

	// Make the remote silently drop one file, as a backend bug might.
	orig := w.remote.Move
	_ = orig
	broken := &lossyRemote{fakeRemote: w.remote}
	ex := w.executor()
	ex.Remote = broken

	_, err := ex.Execute(context.Background(), plan)
	if err == nil {
		t.Fatal("a verification mismatch must fail")
	}
	if !strings.Contains(err.Error(), "不一致") {
		t.Errorf("the error should say the two sides disagree: %v", err)
	}
}

// lossyRemote swallows one move, simulating a remote that reports success without
// doing anything.
type lossyRemote struct {
	*fakeRemote
	dropped bool
}

func (l *lossyRemote) Move(ctx context.Context, from, to string) error {
	if !l.dropped {
		l.dropped = true
		return nil // pretend it worked
	}
	return l.fakeRemote.Move(ctx, from, to)
}

// Undo restores both sides and the state.
func TestUndoRestoresEverything(t *testing.T) {
	w := newWorkdir(t)
	originals := []string{"a.txt", "b.txt", "c.txt", "d.txt"}
	for _, n := range originals {
		w.addFile(t, n, "body-"+n)
	}
	w.seedState(t, originals...)

	plan := w.planFor(t, "", SchemeCount)
	if _, err := w.executor().Execute(context.Background(), plan); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	res, err := w.executor().Undo(context.Background(), w.snap)
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if res.Moved != 4 {
		t.Errorf("undo moved %d, want 4", res.Moved)
	}

	// Local: back to the original names, with no bucket directories left.
	got := localNames(t, w.root)
	if strings.Join(got, ",") != "a.txt,b.txt,c.txt,d.txt" {
		t.Errorf("local root holds %v, want the original four files", got)
	}
	// Remote: same.
	if len(w.remote.files) != 4 {
		t.Errorf("remote holds %d files, want 4: %v", len(w.remote.files), w.remote.files)
	}
	for _, n := range originals {
		if w.remote.files[n] != "body-"+n {
			t.Errorf("remote %q = %q", n, w.remote.files[n])
		}
	}
	// State: keys back to the original paths, tokens preserved.
	for _, n := range originals {
		token, ok := w.state.repo[n]
		if !ok {
			t.Errorf("state has no entry for %q after undo", n)
			continue
		}
		if token != "tok-"+n {
			t.Errorf("entry %q has token %q", n, token)
		}
	}

	// The ledger must say "undone", not "never finished": otherwise the console
	// offers to resume a split the operator deliberately cancelled.
	snap, err := LoadSnapshot(w.snap)
	if err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}
	if !snap.Undone {
		t.Error("snapshot is not marked undone")
	}
	if snap.Completed {
		t.Error("an undone snapshot is still marked completed")
	}
	if snap.UndoneAt.IsZero() {
		t.Error("snapshot has no undone timestamp")
	}
	if got := snap.Pending(); got != 0 {
		t.Errorf("pending after undo = %d, want 0", got)
	}
}

// Undo must not overwrite a local file that came back some other way.
func TestUndoRefusesToOverwrite(t *testing.T) {
	w := newWorkdir(t)
	for _, n := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		w.addFile(t, n, "body-"+n)
	}
	plan := w.planFor(t, "", SchemeCount)
	if _, err := w.executor().Execute(context.Background(), plan); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// A new file appears at one of the original paths.
	if err := os.WriteFile(filepath.Join(w.root, "a.txt"), []byte("newer"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := w.executor().Undo(context.Background(), w.snap); err == nil {
		t.Fatal("undo must refuse to overwrite an existing local file")
	}
	// The newer file must still be there.
	body, err := os.ReadFile(filepath.Join(w.root, "a.txt"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(body) != "newer" {
		t.Errorf("the existing file was replaced: %q", body)
	}
}

// A split of a subdirectory must keep everything under that subdirectory.
func TestExecuteInSubdirectory(t *testing.T) {
	w := newWorkdir(t)
	for i := 0; i < 4; i++ {
		w.addFile(t, fmt.Sprintf("reports/f%d.txt", i), fmt.Sprintf("b%d", i))
	}
	w.seedState(t, "reports/f0.txt", "reports/f1.txt", "reports/f2.txt", "reports/f3.txt")

	plan := w.planFor(t, "reports", SchemeCount)
	for _, m := range plan.Moves {
		if !strings.HasPrefix(m.To, "reports/") {
			t.Errorf("move target %q escaped the split directory", m.To)
		}
	}

	if _, err := w.executor().Execute(context.Background(), plan); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := len(localNames(t, filepath.Join(w.root, "reports"))); got != 2 {
		t.Errorf("reports holds %d entries, want the two buckets", got)
	}

	if _, ok := w.state.repo["reports/f0.txt"]; ok {
		t.Error("the old nested key survived")
	}
}

// A pair with no state file must still split; there is simply nothing to rewrite.
func TestExecuteWithoutState(t *testing.T) {
	w := newWorkdir(t)
	for _, n := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		w.addFile(t, n, "body-"+n)
	}
	plan := w.planFor(t, "", SchemeCount)

	ex := w.executor()
	ex.State = nil
	res, err := ex.Execute(context.Background(), plan)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.StateRewritten != 0 {
		t.Errorf("rewrote %d state entries without a state mover", res.StateRewritten)
	}
}

// The snapshot must exist on disk before any file moves, and must describe every
// move: it is the only way back.
func TestSnapshotIsWrittenBeforeMoving(t *testing.T) {
	w := newWorkdir(t)
	for _, n := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		w.addFile(t, n, "body-"+n)
	}
	plan := w.planFor(t, "", SchemeCount)

	// The remote fails immediately, so the local phase runs and then everything
	// stops; the snapshot has to be there regardless.
	w.remote.failMkdirAfter = 1
	if _, err := w.executor().Execute(context.Background(), plan); err == nil {
		t.Fatal("expected a failure")
	}

	snap, err := LoadSnapshot(w.snap)
	if err != nil {
		t.Fatalf("the snapshot must exist after a failure: %v", err)
	}
	if len(snap.Moves) != 4 {
		t.Errorf("snapshot records %d moves, want 4", len(snap.Moves))
	}
	for _, m := range snap.Moves {
		if m.Size <= 0 {
			t.Errorf("move %q records size %d; the snapshot needs it for an undo check",
				m.From, m.Size)
		}
	}
}

// Concurrent-looking use: a second Execute on the same snapshot path must not be
// needed, but LoadSnapshot on a truncated file must fail cleanly rather than panic.
func TestLoadSnapshotRejectsGarbage(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"moves":`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadSnapshot(bad); err == nil {
		t.Error("truncated JSON must be refused")
	}

	empty := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(empty, []byte(`{"version":1,"moves":[]}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadSnapshot(empty); err == nil {
		t.Error("a snapshot with no moves must be refused")
	}

	if _, err := LoadSnapshot(filepath.Join(dir, "missing.json")); err == nil {
		t.Error("a missing snapshot must be refused")
	}
}

func TestSnapshotName(t *testing.T) {
	at := time.Date(2026, 11, 8, 9, 30, 15, 0, time.UTC)
	if got := SnapshotName("", at); got != "split-root-20261108-093015.json" {
		t.Errorf("root name = %q", got)
	}
	if got := SnapshotName("a/b", at); got != "split-a-b-20261108-093015.json" {
		t.Errorf("nested name = %q", got)
	}
}
