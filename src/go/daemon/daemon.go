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

// JobStatus is the observable state of one job.
type JobStatus struct {
	Name      string     `json:"name"`
	File      string     `json:"file"`
	State     RunState   `json:"state"`
	Enabled   bool       `json:"enabled"`
	Pairs     int        `json:"pairs"`
	Schedule  string     `json:"schedule"`
	NextRun   *time.Time `json:"next_run,omitempty"`
	LastRun   *time.Time `json:"last_run,omitempty"`
	LastOK    *bool      `json:"last_ok,omitempty"`
	LastError string     `json:"last_error,omitempty"`
	// Skipped counts runs that were not started because the previous one was
	// still going.
	Skipped int `json:"skipped"`
	// LastResult summarises the previous run.
	LastResult []job.PairResult `json:"last_result,omitempty"`
}

// entry is one scheduled job file.
type entry struct {
	file     string
	cfg      *job.Config
	schedule job.ResolvedSchedule

	// running guards against overlapping runs of the same job.
	running bool

	name      string
	enabled   bool
	pairs     int
	nextRun   time.Time
	lastRun   time.Time
	lastOK    bool
	hasRun    bool
	lastError string
	skipped   int
	lastRes   []job.PairResult
}

// Daemon owns the jobs and their schedule.
type Daemon struct {
	mu      sync.Mutex
	entries []*entry
	log     io.Writer
	now     func() time.Time

	// runJob is injectable so tests can avoid touching the network.
	runJob func(ctx context.Context, cfg *job.Config, w io.Writer) ([]job.PairResult, error)

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
}

// New builds a daemon for the given job files.
func New(files []string, log io.Writer) (*Daemon, error) {
	if len(files) == 0 {
		return nil, errors.New("daemon: at least one job file is required")
	}
	d := &Daemon{
		log:  log,
		now:  time.Now,
		stop: make(chan struct{}),
		runJob: func(ctx context.Context, cfg *job.Config, w io.Writer) ([]job.PairResult, error) {
			return job.Run(ctx, cfg, w, job.Options{})
		},
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
			file:     f,
			cfg:      cfg,
			schedule: sched,
			name:     name,
			enabled:  cfg.Enabled(),
			pairs:    len(cfg.Pair),
			nextRun:  d.computeNext(sched, d.now()),
		})
	}
	return d, nil
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
			d.execute(ctx, e)
		}(e)
	}
}

// execute runs one job and records the outcome.
func (d *Daemon) execute(ctx context.Context, e *entry) {
	start := d.now()
	d.logf("job %q: starting", e.name)

	results, err := d.runJob(ctx, e.cfg, d.log)

	d.mu.Lock()
	finishedCount := len(results)
	d.mu.Unlock()

	var conflicts int
	for _, r := range results {
		conflicts += len(r.Conflicts)
	}

	d.mu.Lock()
	e.running = false
	e.lastRun = start
	e.hasRun = true
	e.lastOK = err == nil
	e.lastRes = results
	if err != nil {
		e.lastError = err.Error()
	} else {
		e.lastError = ""
	}
	d.mu.Unlock()

	switch {
	case err != nil:
		d.logf("job %q: failed after %v: %v", e.name, d.now().Sub(start).Round(time.Millisecond), err)
	default:
		d.logf("job %q: finished in %v (%d pair result(s), %d conflict(s))",
			e.name, d.now().Sub(start).Round(time.Millisecond), finishedCount, conflicts)
	}
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
	runCtx := d.ctx
	if runCtx == nil {
		runCtx = context.Background()
	}
	d.mu.Unlock()

	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		d.execute(runCtx, target)
	}()
	return nil
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
		}
		st.LastError = e.lastError
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
