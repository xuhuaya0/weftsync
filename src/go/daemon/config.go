package daemon

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"weftsync/job"
)

// Editing job files from the UI.
//
// Two rules shape this:
//
//  1. The daemon's in-memory copy must not diverge from the file. A save reloads
//     the job immediately, so the next scheduled run uses what the user just saw
//     rather than what was on disk at startup.
//  2. A save must be impossible to perform half-way. job.Save validates with the
//     same rules as Load, writes atomically and keeps a .bak; this layer only
//     decides permissions and shapes the JSON.

// ConfigView is the editable shape sent to the browser.
//
// It mirrors job.Config but adds the details the UI needs to render sensible
// controls: which values are defaults versus explicit, and whether saving would
// discard comments.
type ConfigView struct {
	Name string `json:"name"`
	File string `json:"file"`
	// Enabled is always present (job.Enabled is a *bool).
	Enabled bool   `json:"enabled"`
	Variant string `json:"variant"`
	// Variants lists the values the UI may offer.
	Variants []string `json:"variants"`

	Schedule ScheduleView `json:"schedule"`
	Sync     SyncView     `json:"sync"`
	Pairs    []PairView   `json:"pairs"`

	// HasComments means a save would drop them; the UI must ask before saving.
	HasComments bool `json:"has_comments"`
	// Backups lists existing .bak files so the UI can tell the user where the
	// previous version went.
	BackupExists bool `json:"backup_exists"`
}

// ScheduleView is the [job.schedule] table.
type ScheduleView struct {
	Mode         string   `json:"mode"`
	Interval     string   `json:"interval"`
	StartAt      string   `json:"start_at"`
	AlignToStart bool     `json:"align_to_start"`
	DailyTime    string   `json:"daily_time"`
	Cron         string   `json:"cron"`
	Modes        []string `json:"modes"`
}

// SyncView is a [sync] table, showing inherited and explicit values separately.
type SyncView struct {
	Deletion         string `json:"deletion"`
	VersioningDir    string `json:"versioning_dir"`
	VersioningNaming string `json:"versioning_naming"`
	Conflict         string `json:"conflict"`
	MaxDelete        int64  `json:"max_delete"`
	// MaxDeleteSet is false when the value shown is the inherited default.
	MaxDeleteSet bool `json:"max_delete_set"`
	// MaxDeleteSize is the byte ceiling as written, e.g. "1G". Empty means none.
	MaxDeleteSize string `json:"max_delete_size"`
	// MaxDeleteSizeHelp is the one-line explanation the form shows.
	MaxDeleteSizeHelp string `json:"max_delete_size_help"`

	Deletions []string `json:"deletions"`
	Namings   []string `json:"namings"`
	Conflicts []string `json:"conflicts"`
}

// PairView is one [[pair]] table.
type PairView struct {
	Name    string `json:"name"`
	Variant string `json:"variant"`
	// EffectiveVariant is what will actually run (pair value or job default).
	EffectiveVariant string `json:"effective_variant"`

	LocalRoot     string `json:"local_root"`
	RemoteBackend string `json:"remote_backend"`
	RemoteRoot    string `json:"remote_root"`
	StateFile     string `json:"state_file"`

	Sync SyncView `json:"sync"`
}

func syncView(spec job.SyncSpec, resolved job.ResolvedSync) SyncView {
	v := SyncView{
		Deletion:         firstNonEmpty(spec.Deletion, resolved.Deletion),
		VersioningDir:    firstNonEmpty(spec.VersioningDir, resolved.VersioningDir),
		VersioningNaming: firstNonEmpty(spec.VersioningNaming, resolved.VersioningNaming),
		Conflict:         firstNonEmpty(spec.Conflict, resolved.Conflict),
		MaxDelete:        resolved.MaxDelete,
		MaxDeleteSet:     spec.MaxDelete != nil,
		MaxDeleteSize:    resolved.MaxDeleteSizeText,
		MaxDeleteSizeHelp: "单个 run 删除的总字节上限，例如 500M / 1G；留空=不限。" +
			"按体积拦下时不删任何文件",
		Deletions: []string{job.DeletionPermanent, job.DeletionVersioning},
		Namings: []string{
			job.NamingReplace, job.NamingTimestampFile, job.NamingTimestampDir,
		},
		Conflicts: []string{
			job.ConflictReport, job.ConflictOverwrite, job.ConflictKeepBoth,
		},
	}
	return v
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// jobDefaults resolves the job-level sync defaults with no pair overrides.
//
// EffectiveSync lives on Pair because a pair is what actually runs, so asking
// pair[0] for "the job defaults" returns pair[0]'s overrides whenever it has
// any. That made the "defaults" section of the form show one pair's override
// as if it were the default, and made a save compare against the wrong value.
// A pair with no overrides of its own resolves to exactly the job defaults.
func jobDefaults(cfg *job.Config) job.ResolvedSync {
	resolved, _ := (&job.Pair{Name: "(job)"}).EffectiveSync(cfg)
	return resolved
}

// ViewConfig converts a loaded config into the editable shape.
func ViewConfig(e *entry, raw []byte) ConfigView {
	cfg := e.cfg
	enabled := true
	if cfg.Job.Enabled != nil {
		enabled = *cfg.Job.Enabled
	}

	jobResolved := jobDefaults(cfg)

	v := ConfigView{
		Name:     e.name,
		File:     e.file,
		Enabled:  enabled,
		Variant:  firstNonEmpty(cfg.Job.Variant, job.DefaultVariant),
		Variants: []string{job.VariantUpdate, job.VariantMirror},
		Schedule: ScheduleView{
			Mode:         cfg.Job.Schedule.Mode,
			Interval:     cfg.Job.Schedule.Interval,
			StartAt:      cfg.Job.Schedule.StartAt,
			AlignToStart: cfg.Job.Schedule.AlignToStart,
			DailyTime:    cfg.Job.Schedule.DailyTime,
			Cron:         cfg.Job.Schedule.Cron,
			Modes: []string{
				job.ScheduleNone, job.ScheduleInterval,
				job.ScheduleDailyTime, job.ScheduleCron,
			},
		},
		Sync:        syncView(cfg.Job.Sync, jobResolved),
		HasComments: job.HasComments(raw),
	}

	if _, err := os.Stat(e.file + ".bak"); err == nil {
		v.BackupExists = true
	}

	for i := range cfg.Pair {
		p := &cfg.Pair[i]
		resolved, _ := p.EffectiveSync(cfg)
		effective, _ := p.EffectiveVariant(cfg)
		v.Pairs = append(v.Pairs, PairView{
			Name:             p.Name,
			Variant:          p.Variant,
			EffectiveVariant: effective,
			LocalRoot:        p.Local.Root,
			RemoteBackend:    p.Remote.Backend,
			RemoteRoot:       p.Remote.Root,
			StateFile:        p.Remote.Options["state_file"],
			Sync:             syncView(p.Sync, resolved),
		})
	}
	return v
}

// ApplyConfigView writes a submitted view back into the job file.
//
// The incoming value is turned into a fresh Config and validated by job.Save, so
// a malformed submission cannot produce a malformed file. Fields absent from the
// view keep their previous value: the UI may not render everything.
// mergeSync applies a submitted [sync] table on top of the previous one.
//
// The form always renders effective values, so "the user left this alone" cannot
// be detected by looking for empty input: an untouched pair and one explicitly
// set to the job default look identical. Comparing against what the form was
// shown is what keeps an untouched field exactly as it was — explicit stays
// explicit, inherited stays inherited — while still letting the user change
// anything.
//
// out starts as prev so fields this form does not expose keep their value
// instead of being erased by a save that knows nothing about them.
func mergeSync(prev job.SyncSpec, shown, in SyncView) job.SyncSpec {
	out := prev
	if in.Deletion != shown.Deletion {
		out.Deletion = in.Deletion
	}
	if in.VersioningDir != shown.VersioningDir {
		out.VersioningDir = in.VersioningDir
	}
	if in.VersioningNaming != shown.VersioningNaming {
		out.VersioningNaming = in.VersioningNaming
	}
	if in.Conflict != shown.Conflict {
		out.Conflict = in.Conflict
	}
	if in.MaxDeleteSet {
		v := in.MaxDelete
		out.MaxDelete = &v
	} else {
		// An empty box means "inherit the default"; it must be able to clear a
		// previously explicit limit.
		out.MaxDelete = nil
	}
	if in.MaxDeleteSize != shown.MaxDeleteSize {
		out.MaxDeleteSize = strings.TrimSpace(in.MaxDeleteSize)
	}
	return out
}

// ApplyConfigView writes a submitted view back into the job file.
//
// The submitted value is merged onto the config already loaded instead of
// replacing it, then handed to job.Save, which validates with the same rules as
// Load and writes atomically. Starting from the previous config matters: the
// form does not cover every field, and a save must not silently erase settings
// it cannot show.
func (d *Daemon) ApplyConfigView(name string, in ConfigView) (job.SaveResult, error) {
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
		return job.SaveResult{}, errors.New("no such job")
	}
	if target.running {
		d.mu.Unlock()
		return job.SaveResult{}, errors.New(
			"the job is running; stop it before editing, otherwise the change would " +
				"apply to a run that already started")
	}
	file := target.file
	previous := target.cfg
	d.mu.Unlock()

	// The pairs of the previous config give us both the values the form was
	// shown and a place to keep everything the form cannot represent.
	prevJobSync := syncView(previous.Job.Sync, jobDefaults(previous))

	cfg := &job.Config{
		Job: job.Job{
			Name:    name,
			Variant: in.Variant,
			Sync:    mergeSync(previous.Job.Sync, prevJobSync, in.Sync),
			Schedule: job.ScheduleSpec{
				Mode:         in.Schedule.Mode,
				Interval:     in.Schedule.Interval,
				StartAt:      in.Schedule.StartAt,
				AlignToStart: in.Schedule.AlignToStart,
				DailyTime:    in.Schedule.DailyTime,
				Cron:         in.Schedule.Cron,
			},
		},
	}
	// enabled: keep it explicit only when turning the job OFF. Writing
	// "enabled = true" adds noise to a file that defaults to enabled.
	if !in.Enabled {
		off := false
		cfg.Job.Enabled = &off
	}

	for i, pv := range in.Pairs {
		sync := job.SyncSpec{}
		if i < len(previous.Pair) {
			prevResolved, _ := previous.Pair[i].EffectiveSync(previous)
			sync = mergeSync(previous.Pair[i].Sync, syncView(previous.Pair[i].Sync, prevResolved), pv.Sync)
		} else {
			sync = mergeSync(job.SyncSpec{}, SyncView{}, pv.Sync)
		}

		p := job.Pair{
			Name:    pv.Name,
			Variant: pv.Variant,
			Local:   job.Endpoint{Root: pv.LocalRoot},
			Remote: job.Endpoint{
				Backend: pv.RemoteBackend,
				Root:    pv.RemoteRoot,
			},
			Sync: sync,
		}
		if pv.StateFile != "" {
			p.Remote.Options = map[string]string{"state_file": pv.StateFile}
		}
		cfg.Pair = append(cfg.Pair, p)
	}
	if len(cfg.Pair) == 0 {
		// The UI sent nothing usable; keep the pairs we already had rather than
		// deleting them by accident.
		cfg.Pair = previous.Pair
	}

	res, err := job.Save(cfg, file, job.SaveOptions{Force: true})
	if err != nil {
		return res, err
	}

	// Reload so the next run uses what the user just saved.
	if err := d.reload(name); err != nil {
		return res, err
	}
	return res, nil
}

// reload re-reads one job file into its entry.
func (d *Daemon) reload(name string) error {
	d.mu.Lock()
	var e *entry
	for _, cand := range d.entries {
		if cand.name == name {
			e = cand
			break
		}
	}
	if e == nil {
		d.mu.Unlock()
		return errors.New("no such job")
	}
	file := e.file
	d.mu.Unlock()

	cfg, err := job.Load(file)
	if err != nil {
		return err
	}
	sched, err := cfg.EffectiveSchedule()
	if err != nil {
		return err
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	e.cfg = cfg
	e.schedule = sched
	e.enabled = cfg.Enabled()
	e.pairs = len(cfg.Pair)
	e.nextRun = d.computeNext(sched, d.now())
	return nil
}

// handleConfig serves the editable view and accepts changes.
func (a *API) handleConfig(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("job")
	if name == "" {
		writeError(w, http.StatusBadRequest, "the job query parameter is required")
		return
	}

	switch r.Method {
	case http.MethodGet:
		a.D.mu.Lock()
		var e *entry
		for _, cand := range a.D.entries {
			if cand.name == name {
				e = cand
				break
			}
		}
		a.D.mu.Unlock()
		if e == nil {
			writeError(w, http.StatusNotFound, "no such job: "+name)
			return
		}

		raw, err := os.ReadFile(e.file)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "read job file: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, ViewConfig(e, raw))

	case http.MethodPost, http.MethodPut:
		var in ConfigView
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		if err := dec.Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}

		res, err := a.D.ApplyConfigView(name, in)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"saved":       true,
			"bytes":       res.Bytes,
			"backup_path": res.BackupPath,
			"lossy":       res.Lossy,
			"saved_at":    time.Now().Format(time.RFC3339),
		})

	default:
		writeError(w, http.StatusMethodNotAllowed, "GET or POST only")
	}
}
