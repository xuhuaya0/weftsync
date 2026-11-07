package daemon

import (
	"embed"
	"net/http"
	"strconv"
	"strings"
	"time"
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

// handleLog reports recent daemon log lines, optionally filtered.
//
// Filtering is server-side because the buffer is a fixed window: "the errors from
// this run" may be spread over 500 lines, and sending everything for the browser
// to hide means the interesting lines can be evicted before the filter runs.
//
// Query parameters: level, contains, limit, since (seq number).
func (a *API) handleLog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	q := logQueryFrom(r)
	var lines []LogLine
	if a.D.logs != nil {
		lines = a.D.logs.Filtered(q)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"lines": lines,
		// last_seq lets the page ask for only what it has not seen. It is the
		// newest sequence in the whole buffer, not in the filtered result, so a
		// filtered view still advances its cursor past lines it skipped.
		"last_seq": a.D.lastLogSeq(),
	})
}

// handleLogExport downloads the retained log as text.
//
// A Content-Disposition attachment makes the browser save a file instead of
// rendering it, which is what somebody collecting a log to send on actually
// wants.
func (a *API) handleLogExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	if a.D.logs == nil {
		writeError(w, http.StatusServiceUnavailable, "log capture is not enabled")
		return
	}

	body := a.D.logs.DumpText(logQueryFrom(r))
	name := "weftsync-" + time.Now().Format("20060102-150405") + ".log"

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(body))
}

// logQueryFrom reads the shared filter parameters.
func logQueryFrom(r *http.Request) LogQuery {
	q := r.URL.Query()
	out := LogQuery{
		Level:    strings.TrimSpace(q.Get("level")),
		Contains: q.Get("contains"),
	}
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 {
		out.Limit = n
	}
	if n, err := strconv.ParseInt(q.Get("since"), 10, 64); err == nil && n > 0 {
		out.Since = n
	}
	return out
}
