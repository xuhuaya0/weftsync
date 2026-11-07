package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rclone/rclone/fs"

	"weftsync/job"
	"weftsync/split"
)

// The split wizard's server side.
//
// Three endpoints, and the split between them is the safety property: the plan is a
// pure read, and executing it needs a confirmation string that a person has to read
// the plan to know. There is no single call that both decides and acts.
//
// Nothing here ever splits automatically. A scheduled run that hits the limit is
// refused with a clear message (see job.RunPair) and recorded as a skipped run; the
// user comes to this page.

// SplitView is what the wizard renders.
type SplitView struct {
	Job  string `json:"job"`
	Pair string `json:"pair"`
	// Dir is the directory that would be split, relative to the pair root.
	Dir string `json:"dir"`
	// RefuseAt is the threshold this build uses.
	RefuseAt int `json:"refuse_at"`
	// FeishuLimit is the backend's documented limit, for context.
	FeishuLimit int `json:"feishu_limit"`
	// Findings is every over-limit directory, largest first.
	Findings []SplitFinding `json:"findings"`
}

// SplitFinding is one over-limit directory.
type SplitFinding struct {
	RelPath string `json:"rel_path"`
	Files   int    `json:"files"`
	Dirs    int    `json:"dirs"`
	Total   int    `json:"total"`
}

// PlanView is a plan plus the confirmation string the user must type.
type PlanView struct {
	Job    string `json:"job"`
	Pair   string `json:"pair"`
	Dir    string `json:"dir"`
	Scheme string `json:"scheme"`
	// Confirm is the exact text required to execute, so the page can show it.
	Confirm string        `json:"confirm"`
	Buckets []SplitBucket `json:"buckets"`
	// Notes carry caveats such as a bucket having to be divided again.
	Notes []string `json:"notes,omitempty"`
	Files int      `json:"files"`
	Bytes int64    `json:"bytes"`
	// Samples shows the first few mappings, so the user can see the shape.
	Samples []SplitSample `json:"samples,omitempty"`
}

// SplitBucket is one target directory.
type SplitBucket struct {
	Name  string `json:"name"`
	Files int    `json:"files"`
	Bytes int64  `json:"bytes"`
}

// SplitSample is one example mapping.
type SplitSample struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// SnapshotsView lists the undo ledgers for a pair.
type SnapshotsView struct {
	Job       string          `json:"job"`
	Pair      string          `json:"pair"`
	Snapshots []SnapshotBrief `json:"snapshots"`
}

// SnapshotBrief describes one ledger without its move list.
type SnapshotBrief struct {
	Path      string    `json:"path"`
	Dir       string    `json:"dir"`
	Scheme    string    `json:"scheme"`
	CreatedAt time.Time `json:"created_at"`
	Moves     int       `json:"moves"`
	Pending   int       `json:"pending"`
	Completed bool      `json:"completed"`
	Undone    bool      `json:"undone"`
}

// pairOf resolves the job and pair a request refers to, refusing while the job runs.
func (a *API) pairOf(r *http.Request) (*job.Config, *job.Pair, error) {
	jobName := r.URL.Query().Get("job")
	if jobName == "" {
		return nil, nil, errors.New(`the job query parameter is required`)
	}
	cfg, err := a.D.ConfigOf(jobName)
	if err != nil {
		return nil, nil, err
	}

	pairName := r.URL.Query().Get("pair")
	if pairName == "" {
		if len(cfg.Pair) == 1 {
			return cfg, &cfg.Pair[0], nil
		}
		return nil, nil, errors.New(
			"this job has several pairs; the pair query parameter is required")
	}
	for i := range cfg.Pair {
		if cfg.Pair[i].Name == pairName {
			return cfg, &cfg.Pair[i], nil
		}
	}
	return nil, nil, fmt.Errorf("no pair named %q in job %q", pairName, jobName)
}

// statusFor maps an error from pairOf to an HTTP status.
func statusFor(err error) int {
	switch {
	case errors.Is(err, ErrNoSuchJob):
		return http.StatusNotFound
	case errors.Is(err, ErrJobRunning):
		return http.StatusConflict
	}
	// A bad parameter is the common case for everything else.
	return http.StatusBadRequest
}

// handleSplitPrecheck reports directories that are over the limit.
func (a *API) handleSplitPrecheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	cfg, p, err := a.pairOf(r)
	if err != nil {
		writeError(w, statusFor(err), err.Error())
		return
	}

	findings, err := split.Precheck(p.LocalPath(), split.PrecheckOptions{})
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	view := SplitView{
		Job: cfg.Job.Name, Pair: p.Name,
		RefuseAt:    split.DefaultRefuseAt,
		FeishuLimit: split.FeishuLimit,
	}
	for _, f := range findings {
		view.Findings = append(view.Findings, SplitFinding{
			RelPath: f.RelPath, Files: f.Files, Dirs: f.Dirs, Total: f.Total(),
		})
	}
	// The biggest offender is the default target: it is the one that will fail
	// first.
	if len(findings) > 0 {
		view.Dir = findings[0].RelPath
	}
	writeJSON(w, http.StatusOK, view)
}

// handleSplitPlan builds a plan without touching anything.
func (a *API) handleSplitPlan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	cfg, p, err := a.pairOf(r)
	if err != nil {
		writeError(w, statusFor(err), err.Error())
		return
	}

	dir, scheme, err := splitParams(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	plan, err := buildSplitPlan(p, dir, scheme)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, planView(cfg, p, plan))
}

// splitParams reads and validates the shared query parameters.
func splitParams(r *http.Request) (string, split.Scheme, error) {
	rawDir := r.URL.Query().Get("dir")
	dir, err := split.CleanRelPath(rawDir)
	if err != nil {
		return "", "", err
	}
	scheme := split.Scheme(r.URL.Query().Get("scheme"))
	if scheme == "" {
		scheme = split.SchemeType
	}
	if !split.ValidScheme(scheme) {
		return "", "", fmt.Errorf("unknown scheme %q (expected type, initial, year or count)", scheme)
	}
	return dir, scheme, nil
}

// buildSplitPlan reads one directory and builds a plan.
func buildSplitPlan(p *job.Pair, dir string, scheme split.Scheme) (*split.Plan, error) {
	entries, err := split.Entries(p.LocalPath(), dir)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("目录 %q 是空的，没有可拆分的内容", displayDir(dir))
	}
	return split.BuildPlan(dir, entries, scheme, split.Options{})
}

// planView formats a plan for the page.
func planView(cfg *job.Config, p *job.Pair, plan *split.Plan) PlanView {
	view := PlanView{
		Job: cfg.Job.Name, Pair: p.Name,
		Dir: plan.Dir, Scheme: string(plan.Scheme),
		Notes:   plan.Notes,
		Files:   plan.Total(),
		Bytes:   plan.Bytes(),
		Confirm: confirmWord(plan.Dir),
	}
	for _, b := range plan.Buckets {
		view.Buckets = append(view.Buckets, SplitBucket{Name: b.Name, Files: b.Files, Bytes: b.Bytes})
	}
	for i, m := range plan.Moves {
		if i >= 5 {
			break
		}
		view.Samples = append(view.Samples, SplitSample{From: m.From, To: m.To})
	}
	return view
}

// confirmWord is the text a user has to type to execute.
//
// The directory's own name, as PLAN.md specifies: it can only be produced by
// reading the plan, so it cannot be clicked through by accident. The root directory
// has no name, so it gets a word that is equally deliberate.
func confirmWord(dir string) string {
	if dir == "" {
		return "root"
	}
	if i := strings.LastIndex(dir, "/"); i >= 0 {
		return dir[i+1:]
	}
	return dir
}

func displayDir(dir string) string {
	if dir == "" {
		return "(根目录)"
	}
	return dir
}

// handleSplitExecute performs the split.
//
// The confirmation is required here, not only in the page: an HTTP client is not
// the page, and a destructive endpoint that trusts its caller to have asked is not
// a safeguard.
func (a *API) handleSplitExecute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	cfg, p, err := a.pairOf(r)
	if err != nil {
		writeError(w, statusFor(err), err.Error())
		return
	}

	dir, scheme, err := splitParams(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if got, want := r.URL.Query().Get("confirm"), confirmWord(dir); got != want {
		writeError(w, http.StatusBadRequest, fmt.Sprintf(
			`要执行拆分，必须把 confirm 设为目录名 %q（当前是 %q）`, want, got))
		return
	}

	// One split at a time per job: two would move the same files under each other.
	if !a.D.beginSplit(cfg.Job.Name) {
		writeError(w, http.StatusConflict, "this job is already being split")
		return
	}
	defer a.D.endSplit(cfg.Job.Name)

	plan, err := buildSplitPlan(p, dir, scheme)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	ex, err := a.splitExecutor(r, cfg, p, dir)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	// Generous but bounded: the remote is rate limited and a large split is
	// thousands of calls. The request stays open so the page can report progress,
	// and the snapshot makes a timeout recoverable.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Hour)
	defer cancel()

	res, err := ex.Execute(ctx, plan)
	if err != nil {
		// 500 rather than 400: the request was fine, the operation failed. The
		// message carries the snapshot path for a resume.
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error":  err.Error(),
			"result": res,
		})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// splitExecutor builds an executor for a pair.
func (a *API) splitExecutor(r *http.Request, cfg *job.Config, p *job.Pair, dir string) (*split.Executor, error) {
	remotePath, err := p.RemotePath()
	if err != nil {
		return nil, err
	}
	// The same Fs the sync path uses, so a failure here is the failure a run would
	// hit, and so the backend's in-memory state is the one that gets updated.
	fdst, err := fs.NewFs(context.WithoutCancel(r.Context()), remotePath)
	if err != nil {
		return nil, fmt.Errorf("打开云端 %q: %w", remotePath, err)
	}

	return &split.Executor{
		LocalRoot:    p.LocalPath(),
		Remote:       split.NewRcloneRemote(fdst),
		State:        split.NewRcloneStateMover(fdst),
		SnapshotPath: a.snapshotPath(cfg, p, dir),
		RemoteLabel:  remotePath,
	}, nil
}

// snapshotDir is the directory a pair's ledgers live in.
//
// Next to the pair's state file when there is one: that is the pair's own
// directory, it is already backed up and ignored by git, and it keeps a pair's
// history together. Otherwise the user config directory, since writing into the
// synced folder would upload the ledger itself.
func (a *API) snapshotDir(cfg *job.Config, p *job.Pair) string {
	if dir := strings.TrimSpace(a.SplitDir); dir != "" {
		return dir
	}
	if sf := strings.TrimSpace(p.Remote.Options["state_file"]); sf != "" {
		return filepath.Dir(sf)
	}
	base, err := os.UserConfigDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "weftsync", "splits", safeName(cfg.Job.Name+"-"+p.Name))
}

// snapshotPath is where one split's undo ledger lives.
func (a *API) snapshotPath(cfg *job.Config, p *job.Pair, dir string) string {
	return filepath.Join(a.snapshotDir(cfg, p), split.SnapshotName(dir, time.Now()))
}

// safeName keeps a job or pair name usable as a directory name.
func safeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return "pair"
	}
	return b.String()
}

// handleSplitSnapshots lists the undo ledgers found for a pair.
func (a *API) handleSplitSnapshots(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	cfg, p, err := a.pairOf(r)
	if err != nil {
		writeError(w, statusFor(err), err.Error())
		return
	}

	// One directory, the one a new ledger would be written to. Searching both
	// possible homes was tempting but misleading: snapshotDir already resolves
	// which one applies to this pair, and a listing that mixed two places would
	// report ledgers the undo endpoint would then refuse.
	dir := a.snapshotDir(cfg, p)
	view := SnapshotsView{Job: cfg.Job.Name, Pair: p.Name}

	items, err := os.ReadDir(dir)
	if err != nil {
		// No directory means no ledgers, which is not an error.
		writeJSON(w, http.StatusOK, view)
		return
	}
	for _, it := range items {
		if it.IsDir() || !strings.HasPrefix(it.Name(), "split-") ||
			!strings.HasSuffix(it.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, it.Name())
		snap, err := split.LoadSnapshot(path)
		if err != nil {
			// A leftover .tmp or a file another feature wrote; skip it rather than
			// failing the listing.
			continue
		}
		view.Snapshots = append(view.Snapshots, SnapshotBrief{
			Path: path, Dir: snap.Dir, Scheme: string(snap.Scheme),
			CreatedAt: snap.CreatedAt, Moves: len(snap.Moves),
			Pending: snap.Pending(), Completed: snap.Completed,
			Undone: snap.Undone,
		})
	}
	sort.SliceStable(view.Snapshots, func(i, j int) bool {
		return view.Snapshots[i].CreatedAt.After(view.Snapshots[j].CreatedAt)
	})
	writeJSON(w, http.StatusOK, view)
}

// handleSplitUndo moves a split's files back.
func (a *API) handleSplitUndo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	cfg, p, err := a.pairOf(r)
	if err != nil {
		writeError(w, statusFor(err), err.Error())
		return
	}
	snapPath := strings.TrimSpace(r.URL.Query().Get("snapshot"))
	if snapPath == "" {
		writeError(w, http.StatusBadRequest, "the snapshot query parameter is required")
		return
	}
	// Only ledgers this pair could own. Accepting any path would make this an
	// endpoint that reads and acts on an arbitrary file.
	if !a.snapshotBelongsTo(cfg, p, snapPath) {
		writeError(w, http.StatusBadRequest,
			"that snapshot does not belong to this pair")
		return
	}

	if !a.D.beginSplit(cfg.Job.Name) {
		writeError(w, http.StatusConflict, "this job is already being split")
		return
	}
	defer a.D.endSplit(cfg.Job.Name)

	ex, err := a.splitExecutor(r, cfg, p, "")
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Hour)
	defer cancel()

	res, err := ex.Undo(ctx, snapPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": err.Error(), "result": res,
		})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// snapshotBelongsTo checks that a path is a ledger in this pair's snapshot
// directory.
func (a *API) snapshotBelongsTo(cfg *job.Config, p *job.Pair, snapPath string) bool {
	target := absPath(snapPath)
	dir := absPath(a.snapshotDir(cfg, p))

	return filepath.Dir(target) == dir &&
		strings.HasPrefix(filepath.Base(target), "split-") &&
		strings.HasSuffix(target, ".json")
}

func absPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	return filepath.Clean(abs)
}

// beginSplit claims the per-job split slot.
func (d *Daemon) beginSplit(name string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.splitting == nil {
		d.splitting = map[string]bool{}
	}
	if d.splitting[name] {
		return false
	}
	d.splitting[name] = true
	return true
}

// endSplit releases it.
func (d *Daemon) endSplit(name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.splitting, name)
}
