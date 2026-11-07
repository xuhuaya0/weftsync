package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
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
}

// Routes returns the handler for the API.
func (a *API) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", a.handleStatus)
	mux.HandleFunc("/api/run", a.handleRun)
	mux.HandleFunc("/api/history", a.handleHistory)
	mux.HandleFunc("/api/log", a.handleLog)
	mux.HandleFunc("/api/config", a.handleConfig)
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
func (a *API) handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
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

	if name == "" {
		// Run everything enabled.
		var started []string
		var problems []string
		for _, st := range a.D.Status() {
			if !st.Enabled {
				continue
			}
			if err := a.D.Trigger(r.Context(), st.Name); err != nil {
				problems = append(problems, fmt.Sprintf("%s: %v", st.Name, err))
				continue
			}
			started = append(started, st.Name)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"started": started, "problems": problems,
		})
		return
	}

	if err := a.D.Trigger(r.Context(), name); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"started": []string{name}})
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
