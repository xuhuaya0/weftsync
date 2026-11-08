package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/rclone/rclone/fs/filter"
)

// Daemon-level settings, stored outside the job files.
//
// Why not in a job file: these apply to the daemon, not to one job. A job file
// describes what to sync; this describes how the program behaves. Putting a
// notification switch inside a job would mean each job has its own answer to a
// question about the tray icon.
//
// Why a separate file at all rather than flags: they are meant to be changed from
// the web console, and a flag can only be changed by editing an autostart entry
// or a shortcut, which is exactly the thing nobody remembers how to do.

// Settings is the persistent daemon configuration.
type Settings struct {
	// NotifyEnabled turns desktop notifications on or off. Default true: a
	// notification only fires for failures, guard hits and conflicts, so the
	// cautious default is to say something.
	NotifyEnabled *bool `json:"notify_enabled,omitempty"`

	// Exclude holds filter patterns applied to every pair, on top of whatever the
	// job file implies. They are additive: nothing here can re-include a file the
	// job's own filters excluded.
	//
	// Patterns are rclone filter syntax, so "*.tmp" works and ".git/**" works.
	Exclude []string `json:"exclude,omitempty"`

	// Note on "global defaults for new pairs": there is no field for it on
	// purpose. A new pair is created from the job's own [job.sync] defaults,
	// which is already the closest correct answer - and a second layer of
	// defaults would mean a pair's initial values depend on a file the user is not
	// looking at. "The job file says what runs" is worth more than one saved click.
}

// NotificationsOn reports whether desktop notifications should be raised.
func (s Settings) NotificationsOn() bool {
	return s.NotifyEnabled == nil || *s.NotifyEnabled
}

// settingsPath is where the settings file lives.
//
// A variable so tests can keep the real user profile untouched: a test that
// writes to %APPDATA% leaves state behind that changes the behaviour of the next
// test run.
var settingsPath = func() string {
	base, err := os.UserConfigDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "weftsync", "settings.json")
}

// LoadSettings reads the settings file, returning defaults when it is absent.
//
// A missing file is not an error: it is the state of a fresh install.
func LoadSettings() (Settings, error) {
	var s Settings
	raw, err := os.ReadFile(settingsPath())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return s, nil
		}
		return s, fmt.Errorf("read settings: %w", err)
	}
	if len(raw) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return s, fmt.Errorf("parse %s: %w", settingsPath(), err)
	}
	return s, nil
}

// Save writes the settings atomically, creating the directory if needed.
func (s Settings) Save() error {
	path := settingsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create settings directory: %w", err)
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	raw = append(raw, '\n')

	tmp := path + ".tmp"
	// 0600: the file can name paths, and on a shared machine it is nobody
	// else's business which folders are synchronised.
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("write settings: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace settings: %w", err)
	}
	return nil
}

// validateSettings rejects settings that would fail silently later.
//
// A filter pattern with a syntax error is the dangerous case: rclone would refuse
// the run, but only after the user has saved and walked away, and the message
// would arrive on the next scheduled run instead of at the moment of the typo.
func validateSettings(s Settings) error {
	for _, p := range s.Exclude {
		if strings.TrimSpace(p) == "" {
			continue
		}
		if err := checkFilterPattern(p); err != nil {
			return err
		}
	}
	return nil
}

// SettingsView is the API shape: the stored values plus what the UI needs to
// explain itself.
type SettingsView struct {
	Settings
	// NotificationsAvailable is false when no notifier is installed (no tray),
	// so the checkbox can be disabled with a reason instead of pretending.
	NotificationsAvailable bool `json:"notifications_available"`
	// SettingsFile is where this lives, shown so "where is that stored?" has an
	// answer on the page itself.
	SettingsFile string `json:"settings_file"`
}

// handleSettings reads or writes the daemon settings.
func (a *API) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s, err := LoadSettings()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, SettingsView{
			Settings:               s,
			NotificationsAvailable: a.D.HasNotifier(),
			SettingsFile:           settingsPath(),
		})

	case http.MethodPost:
		var in Settings
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if err := validateSettings(in); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := in.Save(); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		a.D.ApplySettings(in)
		writeJSON(w, http.StatusOK, SettingsView{
			Settings:               in,
			NotificationsAvailable: a.D.HasNotifier(),
			SettingsFile:           settingsPath(),
		})

	default:
		writeError(w, http.StatusMethodNotAllowed, "GET or POST")
	}
}

// excludePatterns returns the configured patterns, ignoring blanks.
func (s Settings) excludePatterns() []string {
	out := make([]string, 0, len(s.Exclude))
	for _, p := range s.Exclude {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// checkFilterPattern asks rclone whether a pattern is usable.
//
// Doing it here rather than letting the next run discover it means the message
// arrives while the user is still looking at the box they typed into.
func checkFilterPattern(pattern string) error {
	ctx, fi := filter.AddConfig(context.Background())
	if err := fi.Add(false, pattern); err != nil {
		return fmt.Errorf("过滤规则 %q 无法解析：%w", pattern, err)
	}
	_ = ctx
	return nil
}
