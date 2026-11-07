package daemon

import (
	"net/http"

	"weftsync/job"
)

// Resolving conflicts from the UI.
//
// The daemon's job here is to decide whether the request is allowed at all, then
// hand it to the job layer. Two refusals are deliberate:
//
//   - While a run is in progress, the config and the state it is using are being
//     read and written; editing the snapshot underneath it would apply a change to
//     a run that already decided what to do.
//   - A disabled job can still be resolved, because fixing a conflict is how you
//     get it ready to enable.

// handleConflictResolve settles one conflicting path.
//
// Query parameters: job, path, action (push|pull), pair (optional when the job has
// exactly one pair).
func (a *API) handleConflictResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	q := r.URL.Query()
	name := q.Get("job")
	path := q.Get("path")
	action := q.Get("action")
	pairName := q.Get("pair")

	if name == "" {
		writeError(w, http.StatusBadRequest, "the job query parameter is required")
		return
	}
	if path == "" {
		writeError(w, http.StatusBadRequest, "the path query parameter is required")
		return
	}
	switch action {
	case job.ActionPush, job.ActionPull:
	default:
		writeError(w, http.StatusBadRequest,
			`the action must be "push" (keep local) or "pull" (keep remote)`)
		return
	}

	cfg, err := a.D.ConfigOf(name)
	switch {
	case err == ErrNoSuchJob:
		writeError(w, http.StatusNotFound, err.Error())
		return
	case err == ErrJobRunning:
		writeError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if err := job.ResolveConflict(r.Context(), cfg, pairName, path, action); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	msg := "已按本地为准处理：下次运行会覆盖云端那一份"
	if action == job.ActionPull {
		msg = "已按云端为准处理：本地文件已被云端版本覆盖"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"resolved": true, "job": name, "path": path, "action": action, "message": msg,
	})
}
