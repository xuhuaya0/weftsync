package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"weftsync/autostart"
)

// Local HTTP API.
//
// It binds to 127.0.0.1 only, by design: this server can start syncs and read
// configuration, so exposing it on a LAN address would be a remote-code-ish hole
// with no authentication worth the name. If access from another machine is ever
// wanted, a token and an explicit bind option must come first.

// API is the HTTP surface.
type API struct {
	// D is the daemon being served.
	D *Daemon
	// Addr is the listen address, e.g. "127.0.0.1:5572".
	Addr string
	// Autostart is the command to register for login startup. It is captured
	// from the running process so the entry reproduces what actually works; an
	// empty Exe turns the feature off in the UI instead of guessing.
	Autostart autostart.Spec
	// SplitDir overrides where split undo ledgers are written.
	//
	// Empty means "next to the pair's state file, or under the user config
	// directory when the pair has none". Exposed as a daemon flag because a ledger is
	// a small permanent file the operator may want on a different disk - and because
	// a test must be able to keep them out of the real user profile.
	SplitDir string
	// StartedAt is when this process began serving, shown on the settings page and
	// used by the page to notice that it is running against an older build.
	StartedAt time.Time
	// startedOnce guards the lazy default for StartedAt: /api/status and /api/info
	// are polled concurrently, so two requests could otherwise both write it.
	startedOnce sync.Once
}

// startedAt returns when this process began serving, defaulting to now on the
// first call. A daemon started without an explicit time still needs a stable
// value, or every request would report a different one and the page would think
// the server restarted on every poll.
func (a *API) startedAt() time.Time {
	a.startedOnce.Do(func() {
		if a.StartedAt.IsZero() {
			a.StartedAt = time.Now()
		}
	})
	return a.StartedAt
}

// Routes returns the handler for the API.
func (a *API) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", a.handleStatus)
	mux.HandleFunc("/api/run", a.handleRun)
	mux.HandleFunc("/api/stop", a.handleStop)
	mux.HandleFunc("/api/history", a.handleHistory)
	mux.HandleFunc("/api/log", a.handleLog)
	mux.HandleFunc("/api/log/export", a.handleLogExport)
	mux.HandleFunc("/api/conflict/resolve", a.handleConflictResolve)
	mux.HandleFunc("/api/credentials", a.handleCredentials)
	mux.HandleFunc("/api/credentials/test", a.handleCredentialTest)
	mux.HandleFunc("/api/split/precheck", a.handleSplitPrecheck)
	mux.HandleFunc("/api/split/plan", a.handleSplitPlan)
	mux.HandleFunc("/api/split/execute", a.handleSplitExecute)
	mux.HandleFunc("/api/split/snapshots", a.handleSplitSnapshots)
	mux.HandleFunc("/api/split/undo", a.handleSplitUndo)
	mux.HandleFunc("/api/config", a.handleConfig)
	mux.HandleFunc("/api/config/files", a.handleConfigFiles)
	mux.HandleFunc("/api/config/open", a.handleConfigOpen)
	mux.HandleFunc("/api/config/save-as", a.handleConfigSaveAs)
	mux.HandleFunc("/api/browse", a.handleBrowse)
	mux.HandleFunc("/api/autostart", a.handleAutostart)
	mux.HandleFunc("/api/info", a.handleInfo)
	mux.HandleFunc("/api/settings", a.handleSettings)
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	// The UI is the point of opening the address in a browser.
	mux.Handle("/", a.uiHandler())
	return mux
}

// handleStatus reports every job.
func (a *API) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"jobs": a.D.Status(),
		"now":  a.D.now().Format(time.RFC3339),
		// started identifies this process instance and nothing else. The page
		// remembers the value it was loaded with; a different one means the daemon
		// was restarted, and since the HTML is embedded in the binary, the document
		// in the browser is now stale.
		//
		// This exists because the clock in the bar is refreshed by JavaScript every
		// few seconds, so a stale page still looks alive. Measured twice: after two
		// rounds of CSS fixes the answers were "所以你修了啥我没看见" and
		// "为什么没丝毫变化" - both times the document predated the rebuild.
		"started": a.startedAt().Format(time.RFC3339Nano),
	})
}

// handleRun starts one job, or every enabled job when no name is given.
//
// `plan=true` turns the run into a preview: it measures what would change and
// changes nothing. The flag lives here rather than on a separate endpoint because
// everything except the mode is identical - same single-flight guard, same
// history entry, same progress reporting.
func (a *API) handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}

	mode := ModeRun
	if isTrue(r.URL.Query().Get("plan")) {
		mode = ModePlan
	}

	name := r.URL.Query().Get("job")
	// onlyPaths restricts a real run to the rows the console left ticked. Absent
	// means "everything", which is what a scheduled run and the CLI want.
	var only []string
	if raw := r.URL.Query().Get("only"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &only); err != nil {
			// A malformed selection must not silently become "run everything":
			// that is the difference between doing what was asked and doing more.
			writeError(w, http.StatusBadRequest,
				"the only parameter must be a JSON array of paths: "+err.Error())
			return
		}
	}
	if name == "" {
		if err := json.NewDecoder(r.Body).Decode(&struct {
			Job *string `json:"job"`
		}{Job: &name}); err != nil {
			// An empty body is fine: the query parameter is the documented way.
			name = ""
		}
	}

	start := func(n string) error {
		if mode == ModePlan {
			return a.D.TriggerPlan(r.Context(), n)
		}
		if len(only) > 0 {
			return a.D.TriggerPaths(r.Context(), n, only)
		}
		return a.D.Trigger(r.Context(), n)
	}

	if name == "" {
		// Run everything enabled.
		var started []string
		var problems []string
		for _, st := range a.D.Status() {
			if !st.Enabled {
				continue
			}
			if err := start(st.Name); err != nil {
				problems = append(problems, fmt.Sprintf("%s: %v", st.Name, err))
				continue
			}
			started = append(started, st.Name)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"started": started, "problems": problems, "mode": mode,
		})
		return
	}

	if err := start(name); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"started": []string{name}, "mode": mode})
}

// handleStop cancels the run in flight for one job.
func (a *API) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	name := r.URL.Query().Get("job")
	if name == "" {
		writeError(w, http.StatusBadRequest, "the job query parameter is required")
		return
	}

	err := a.D.StopJob(name)
	switch {
	case err == nil:
		writeJSON(w, http.StatusAccepted, map[string]any{"stopping": name})
	case errors.Is(err, ErrNotRunning):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusNotFound, err.Error())
	}
}

// isTrue accepts the forms a hand-written query string actually uses.
func isTrue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}

// Listen binds the API and returns a server ready to Serve.
//
// Binding is also the single-instance guard: a second daemon on the same port
// fails here with a clear message instead of running two schedulers that would
// both try to sync the same pairs.
func Listen(addr string, a *API) (*http.Server, net.Listener, error) {
	if addr == "" {
		addr = "127.0.0.1:5572"
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, nil, fmt.Errorf("daemon: listen address %q: %w", addr, err)
	}
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return nil, nil, fmt.Errorf(
			"daemon: refusing to listen on %q. This API can start syncs and has no "+
				"authentication, so it must stay on the loopback interface", addr)
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		if strings.Contains(err.Error(), "Only one usage") ||
			strings.Contains(err.Error(), "address already in use") {
			return nil, nil, fmt.Errorf(
				"daemon: %s is already in use, which normally means another weftsync "+
					"daemon is running. Stop it first, or pass a different --addr", addr)
		}
		return nil, nil, fmt.Errorf("daemon: listen on %s: %w", addr, err)
	}

	srv := &http.Server{
		Handler:           a.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return srv, ln, nil
}

// Serve runs the server until ctx is cancelled.
func Serve(ctx context.Context, srv *http.Server, ln net.Listener) error {
	errc := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			errc <- err
			return
		}
		errc <- nil
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}
