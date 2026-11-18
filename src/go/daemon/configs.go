package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

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
	//
	// Short and path-free on purpose: the row already shows the path, and the
	// dialog used to slice this at 40 characters, which cut the path at the front
	// and left the actual reason past the edge.
	Error string `json:"error,omitempty"`
}

// listConfigFiles returns the weftsync configs in dir, with their job names.
//
// A file that is a config but does not load is still listed, with the reason
// attached: hiding it would leave the user staring at a directory that "has no
// configs" while the file sits right there.
//
// A .toml file that is not a config at all (pyproject.toml, Cargo.toml) is not
// listed. It did not fail to open - it was never openable - and a greyed-out row
// carrying a Go error only teaches the user that the dialog is broken. The count
// of skipped files is returned so the dialog can say so instead of hiding them
// without a word.
func listConfigFiles(dir, loadedDir string) (files []ConfigFile, other int, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0, err
	}
	files = make([]ConfigFile, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".toml") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if raw, readErr := os.ReadFile(path); readErr == nil && !looksLikeConfig(raw) {
			other++
			continue
		}
		cf := ConfigFile{Path: path, File: e.Name()}
		cfg, loadErr := job.Load(path)
		if loadErr != nil {
			cf.Error = shortLoadError(path, loadErr)
		} else {
			cf.Name = cfg.Job.Name
			if cf.Name == "" {
				cf.Name = strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
			}
			cf.Loaded = samePath(filepath.Dir(path), loadedDir)
		}
		files = append(files, cf)
	}
	sort.Slice(files, func(i, j int) bool {
		return strings.ToLower(files[i].File) < strings.ToLower(files[j].File)
	})
	return files, other, nil
}

// looksLikeConfig reports whether a .toml file is a weftsync job file at all.
//
// The test is "has one of our two top-level keys", and it has to run on an
// untyped map: the real decoder is strict (DisallowUnknownFields), so on somebody
// else's file it fails before anything can be inspected. That is exactly how a
// pyproject.toml ended up in the picker labelled with a decoder error instead of
// being recognised as not ours.
//
// A file that will not parse at all counts as a config: "line 3 is not valid
// TOML" is a reason worth showing when the user was editing one.
func looksLikeConfig(raw []byte) bool {
	var doc map[string]any
	if err := toml.Unmarshal(raw, &doc); err != nil {
		return true
	}
	if _, ok := doc["job"]; ok {
		return true
	}
	_, ok := doc["pair"]
	return ok
}

// shortLoadError turns a load error into one row-sized sentence.
//
// Two things come out. The path, because the row already shows it. And the toml
// library's "strict mode: fields in the document are missing in the target
// struct", which describes the decoder rather than the file - what the user needs
// to know is that they typed a key weftsync does not have.
func shortLoadError(path string, err error) string {
	msg := err.Error()
	msg = strings.TrimPrefix(msg, "parse "+path+": ")
	msg = strings.TrimPrefix(msg, path+": ")
	msg = strings.TrimPrefix(msg, "read job file: ")
	if strings.Contains(msg, "fields in the document are missing in the target struct") {
		return "有 weftsync 不认识的键（拼错了，或来自别的版本）"
	}
	return msg
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

// RemoveConfig unloads the config that came from path and returns its name.
//
// Unloading and deleting are separate on purpose. Removing an entry is what "close
// this configuration" means and is always safe; deleting the file is a decision
// the user has to make explicitly, so it stays in the handler.
//
// A running job is refused rather than waited for: the run holds the entry and its
// tracker, and pulling that out from under it would lose the result of a transfer
// that is already moving bytes.
func (d *Daemon) RemoveConfig(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	for i, e := range d.entries {
		if !samePath(e.file, abs) {
			continue
		}
		if e.running {
			return "", fmt.Errorf("%s is running; stop it before removing it",
				filepath.Base(abs))
		}
		name := e.name
		d.entries = append(d.entries[:i], d.entries[i+1:]...)
		// The split lock is keyed by name, so it would outlive the config and
		// make a re-opened config of the same name look like it had a split in
		// flight.
		delete(d.splitting, name)
		return name, nil
	}
	return "", fmt.Errorf("%s is not open in this daemon", filepath.Base(abs))
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
	// Nothing loaded yet, so there is no "next to the config you have open" to
	// fall back on. Returning "" left the 打开 and 另存为 dialogs with an empty
	// directory and no rows to click - a dead end on a fresh daemon, which is
	// exactly when the first configuration has to be created.
	//
	// The working directory is the honest answer: it is where the user started the
	// daemon, and where a first job.toml most naturally goes.
	if wd, err := os.Getwd(); err == nil {
		return wd
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
	files, other, listErr := listConfigFiles(abs, loadedDir)
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
		// Other counts the .toml files here that are not weftsync configs.
		// Reported instead of silently dropped: someone whose own config sits in
		// this folder and does not appear needs to know why.
		Other int        `json:"other"`
		View  BrowseView `json:"view"`
	}{Dir: abs, Files: files, Other: other, View: view})
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
		writeError(w, http.StatusConflict, alreadyExistsText(abs))
		return
	}
	res, err := job.Save(cfg, abs, job.SaveOptions{Force: true})
	if err != nil {
		writeError(w, http.StatusBadRequest, saveErrText(err))
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

// saveErrText strips the parts of a Save error that only repeat what the caller
// already knows.
//
// job.Save reports a rejected config as
//
//	the edited config is not valid: <absolute path>: <reason>
//
// Three layers, and two of them are noise in a dialog: the client chose the path
// (it is on screen next to the box), and "the edited config is not valid" says
// nothing that the heading 保存失败 has not already said. What is left is the
// reason, which the console translates into one sentence naming the field.
//
// Measured 2026-11-18, the user's screenshot of the resulting dialog:
//
//	保存失败：the edited config is not valid: C:\Users\…\dist\te.toml:
//	          pair "pair-1": local.root is required
func saveErrText(err error) string {
	const wrapper = "the edited config is not valid: "
	msg := err.Error()
	if !strings.HasPrefix(msg, wrapper) {
		return msg
	}
	msg = strings.TrimPrefix(msg, wrapper)
	// The reason always follows "<file>.toml: ". Cutting there rather than
	// trimming a path this function was never given keeps it usable from callers
	// that only hold the error (ApplyConfigView works from a job name).
	if i := strings.Index(msg, ".toml: "); i >= 0 {
		return msg[i+len(".toml: "):]
	}
	return msg
}

// alreadyExistsText says a file is in the way without explaining the policy.
//
// The old wording carried "(its previous content would be overwritten)", which
// describes what the code refuses to do rather than what the user has to change.
func alreadyExistsText(path string) string {
	return filepath.Base(path) + " 已经存在，换一个名字。"
}

// handleConfigRemove unloads a config, and optionally deletes its file.
//
// POST /api/config/remove?path=<file>&delete_file=true
//
// Two operations behind one endpoint because they answer the same user action
// ("delete this configuration") and the difference is a checkbox, not a workflow.
// The order is deliberate: unload first, delete second. A failed unlink then
// leaves a file that is simply no longer open, which the user can re-open; the
// reverse order could delete a file whose entry is still scheduled.
//
// delete_file defaults to false. Unloading a config is reversible, deleting the
// file is not, and a default that destroys a file on a stray request is not one
// worth having.
func (a *API) handleConfigRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		writeError(w, http.StatusBadRequest, "the path query parameter is required")
		return
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	name, err := a.D.RemoveConfig(abs)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	out := map[string]any{"name": name, "path": abs, "deleted": false}
	if r.URL.Query().Get("delete_file") == "true" {
		if rmErr := os.Remove(abs); rmErr != nil && !os.IsNotExist(rmErr) {
			// The config is already unloaded, so this is not a failure of the
			// whole operation - it is "it is gone from the list but the file is
			// still there", which the caller has to be able to say.
			out["error"] = "removed from the list, but the file could not be deleted: " +
				rmErr.Error()
			writeJSON(w, http.StatusOK, out)
			return
		}
		out["deleted"] = true
	}
	writeJSON(w, http.StatusOK, out)
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
