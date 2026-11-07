package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
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
	mux.HandleFunc("/api/config", a.handleConfig)
	mux.HandleFunc("/api/autostart", a.handleAutostart)
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
