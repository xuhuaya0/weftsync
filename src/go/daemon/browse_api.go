package daemon

import (
	"net/http"

	"github.com/rclone/rclone/fs"
)

// handleBrowse lists subdirectories for the folder picker.
//
// GET /api/browse?path=<dir>
//
// Loopback only, like the rest of the API. It exposes directory names, not file
// contents, and only to a client that can already start syncs - so it does not
// widen what this API can do.
func (a *API) handleBrowse(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	path := r.URL.Query().Get("path")
	view := browseView(path)
	// A listing problem is reported inside the view, not as an HTTP error: the
	// picker still has to render the parent so the user can navigate away.
	if view.Error != "" {
		fs.Debugf(nil, "browse %q: %s", path, view.Error)
	}
	writeJSON(w, http.StatusOK, view)
}
