package daemon

import (
	"encoding/json"
	"net/http"

	"weftsync/autostart"
)

// The login-startup toggle.
//
// The command written to the entry is captured by the process that is running
// right now (API.Autostart), not assembled from the browser. The browser has no
// idea where the binary lives or which job files are in use, and asking it would
// turn "enable autostart" into a way to register an arbitrary command in the
// user's startup list.

// autostartStatus is what the settings page renders.
func (a *API) autostartStatus() autostart.Status {
	if a.Autostart.Exe == "" {
		return autostart.Status{
			Supported: false,
			Location:  autostart.Location(),
			Detail:    "this daemon was not started in a way that records a command",
		}
	}
	st, err := autostart.Read(a.Autostart)
	if err != nil {
		return autostart.Status{
			Supported: autostart.Supported(),
			Location:  autostart.Location(),
			Detail:    err.Error(),
		}
	}
	return st
}

// handleAutostart reads or changes the login-startup entry.
func (a *API) handleAutostart(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, a.autostartStatus())

	case http.MethodPost, http.MethodPut:
		if a.Autostart.Exe == "" {
			writeError(w, http.StatusConflict,
				"this daemon does not know its own command line, so it cannot register one")
			return
		}

		var in struct {
			// Enabled is a pointer so a body without the field is a client
			// error rather than a silent "disable".
			Enabled *bool `json:"enabled"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if in.Enabled == nil {
			writeError(w, http.StatusBadRequest, `"enabled" is required`)
			return
		}

		var err error
		if *in.Enabled {
			err = autostart.Enable(a.Autostart)
		} else {
			err = autostart.Disable()
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, a.autostartStatus())

	default:
		writeError(w, http.StatusMethodNotAllowed, "GET or POST only")
	}
}
