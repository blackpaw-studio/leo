package web

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/blackpaw-studio/leo/internal/observe"
)

// maxProgressBodyBytes bounds a progress report; the largest legitimate one
// is a few hundred bytes of bounded strings.
const maxProgressBodyBytes = 16 << 10

// progressRequest is the body of POST /api/agent/progress: the launch's
// attention token plus one task update. It deliberately has no agent field:
// the agent is whoever the token resolves to.
type progressRequest struct {
	Token string `json:"token"`
	observe.TaskUpdate
}

// progressResponse is the success body: {ok, task}.
type progressResponse struct {
	OK   bool              `json:"ok"`
	Task observe.AgentTask `json:"task"`
}

// handleAPIAgentProgress records one task update from a supervised agent
// (leo_progress / `leo progress`) for the agent its per-launch attention
// token currently belongs to. Unlike the hook route it answers errors: the
// agent needs to read a validation or cap failure.
// POST /api/agent/progress
func (s *Server) handleAPIAgentProgress(w http.ResponseWriter, r *http.Request) {
	if s.progress == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiResponse{Error: "task progress is not available"})
		return
	}
	var req progressRequest
	if !decodeControlBody(w, r, maxProgressBodyBytes, false, &req) {
		return
	}
	name, ok := "", false
	if s.attention != nil {
		name, ok = s.attention.AgentForToken(req.Token)
	}
	if !ok {
		writeJSON(w, http.StatusForbidden, apiResponse{Error: "unknown or expired agent token (is this running inside a Leo agent?)"})
		return
	}
	// Re-checked under the store's lock so a stop that revokes the token
	// and clears the agent's tasks cannot be followed by this write.
	stillOurs := func() bool {
		current, ok := s.attention.AgentForToken(req.Token)
		return ok && current == name
	}
	task, err := s.progress.UpsertGuarded(name, req.TaskUpdate, stillOurs)
	if err != nil {
		writeProgressError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(progressResponse{OK: true, Task: task})
}

// handleAPIDismissTask removes one ended task. Operator only: agents
// cannot dismiss their own tasks.
// DELETE /api/v1/agents/{name}/tasks/{id}
func (s *Server) handleAPIDismissTask(w http.ResponseWriter, r *http.Request) {
	if err := s.progress.Dismiss(r.PathValue("name"), r.PathValue("id")); err != nil {
		writeProgressError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeProgressError renders a task store error with its status.
func writeProgressError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, observe.ErrTaskInvalid):
		status = http.StatusBadRequest
	case errors.Is(err, observe.ErrTaskCap), errors.Is(err, observe.ErrTaskRunning), errors.Is(err, observe.ErrTaskNotDismissable):
		status = http.StatusConflict
	case errors.Is(err, observe.ErrTaskNotFound):
		status = http.StatusNotFound
	case errors.Is(err, observe.ErrTaskUnauthorized):
		status = http.StatusForbidden
	}
	writeJSON(w, status, apiResponse{Error: err.Error()})
}
