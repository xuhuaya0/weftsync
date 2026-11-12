package daemon

import (
	"net/http"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/rclone/rclone/fs/config"
)

// Runtime information for the About window (Help -> About weftsync).
//
// The point of this list is not to be pretty: every row answers a question that
// comes up when something behaves unexpectedly and the console does not explain
// why - "which binary is this?", "which job files did it actually load?", "where
// would a split ledger go?", "how long has it been up?". Each one has cost a
// debugging session at some point because the answer was only in the process
// command line.
//
// Rows that were here and are gone: the in-memory history/log counters (the
// history and log pages already show the real numbers, and a buffer length is
// not something anyone acts on) and the wall-clock start time (the same fact as
// the uptime row, written twice).
type InfoView struct {
	// Version comes from the build metadata, so it is the revision that was
	// compiled rather than a number somebody remembered to bump.
	Version string `json:"version"`
	// GoVersion and GOOS/GOARCH say which binary this is when several exist.
	GoVersion string `json:"go_version"`
	GOOS      string `json:"goos"`
	GOARCH    string `json:"goarch"`

	// Addr is the listen address the console is served on.
	Addr string `json:"addr"`
	// StartedAt and UptimeSeconds describe this process, not the jobs.
	StartedAt     time.Time `json:"started_at"`
	UptimeSeconds int64     `json:"uptime_seconds"`

	// JobFiles are the files this daemon loaded, in order. A job missing from
	// here was never passed on the command line.
	JobFiles []string `json:"job_files"`
	// ConfigFile is the rclone configuration holding the credentials.
	ConfigFile string `json:"config_file"`
	// SplitDir is where split ledgers are written. Empty means the default
	// location, which is not worth a row of its own.
	SplitDir string `json:"split_dir"`
}

// handleInfo reports what this process is and where it reads from.
func (a *API) handleInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}

	view := InfoView{
		Version:    buildVersion(),
		GoVersion:  runtime.Version(),
		GOOS:       runtime.GOOS,
		GOARCH:     runtime.GOARCH,
		Addr:       a.Addr,
		ConfigFile: config.GetConfigPath(),
		SplitDir:   a.SplitDir,
	}
	if a.StartedAt.IsZero() {
		a.StartedAt = time.Now()
	}
	view.StartedAt = a.StartedAt
	view.UptimeSeconds = int64(time.Since(a.StartedAt).Seconds())

	for _, st := range a.D.Status() {
		view.JobFiles = append(view.JobFiles, st.File)
	}
	writeJSON(w, http.StatusOK, view)
}

// buildVersion reads the revision the binary was built from.
//
// A plain `go build` leaves Main.Version as "(devel)", so the VCS stamp is what
// actually identifies the build. Without it "which version are you running?"
// can only be answered by "the one in that folder", which stops being true the
// moment somebody copies the exe somewhere.
func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	var revision, dirty string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			if s.Value == "true" {
				dirty = "+dirty"
			}
		}
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	if revision != "" {
		if info.Main.Version != "" && info.Main.Version != "(devel)" {
			return info.Main.Version + " (" + revision + dirty + ")"
		}
		return revision + dirty
	}
	if info.Main.Version != "" {
		return info.Main.Version
	}
	return "unknown"
}
