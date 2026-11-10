package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"weftsync/job"
)

// Configuration files as documents.
//
// FreeFileSync's main window edits one configuration and its File menu offers
// New / Open / Save / Save As. This daemon used to know only the files it was
// started with, so the console could overwrite the file it came from and nothing
// else: there was no way to keep a set of settings under a new name, and no way
// to open one that was not passed on the command line.
//
// The three operations here fill that gap. They are deliberately about files on
// disk, not about an abstract "profile" store: a configuration is a .toml file
// the user can copy, edit and put in version control, exactly like a .ffs_gui.

// ConfigFile is one entry of the configuration list.
type ConfigFile struct {
	// Name is the job name stored inside the file, which is what the rest of the
	// API addresses. Empty when the file could not be read.
	Name string `json:"name"`
	// Path is the absolute path.
	Path string `json:"path"`
	// File is the base name, for display.
	File string `json:"file"`
	// Loaded means this file is already in the daemon and can be selected.
	Loaded bool `json:"loaded"`
	// Error explains why the file cannot be opened, without failing the listing:
	// one broken file must not hide the rest.
	Error string `json:"error,omitempty"`
}

// listConfigFiles returns the .toml files in dir, with their job names.
//
// A file that does not parse is still listed, with the reason attached. Hiding it
// would leave the user staring at a directory that "has no configs" while the
// file sits right there.
func listConfigFiles(dir, loadedDir string) ([]ConfigFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]ConfigFile, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".toml") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		cf := ConfigFile{Path: path, File: e.Name()}
		cfg, loadErr := job.Load(path)
		if loadErr != nil {
			cf.Error = loadErr.Error()
		} else {
			cf.Name = cfg.Job.Name
			if cf.Name == "" {
				cf.Name = strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
			}
			cf.Loaded = samePath(filepath.Dir(path), loadedDir)
		}
		out = append(out, cf)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].File) < strings.ToLower(out[j].File)
	})
	return out, nil
}

// samePath compares two directories without caring about separators or case.
//
// Windows paths are case-insensitive and may carry either separator; a plain
// string comparison would say the same directory is two different ones, and the
// list would show every config as "not loaded".
func samePath(a, b string) bool {
	ca, errA := filepath.Abs(a)
	cb, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return a == b
	}
	return strings.EqualFold(filepath.Clean(ca), filepath.Clean(cb))
}

// AddConfig loads one config file into the daemon.
//
// Adding rather than replacing is what makes Open work: the daemon keeps every
// config it knows about, and the console selects which one it edits. Removing a
// loaded config is a separate concern and deliberately not offered here.
func (d *Daemon) AddConfig(path string) (string, error) {
	cfg, err := job.Load(path)
	if err != nil {
		return "", err
	}
	name := cfg.Job.Name
	if name == "" {
		return "", fmt.Errorf("%s: the job has no name, so the console cannot address it",
			filepath.Base(path))
	}
	sched, err := cfg.EffectiveSchedule()
	if err != nil {
		return "", err
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	for _, e := range d.entries {
		if e.name == name {
			if samePath(e.file, path) {
				return name, nil // already loaded, nothing to do
			}
			return "", fmt.Errorf(
				"%s is already the config named %q (from %s); rename one of them first",
				filepath.Base(path), name, filepath.Base(e.file))
		}
	}

	d.entries = append(d.entries, &entry{
		file:      path,
		cfg:       cfg,
		schedule:  sched,
		name:      name,
		enabled:   cfg.Enabled(),
		pairs:     len(cfg.Pair),
		pairNames: pairNamesOf(cfg),
		nextRun:   d.computeNext(sched, d.now()),
		tracker:   job.NewTracker(statsGroupFor(name)),
	})
	return name, nil
}

// configDirOf returns the directory holding a loaded config, which is where the
// file dialogs start and where a new config is written by default.
func (d *Daemon) configDirOf(name string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, e := range d.entries {
		if e.name == name {
			return filepath.Dir(e.file)
		}
	}
	if len(d.entries) > 0 {
		return filepath.Dir(d.entries[0].file)
	}
	return ""
}

// handleConfigFiles lists the config files in one directory.
//
// GET /api/config/files?dir=<dir>
//
// Without dir it starts from the directory of the config in ?job=, so opening a
// sibling config does not require typing a path first.
func (a *API) handleConfigFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	dir := strings.TrimSpace(r.URL.Query().Get("dir"))
	if dir == "" {
		dir = a.D.configDirOf(r.URL.Query().Get("job"))
	}
	view := BrowseView{Kind: BrowseLocal}
	if dir == "" {
		writeJSON(w, http.StatusOK, struct {
			Dir   string       `json:"dir"`
			Files []ConfigFile `json:"files"`
		}{})
		return
	}

	abs, err := filepath.Abs(dir)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	abs = filepath.Clean(abs)
	view.Path = abs
	if parent := filepath.Dir(abs); parent != abs {
		view.Parent = parent
	}
	view.Roots = browseRoots()

	loadedDir := a.D.configDirOf(r.URL.Query().Get("job"))
	files, listErr := listConfigFiles(abs, loadedDir)
	if listErr != nil {
		// The directory itself is unusable; still return the navigation so the
		// dialog can go elsewhere instead of dead-ending.
		view.Error = listErr.Error()
	}
	if dirs, dErr := listDirs(abs); dErr == nil {
		view.Dirs = dirs
	}

	writeJSON(w, http.StatusOK, struct {
		Dir   string       `json:"dir"`
		Files []ConfigFile `json:"files"`
		View  BrowseView   `json:"view"`
	}{Dir: abs, Files: files, View: view})
}

// handleConfigOpen loads a config file into the daemon.
//
// POST /api/config/open?path=<file>
//
// The response is the new config in the same shape /api/config returns, so the
// console can switch to it without a second round trip.
func (a *API) handleConfigOpen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		writeError(w, http.StatusBadRequest, "the path query parameter is required")
		return
	}
	name, err := a.D.AddConfig(path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.writeConfigView(w, name)
}

// handleConfigSaveAs writes the posted configuration to a new file and opens it.
//
// POST /api/config/save-as?path=<file>
//
// The body is the same ConfigView /api/config returns, so the console can save
// whatever is on screen - including a configuration that has no file yet, which
// is what makes "New" possible without inventing a half-valid file up front.
func (a *API) handleConfigSaveAs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		writeError(w, http.StatusBadRequest, "the path query parameter is required")
		return
	}
	if !strings.EqualFold(filepath.Ext(path), ".toml") {
		// Writing a config to a name with no extension makes it invisible to the
		// picker, which filters by .toml.
		path += ".toml"
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	var in ConfigView
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}

	// The config name follows the file, the way FreeFileSync's title bar shows the
	// file's name. Keeping the old name instead would collide with the original
	// as soon as both are loaded ("saved as copy.toml" would still call itself
	// "base"), so Save As to any fresh name would fail.
	name := strings.TrimSuffix(filepath.Base(abs), filepath.Ext(abs))
	in.Name = name

	// Reuse the live config as the "previous" state when it exists, so fields the
	// form does not show survive the round trip.
	src := a.D.configNamed(name)
	cfg := buildConfig(in, src, name)
	if _, err := os.Stat(abs); err == nil {
		writeError(w, http.StatusConflict,
			fmt.Sprintf("%s already exists; pick another name (its previous content "+
				"would be overwritten)", filepath.Base(abs)))
		return
	}
	res, err := job.Save(cfg, abs, job.SaveOptions{Force: true})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := a.D.AddConfig(abs); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name": name, "path": abs, "saved": res,
		"config": a.D.configView(name),
	})
}

// configNamed returns the loaded config with this name, or nil.
func (d *Daemon) configNamed(name string) *job.Config {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, e := range d.entries {
		if e.name == name {
			return e.cfg
		}
	}
	return nil
}

// configView is the editable view of one loaded config, or nil.
func (d *Daemon) configView(name string) *ConfigView {
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
		return nil
	}
	// The raw bytes are only used to detect comments, and reading a file while
	// holding the daemon lock would stall every other request for a disk read.
	file := e.file
	d.mu.Unlock()

	raw, _ := os.ReadFile(file)
	v := ViewConfig(e, raw)
	return &v
}

// writeConfigView answers with the editable view of one config.
func (a *API) writeConfigView(w http.ResponseWriter, name string) {
	v := a.D.configView(name)
	if v == nil {
		writeError(w, http.StatusNotFound, "no such config")
		return
	}
	writeJSON(w, http.StatusOK, v)
}
