// Package daemon runs jobs unattended: a scheduler, a small local HTTP API, and
// the bookkeeping the UI needs to show what is going on.
//
// Why it is its own package rather than part of `job`: `job` answers "how do I
// run this pair safely", and the daemon answers "when, how often, and how do I
// observe it". Keeping them apart means the safety rules can be tested without
// any clock or network, which is how the delete guard got covered in the first
// place.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"weftsync/job"
	"weftsync/remedy"
)

// RunState is what a job is doing right now.
type RunState string

const (
	// StateIdle means no run in progress.
	StateIdle RunState = "idle"
	// StateRunning means a run is in progress.
	StateRunning RunState = "running"
	// StateDisabled means the job file says enabled = false.
	StateDisabled RunState = "disabled"
)

// RunMode distinguishes a real run from a preview.
type RunMode string

const (
	// ModeRun changes the destination.
	ModeRun RunMode = "run"
	// ModePlan measures what a run would do and changes nothing.
	ModePlan RunMode = "plan"
)

// JobStatus is the observable state of one job.
type JobStatus struct {
	Name    string   `json:"name"`
	File    string   `json:"file"`
	State   RunState `json:"state"`
	Enabled bool     `json:"enabled"`
	Pairs   int      `json:"pairs"`
	// PairNames lists the pairs by name so the console can offer one session per
	// pair. A count alone cannot address the second pair of a multi-pair job.
	PairNames []string   `json:"pair_names,omitempty"`
	Schedule  string     `json:"schedule"`
	NextRun   *time.Time `json:"next_run,omitempty"`
	LastRun   *time.Time `json:"last_run,omitempty"`
	LastOK    *bool      `json:"last_ok,omitempty"`
	LastError string     `json:"last_error,omitempty"`
	// LastRemedy explains the failure in terms the console can act on: which kind
	// of failure it was, the facts behind it, and the commands that fix it.
	//
	// Without it the console can only print the backend's English sentence, which
	// for a half-finished replacement reads like "the upload failed" when the
	// upload in fact succeeded. See package remedy.
	LastRemedy *remedy.Remedy `json:"last_remedy,omitempty"`
	// Skipped counts runs that were not started because the previous one was
	// still going.
	Skipped int `json:"skipped"`
	// LastResult summarises the previous run.
	LastResult []job.PairResult `json:"last_result,omitempty"`

	// Mode is what the current (or last) run was: a real run or a preview.
	Mode RunMode `json:"mode,omitempty"`
	// Plan is the most recent preview for this job, so the UI can show what a run
	// would do without having to read history.
	Plan *PlanStatus `json:"plan,omitempty"`
	// CanStop is true while a run can still be cancelled.
	CanStop bool `json:"can_stop"`
	// LastStopped means the last run was cut short on request rather than failing.
	LastStopped bool `json:"last_stopped,omitempty"`
	// Progress is live counters while a run is in flight.
	Progress *job.Progress `json:"progress,omitempty"`
}

// PlanStatus is the result of the most recent preview.
type PlanStatus struct {
	At      time.Time        `json:"at"`
	Results []job.PairResult `json:"results"`
	// Error is why the preview could not be built.
	//
	// Without it a failed preview is indistinguishable from "nothing to do": both
	// arrive as zero counts, and the UI would tell the user there is nothing to
	// change when in fact the destination could not even be opened.
	Error string `json:"error,omitempty"`
	// Remedy is the actionable form of Error, when we recognise it.
	Remedy *remedy.Remedy `json:"remedy,omitempty"`
	// GuardHit means a real run would be refused by the delete guard, so the
	// preview is also the explanation for why nothing is happening.
	GuardHit bool `json:"guard_hit"`
	// Conflicts and totals are copied out so the UI does not have to re-add them.
	Conflicts   int   `json:"conflicts"`
	Transferred int64 `json:"transferred"`
	Deleted     int64 `json:"deleted"`
}

// entry is one scheduled job file.
type entry struct {
	file     string
	cfg      *job.Config
	schedule job.ResolvedSchedule

	// running guards against overlapping runs of the same job.
	running bool
	// cancelRun aborts the in-flight run; nil when idle. Kept here rather than
	// passed around the API so "stop" does not have to find the goroutine.
	cancelRun context.CancelFunc
	// mode is what the current or last run was.
	mode RunMode
	// tracker owns this job's accounting group so its progress cannot mix with
	// another job's.
	tracker *job.Tracker
	// phase is what the run is doing right now (opening, scanning, comparing
	// contents, writing). Guarded by d.mu, unlike tracker, because it is written by
	// the run goroutine through a callback and read by the API.
	phase    job.Phase
	hasPhase bool

	name    string
	enabled bool
	pairs   int
	// pairNames mirrors the job file's pair names, in order.
	pairNames []string
	nextRun   time.Time
	lastRun   time.Time
	lastOK    bool
	hasRun    bool
	lastError string
	// lastRemedy is the actionable form of lastError; nil when the failure was
	// not one we can explain. Kept separate from lastError because the console
	// shows one as prose and the other as steps.
	lastRemedy *remedy.Remedy
	skipped    int
	lastRes    []job.PairResult
	// lastPlan is the most recent preview; nil until one has run.
	lastPlan *PlanStatus
	// lastStopped records that the last run was cut short on request, which is
	// not the same as a failure and must not read like one.
	lastStopped bool
}

// Daemon owns the jobs and their schedule.
type Daemon struct {
	mu      sync.Mutex
	entries []*entry
	log     io.Writer
	now     func() time.Time

	// runJob is injectable so tests can avoid touching the network.
	runJob func(ctx context.Context, cfg *job.Config, w io.Writer, opt job.Options) ([]job.PairResult, error)

	// ctx is the daemon's own lifetime. Runs must NOT inherit the context of
	// whoever asked for them: an HTTP request's context is cancelled the moment
	// the handler returns, which would abort the transfer it just started
	// (observed: "cannot open root folder ... context canceled" 26ms into a run
	// that the API had accepted).
	ctx    context.Context
	cancel context.CancelFunc

	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup

	// history and logs back the UI's "what happened" views.
	history *ring[RunRecord]
	logs    *logSink

	// splitting marks jobs with a split in flight. A split moves files on both
	// sides, so two at once on one job would move the same files under each other.
	splitting map[string]bool

	// notify raises a desktop notification. Nil means "no tray", which is the
	// normal case on Linux and on a headless Windows box; every caller must
	// tolerate it.
	notify func(title, text string)

	// settings is the daemon-level configuration read from disk at startup and
	// replaced whenever the settings page saves. Guarded by mu like the rest.
	settings Settings
}

// SetNotifier installs the callback used to report a run that needs attention.
//
// Only failures, guard hits and conflicts are reported. A notification for every
// successful scheduled run would be noise that trains the user to ignore the one
// that matters.
func (d *Daemon) SetNotifier(fn func(title, text string)) {
	d.mu.Lock()
	d.notify = fn
	d.mu.Unlock()
}

// notifyf raises a notification when one is installed.
func (d *Daemon) notifyf(title, format string, args ...any) {
	d.mu.Lock()
	fn := d.notify
	enabled := d.settings.NotificationsOn()
	d.mu.Unlock()
	if fn == nil || !enabled {
		return
	}
	// Logged as well as shown: a balloon disappears, and "was I told about
	// this?" is a question the log has to be able to answer.
	d.logf("notify: %s", title)
	fn(title, fmt.Sprintf(format, args...))
}

// HasNotifier reports whether a desktop notifier is installed.
//
// The settings page uses it to disable the notification switch with a reason
// instead of offering a control that cannot do anything (no tray, Linux, or a
// headless box).
func (d *Daemon) HasNotifier() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.notify != nil
}

// ApplySettings installs settings that were just saved, so a change takes effect
// without restarting the daemon.
func (d *Daemon) ApplySettings(s Settings) {
	d.mu.Lock()
	d.settings = s
	d.mu.Unlock()
}

// SettingsOf returns the settings currently in force.
func (d *Daemon) SettingsOf() Settings {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.settings
}

// HistoryLen is how many run records this process still holds.
func (d *Daemon) HistoryLen() int {
	if d.history == nil {
		return 0
	}
	return d.history.len()
}

// RunningJobs names the jobs that a shutdown would have to wait for.
//
// Stop() waits for them, which is the right behaviour but looks like a hang if
// nothing says so. The command line prints this list before it starts waiting.
func (d *Daemon) RunningJobs() []string {
	var out []string
	for _, st := range d.Status() {
		if st.State == StateRunning {
			out = append(out, st.Name)
		}
	}
	return out
}

// LogLen is how many log lines this process still holds.
func (d *Daemon) LogLen() int {
	d.mu.Lock()
	sink := d.logs
	d.mu.Unlock()
	if sink == nil {
		return 0
	}
	return len(sink.Recent())
}

// New builds a daemon for the given job files.
func New(files []string, log io.Writer) (*Daemon, error) {
	if len(files) == 0 {
		return nil, errors.New("daemon: at least one job file is required")
	}
	d := &Daemon{
		log:     log,
		now:     time.Now,
		stop:    make(chan struct{}),
		history: newRing[RunRecord](historySize),
		runJob: func(ctx context.Context, cfg *job.Config, w io.Writer, opt job.Options) ([]job.PairResult, error) {
			return job.Run(ctx, cfg, w, opt)
		},
	}
	if sink, ok := log.(interface{ Recent() []LogLine }); ok {
		d.logs = sink.(*logSink)
		_ = sink
	}

	// Settings are best effort at startup: a corrupt settings file must not stop
	// the sync. Say so once and run with the defaults.
	if s, err := LoadSettings(); err != nil {
		fmt.Fprintf(log, "settings: %v (using defaults)\n", err)
	} else {
		d.settings = s
	}
	d.ctx, d.cancel = context.WithCancel(context.Background())

	seen := map[string]bool{}
	for _, f := range files {
		cfg, err := job.Load(f)
		if err != nil {
			return nil, err
		}
		if seen[cfg.Job.Name] {
			return nil, fmt.Errorf("daemon: two job files share the name %q", cfg.Job.Name)
		}
		seen[cfg.Job.Name] = true

		sched, err := cfg.EffectiveSchedule()
		if err != nil {
			return nil, err
		}

		name := cfg.Job.Name
		if name == "" {
			name = f
		}
		d.entries = append(d.entries, &entry{
			file:      f,
			cfg:       cfg,
			schedule:  sched,
			name:      name,
			enabled:   cfg.Enabled(),
			pairs:     len(cfg.Pair),
			pairNames: pairNamesOf(cfg),
			nextRun:   d.computeNext(sched, d.now()),
			tracker:   job.NewTracker(statsGroupFor(name)),
		})
	}
	return d, nil
}

// pairNamesOf lists the pairs in file order.
//
// The console addresses one session per pair, so it needs the names; a bare count
// cannot tell it which pair the user picked.
func pairNamesOf(cfg *job.Config) []string {
	out := make([]string, 0, len(cfg.Pair))
	for i := range cfg.Pair {
		out = append(out, cfg.Pair[i].Name)
	}
	return out
}

// computeNext is the single place "when does this run next" is decided.
//
// Cron is resolved here rather than in job.ResolvedSchedule so that the schedule
// type stays free of the cron library; NextAfter covers the other two modes.
func (d *Daemon) computeNext(s job.ResolvedSchedule, from time.Time) time.Time {
	if s.Mode == job.ScheduleCron {
		next, err := job.CronNext(s.Cron, from)
		if err != nil {
			return time.Time{}
		}
		return next
	}
	return s.NextAfter(from)
}

// Start launches the scheduler loop and returns immediately.
//
// The caller's context becoming done stops the scheduler; it does not abort a
// run that is already in progress, so a shutdown never leaves half an upload
// behind without a log line explaining it.
func (d *Daemon) Start(ctx context.Context) {
	if d.cancel != nil {
		// Re-parent the daemon lifetime on the caller's context.
		d.cancel()
		d.ctx, d.cancel = context.WithCancel(ctx)
	}

	for _, e := range d.entries {
		if !e.enabled {
			d.logf("job %q is disabled", e.name)
			continue
		}
		d.logf("job %q: %d pair(s), schedule %s", e.name, e.pairs, e.schedule)
		if !e.nextRun.IsZero() {
			d.logf("job %q: next run %s", e.name, e.nextRun.Format(time.RFC3339))
		}
	}

	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		d.loop(ctx)
	}()
}

// loop wakes up on a short tick and starts whatever is due.
//
// A tick rather than one timer per job: cron expressions can name any minute, and
// a single loop keeps "did anything become due" in one place, which is far easier
// to reason about than a pile of rescheduled timers.
func (d *Daemon) loop(ctx context.Context) {
	tick := time.NewTicker(d.tickInterval())
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-d.stop:
			return
		case <-tick.C:
			d.runDue(ctx)
		}
	}
}

// tickInterval keeps the wake-up cost negligible while staying well under a
// minute of scheduling error.
func (d *Daemon) tickInterval() time.Duration { return time.Second }

// runDue starts every job whose next run has arrived.
func (d *Daemon) runDue(ctx context.Context) {
	now := d.now()

	d.mu.Lock()
	type due struct {
		e *entry
	}
	var started []*entry
	for _, e := range d.entries {
		if !e.enabled || e.nextRun.IsZero() || now.Before(e.nextRun) {
			continue
		}
		// Reschedule first, so a slow run cannot make the job "due" twice.
		next := d.computeNext(e.schedule, now)
		if next.Equal(e.nextRun) {
			// Guard against a schedule that cannot advance (should not happen
			// with validated input, but a stuck job would spin the CPU).
			next = now.Add(e.schedule.Interval)
		}
		e.nextRun = next

		if e.running {
			e.skipped++
			d.logf("job %q: previous run still in progress, skipping this one", e.name)
			continue
		}
		e.running = true
		started = append(started, e)
	}
	d.mu.Unlock()

	for _, e := range started {
		d.wg.Add(1)
		go func(e *entry) {
			defer d.wg.Done()
			d.execute(ctx, e, ModeRun, nil)
		}(e)
	}
}

// execute runs one job and records the outcome.
func (d *Daemon) execute(ctx context.Context, e *entry, mode RunMode, only []string) {
	start := d.now()
	if mode == ModePlan {
		d.logf("job %q: building a preview (nothing will be changed)", e.name)
	} else {
		d.logf("job %q: starting", e.name)
	}

	// The run gets its own context so it can be cancelled on request while the
	// daemon keeps running. It is derived from the daemon's context so a shutdown
	// still stops it.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Reset before publishing the run: a second run would otherwise begin with
	// the previous run's totals and the UI would show it as already complete.
	//
	// cfg and tracker are read under the lock because reload() swaps cfg; reading
	// the pointer while it is being replaced is a data race.
	d.mu.Lock()
	cfg := e.cfg
	tracker := e.tracker
	e.cancelRun = cancel
	e.mode = mode
	e.lastStopped = false
	e.phase = job.Phase{}
	e.hasPhase = false
	d.mu.Unlock()

	tracker.Reset()

	// The phase callback writes under d.mu: it arrives from the run goroutine while
	// /api/status reads on an HTTP handler. It is called a handful of times per
	// pair, not per file, except for the content comparison, which reports once per
	// compared file - still cheap next to the file read it accompanies.
	onPhase := func(p job.Phase) {
		d.mu.Lock()
		e.phase = p
		e.hasPhase = true
		d.mu.Unlock()
	}

	results, err := d.runJob(runCtx, cfg, d.log, job.Options{
		DryRun:     mode == ModePlan,
		StatsGroup: tracker.Group(),
		// A preview collects the per-path plan; a real run has no dry-run log to
		// read, and the console shows the plan only for previews.
		CollectPlan: mode == ModePlan,
		// only is the console's row selection. A scheduled run never sets it.
		OnlyPaths: only,
		Exclude:   d.SettingsOf().excludePatterns(),
		OnPhase:   onPhase,
	})

	// A cancelled context is what our own stop request looks like. Reporting it as
	// a failure would send the user hunting for a problem that is not there.
	stopped := errors.Is(runCtx.Err(), context.Canceled)
	if stopped {
		err = nil
	}

	d.mu.Lock()
	finishedCount := len(results)
	d.mu.Unlock()

	var conflicts int
	var transferred, deleted int64
	guardHit := false
	for _, r := range results {
		conflicts += len(r.Conflicts)
		transferred += r.Actual.Transferred
		deleted += r.Actual.Deleted
		if r.GuardHit {
			guardHit = true
		}
	}

	d.mu.Lock()
	e.running = false
	e.cancelRun = nil
	// A preview is not a run: it must not move "last run" or claim success, or the
	// job row would say a sync happened when nothing was touched.
	if mode == ModePlan {
		errText := ""
		if err != nil {
			errText = err.Error()
		}
		e.lastPlan = &PlanStatus{
			At: start, Results: results, GuardHit: guardHit, Error: errText,
			Conflicts: conflicts, Transferred: transferred, Deleted: deleted,
		}
		if r, ok := remedy.Of(err); ok {
			e.lastPlan.Remedy = &r
		}
	} else {
		e.lastRun = start
		e.hasRun = true
		e.lastOK = err == nil
		e.lastRes = results
		e.lastStopped = stopped
		if err != nil {
			e.lastError = err.Error()
			// A run joins one error per pair, so the error worth explaining is
			// usually not the outermost one - remedy.Of walks the whole chain.
			if r, ok := remedy.Of(err); ok {
				e.lastRemedy = &r
			} else {
				e.lastRemedy = nil
			}
		} else {
			e.lastError = ""
			e.lastRemedy = nil
		}
	}
	end := d.now()
	d.mu.Unlock()

	rec := RunRecord{
		Job: e.name, Started: start, Ended: end, OK: err == nil,
		Results: results, Conflicts: conflicts,
		Transferred: transferred, Deleted: deleted, GuardHit: guardHit,
		DryRun: mode == ModePlan, Stopped: stopped,
	}
	if err != nil {
		rec.Error = err.Error()
	}
	if d.history != nil {
		d.history.add(rec)
	}

	switch {
	case stopped:
		d.logf("job %q: stopped on request after %v",
			e.name, d.now().Sub(start).Round(time.Millisecond))
	case err != nil:
		d.logf("job %q: failed after %v: %v", e.name, d.now().Sub(start).Round(time.Millisecond), err)
	case mode == ModePlan:
		d.logf("job %q: preview done in %v (%d pair result(s), %d conflict(s))",
			e.name, d.now().Sub(start).Round(time.Millisecond), finishedCount, conflicts)
	default:
		d.logf("job %q: finished in %v (%d pair result(s), %d conflict(s))",
			e.name, d.now().Sub(start).Round(time.Millisecond), finishedCount, conflicts)
	}

	// A scheduled run happens while nobody is watching the console, so the
	// outcomes that need a human have to come to them. A success does not: a
	// notification per run would train the user to dismiss all of them.
	//
	// Previews are excluded too - the user just clicked, so they are looking.
	if mode != ModeRun || stopped {
		return
	}
	switch {
	case err != nil:
		d.notifyf("weftsync："+e.name+" 同步失败", "%v", err)
	case guardHit:
		d.notifyf("weftsync："+e.name+" 已拦下", "删除量超过上限，本次什么都没有删。打开控制台查看详情")
	case conflicts > 0:
		d.notifyf("weftsync："+e.name+" 有冲突", "%d 个文件在云端被改动，本次未处理，打开控制台查看", conflicts)
	}
}

// statsGroupFor names a job's accounting group.
//
// The prefix keeps our groups distinguishable from anything else that might use
// rclone's group feature in the same process.
func statsGroupFor(jobName string) string {
	return "weftsync:" + jobName
}

// Stop ends the scheduler and waits for running jobs to return.
func (d *Daemon) Stop() {
	d.stopOnce.Do(func() {
		close(d.stop)
		if d.cancel != nil {
			d.cancel()
		}
	})
	d.wg.Wait()
}

// Trigger starts a job on demand, reporting why it could not.
//
// The run uses the daemon's lifetime context, not the caller's: an API request
// that returns immediately must not take the transfer down with it.
func (d *Daemon) Trigger(_ context.Context, name string) error {
	return d.start(name, ModeRun, nil)
}

// TriggerPaths starts a run restricted to the listed paths.
//
// The console sends the rows the user left ticked, which is what makes the
// comparison editable instead of merely readable. Empty means "everything".
func (d *Daemon) TriggerPaths(_ context.Context, name string, paths []string) error {
	return d.start(name, ModeRun, paths)
}

// TriggerPlan builds a preview: it measures what a run would change and changes
// nothing.
//
// It shares the running flag with a real run rather than running alongside one.
// Two passes over the same pair would each see the other's writes and the guard
// count would be meaningless, so "one thing at a time per job" is the useful rule
// even for a read-only pass.
func (d *Daemon) TriggerPlan(_ context.Context, name string) error {
	return d.start(name, ModePlan, nil)
}

func (d *Daemon) start(name string, mode RunMode, only []string) error {
	d.mu.Lock()
	var target *entry
	for _, e := range d.entries {
		if e.name == name {
			target = e
			break
		}
	}
	if target == nil {
		d.mu.Unlock()
		return fmt.Errorf("daemon: no job named %q", name)
	}
	if !target.enabled {
		d.mu.Unlock()
		return fmt.Errorf("daemon: job %q is disabled", name)
	}
	if target.running {
		d.mu.Unlock()
		return fmt.Errorf("daemon: job %q is already running", name)
	}
	target.running = true
	target.mode = mode
	runCtx := d.ctx
	if runCtx == nil {
		runCtx = context.Background()
	}
	d.mu.Unlock()

	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		d.execute(runCtx, target, mode, only)
	}()
	return nil
}

// StopJob cancels the run in flight for one job.
//
// It returns ErrNotRunning when there is nothing to stop, so the caller can answer
// 409 instead of pretending something happened.
func (d *Daemon) StopJob(name string) error {
	d.mu.Lock()
	var target *entry
	for _, e := range d.entries {
		if e.name == name {
			target = e
			break
		}
	}
	if target == nil {
		d.mu.Unlock()
		return fmt.Errorf("daemon: no job named %q", name)
	}
	if !target.running || target.cancelRun == nil {
		d.mu.Unlock()
		return ErrNotRunning
	}
	cancel := target.cancelRun
	d.mu.Unlock()

	// The run unwinds on its own; waiting here would block the HTTP handler for
	// as long as the current transfer takes to notice.
	cancel()
	d.logf("job %q: stop requested", name)
	return nil
}

// ErrNotRunning means a stop was requested for a job that is not running.
var ErrNotRunning = errors.New("daemon: the job is not running")

// ErrNoSuchJob is returned when a name does not match any loaded job.
var ErrNoSuchJob = errors.New("daemon: no such job")

// ErrJobRunning means the request needs the job to be idle.
var ErrJobRunning = errors.New("daemon: the job is running; wait for it to finish")

// ConfigOf returns a copy of a job's configuration for one-off operations.
//
// A copy, not the live pointer: callers act on it outside the lock (opening
// remotes, editing state), and handing out the pointer would race with reload().
func (d *Daemon) ConfigOf(name string) (*job.Config, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	for _, e := range d.entries {
		if e.name != name {
			continue
		}
		if e.running {
			return nil, ErrJobRunning
		}
		cfg := *e.cfg
		cfg.Pair = append([]job.Pair(nil), e.cfg.Pair...)
		return &cfg, nil
	}
	return nil, ErrNoSuchJob
}

// Status returns a snapshot of every job.
func (d *Daemon) Status() []JobStatus {
	d.mu.Lock()
	defer d.mu.Unlock()

	out := make([]JobStatus, 0, len(d.entries))
	for _, e := range d.entries {
		st := JobStatus{
			Name:       e.name,
			File:       e.file,
			Enabled:    e.enabled,
			Pairs:      e.pairs,
			PairNames:  e.pairNames,
			Schedule:   e.schedule.String(),
			Skipped:    e.skipped,
			LastResult: e.lastRes,
		}
		switch {
		case !e.enabled:
			st.State = StateDisabled
		case e.running:
			st.State = StateRunning
		default:
			st.State = StateIdle
		}
		if !e.nextRun.IsZero() {
			next := e.nextRun
			st.NextRun = &next
		}
		if e.hasRun {
			last := e.lastRun
			st.LastRun = &last
			ok := e.lastOK
			st.LastOK = &ok
			st.LastStopped = e.lastStopped
		}
		st.LastError = e.lastError
		if e.lastRemedy != nil {
			r := *e.lastRemedy
			st.LastRemedy = &r
		}
		if e.lastPlan != nil {
			p := *e.lastPlan
			st.Plan = &p
		}

		// Progress is read while holding no lock of ours: the tracker has its own,
		// and holding d.mu here would stall every other API call for the duration
		// of a stats read.
		if e.running && e.tracker != nil {
			p := e.tracker.Snapshot()
			if e.hasPhase {
				phase := e.phase
				p.Phase = &phase
			}
			st.Progress = &p
			st.Mode = e.mode
			st.CanStop = e.cancelRun != nil
		}
		out = append(out, st)
	}
	return out
}

// logf writes one line to the daemon log.
func (d *Daemon) logf(format string, args ...any) {
	if d.log == nil {
		return
	}
	fmt.Fprintf(d.log, "%s %s\n", d.now().Format("2006-01-02 15:04:05"), strings.TrimRight(fmt.Sprintf(format, args...), "\n"))
}

// lastLogSeq is the newest sequence number the log buffer holds.
//
// The UI needs it to advance its "only give me what is new" cursor even when a
// filter hid the lines in between.
func (d *Daemon) lastLogSeq() int64 {
	if d.logs == nil {
		return 0
	}
	return d.logs.Seq()
}
