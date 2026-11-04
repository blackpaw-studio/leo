package daemon

import (
	"errors"
	"net/http"

	"github.com/blackpaw-studio/leo/internal/observe"
)

// handleTaskDismiss removes one ended agent task. The socket's access is
// the operator's own, so this mirrors the web API's operator-only
// DELETE /api/v1/agents/{name}/tasks/{id}.
// DELETE /agents/{name}/tasks/{id}
func (s *Server) handleTaskDismiss(w http.ResponseWriter, r *http.Request) {
	err := s.observeProgress.Dismiss(r.PathValue("name"), r.PathValue("id"))
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, observe.ErrTaskNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, observe.ErrTaskRunning):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}
