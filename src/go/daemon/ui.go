package daemon

import (
	"embed"
	"net/http"
)

// The UI is embedded in the binary on purpose: weftsync is meant to be a single
// executable a user can drop anywhere and double-click. Requiring a separate
// directory of assets would make "just run it" fail in the most confusing way
// possible - a blank page.

//go:embed ui/index.html
var uiFS embed.FS

// uiHandler serves the single-page UI.
func (a *API) uiHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeError(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		page, err := uiFS.ReadFile("ui/index.html")
		if err != nil {
			// Should be impossible: the file is embedded at build time.
			writeError(w, http.StatusInternalServerError, "the embedded UI is missing")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// The page polls its own API, so it must never be cached.
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(page)
	})
}

// handleHistory reports completed runs.
func (a *API) handleHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	var runs []RunRecord
	if a.D.history != nil {
		runs = a.D.history.snapshot()
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

// handleLog reports recent daemon log lines.
func (a *API) handleLog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	var lines []LogLine
	if a.D.logs != nil {
		lines = a.D.logs.Recent()
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": lines})
}
