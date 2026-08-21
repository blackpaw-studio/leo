package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/harness"
)

// resolveAgentOrError resolves a shorthand query against the agent manager and
// writes the appropriate HTTP error (404 not found, 409 ambiguous, 500 other)
// when resolution fails. Ambiguous and not-found responses carry a machine-
// readable Code and (for ambiguous) the candidate Matches so clients can
// reconstruct typed errors. Returns the canonical Record and true on success.
func (s *Server) resolveAgentOrError(w http.ResponseWriter, query string) (agent.Record, bool) {
	return s.resolveAgentOrErrorWithFallback(w, query, nil)
}

// resolveAgentOrErrorWithFallback is resolveAgentOrError plus an optional
// store fallback tried when Resolve reports not-found — used by
// handleAgentRestart and handleAgentStop so a record Resolve deliberately
// excludes (a stopped agent) can still be reached by name when the caller's
// own logic says it's recoverable. A nil fallback makes this identical to
// resolveAgentOrError.
func (s *Server) resolveAgentOrErrorWithFallback(w http.ResponseWriter, query string, fallback func(string) (agent.Record, bool)) (agent.Record, bool) {
	rec, err := s.agentMgr.Resolve(query)
	if err == nil {
		return rec, true
	}
	var nf *agent.ErrNotFound
	var amb *agent.ErrAmbiguous
	switch {
	case errors.As(err, &nf):
		if fallback != nil {
			if fb, ok := fallback(query); ok {
				return fb, true
			}
		}
		writeJSON(w, http.StatusNotFound, Response{OK: false, Error: err.Error(), Code: ErrorCodeNotFound})
	case errors.As(err, &amb):
		writeJSON(w, http.StatusConflict, Response{OK: false, Error: err.Error(), Code: ErrorCodeAmbiguous, Matches: amb.Matches})
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
	return agent.Record{}, false
}

// handleAgentSpawn drives agent.Manager.Spawn via POST /agents/spawn.
func (s *Server) handleAgentSpawn(w http.ResponseWriter, r *http.Request) {
	if s.agentMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "agent manager not attached")
		return
	}

	var req AgentSpawnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid request: %v", err))
		return
	}
	if req.Template == "" && req.FromAgent == "" {
		writeError(w, http.StatusBadRequest, "template or from_agent is required")
		return
	}

	rec, err := s.agentMgr.Spawn(r.Context(), agent.SpawnSpec{
		Template:    req.Template,
		FromAgent:   req.FromAgent,
		Repo:        req.Repo,
		Name:        req.Name,
		Branch:      req.Branch,
		Base:        req.Base,
		Prompt:      req.Prompt,
		Env:         req.Env,
		IdleSuspend: req.IdleSuspend,
	})
	if err != nil {
		writeAgentError(w, err)
		return
	}

	data, err := json.Marshal(rec)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("marshaling record: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, Response{OK: true, Data: data})
}

// handleAgentList returns every running ephemeral agent.
func (s *Server) handleAgentList(w http.ResponseWriter, r *http.Request) {
	if s.agentMgr == nil {
		writeJSON(w, http.StatusOK, Response{OK: true, Data: json.RawMessage("[]")})
		return
	}
	records := s.agentMgr.List()
	data, err := json.Marshal(records)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("marshaling records: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, Response{OK: true, Data: data})
}

// handleAgentStop stops an agent by name or shorthand (repo, repo-short,
// suffix). The server resolves the query to a canonical agent before
// stopping. Like handleAgentRestart, falls back to ResolveRecoverable when
// Resolve reports not-found — otherwise a failed-restore record (Stopped +
// StoppedReason, no live process to kill) would be permanently unreachable
// via `leo agent stop`, since Resolve deliberately excludes every stopped
// record. The agent always stays dormant (record kept) — see agent.Manager.Stop.
func (s *Server) handleAgentStop(w http.ResponseWriter, r *http.Request) {
	if s.agentMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "agent manager not attached")
		return
	}
	query := r.PathValue("name")
	if query == "" {
		writeError(w, http.StatusBadRequest, "agent name is required")
		return
	}
	var req AgentStopRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid request: %v", err))
			return
		}
	}
	rec, ok := s.resolveAgentOrErrorWithFallback(w, query, s.agentMgr.ResolveRecoverable)
	if !ok {
		return
	}
	if err := s.agentMgr.Stop(rec.Name, agent.StopOptions{WakeOnMessage: req.WakeOnMessage}); err != nil {
		writeAgentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, Response{OK: true})
}

// handleAgentStart starts a dormant agent by name via POST /agents/{name}/start.
// The agent is re-spawned with --resume so the prior conversation continues.
func (s *Server) handleAgentStart(w http.ResponseWriter, r *http.Request) {
	if s.agentMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "agent manager not attached")
		return
	}
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "agent name is required")
		return
	}
	if err := s.agentMgr.Start(name); err != nil {
		writeAgentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, Response{OK: true})
}

// handleAgentReset resets an agent by name or shorthand via POST
// /agents/{name}/reset. The server resolves the query to a canonical agent
// (same resolution as stop) before stopping its process/tmux, clearing the
// stored session id, and respawning it fresh.
func (s *Server) handleAgentReset(w http.ResponseWriter, r *http.Request) {
	if s.agentMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "agent manager not attached")
		return
	}
	query := r.PathValue("name")
	if query == "" {
		writeError(w, http.StatusBadRequest, "agent name is required")
		return
	}
	rec, ok := s.resolveAgentOrError(w, query)
	if !ok {
		return
	}
	if err := s.agentMgr.Reset(rec.Name); err != nil {
		writeAgentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, Response{OK: true})
}

// handleAgentRestart bounces a running agent by name or shorthand via POST
// /agents/{name}/restart: the server resolves the query to a canonical agent
// (same resolution as stop/reset), then kills and respawns it with --resume
// so the conversation carries over.
func (s *Server) handleAgentRestart(w http.ResponseWriter, r *http.Request) {
	if s.agentMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "agent manager not attached")
		return
	}
	query := r.PathValue("name")
	if query == "" {
		writeError(w, http.StatusBadRequest, "agent name is required")
		return
	}
	rec, ok := s.resolveAgentOrErrorWithFallback(w, query, s.agentMgr.ResolveRecoverable)
	if !ok {
		return
	}
	if err := s.agentMgr.Restart(rec.Name); err != nil {
		writeAgentError(w, err)
		return
	}
	// Echo the canonical name back so a caller that resolved a shorthand or
	// went through the ResolveRecoverable fallback (Resolve itself
	// excludes stopped records) can report which agent actually came back,
	// without a separate pre-resolve call of its own.
	data, err := json.Marshal(rec)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("marshaling record: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, Response{OK: true, Data: data})
}

// handleAgentSetTemplate re-points an agent at a different template via POST
// /agents/{name}/set-template. The server resolves the query to a canonical
// agent (same resolution as stop/reset/restart), then swaps the agent's
// template — stopping and respawning it when live, rewriting the record when
// suspended — and returns the switch result so the caller can report which
// conversation came back.
func (s *Server) handleAgentSetTemplate(w http.ResponseWriter, r *http.Request) {
	if s.agentMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "agent manager not attached")
		return
	}
	query := r.PathValue("name")
	if query == "" {
		writeError(w, http.StatusBadRequest, "agent name is required")
		return
	}
	template := r.URL.Query().Get("template")
	if template == "" {
		writeError(w, http.StatusBadRequest, "template is required")
		return
	}
	rec, ok := s.resolveAgentOrError(w, query)
	if !ok {
		return
	}
	result, err := s.agentMgr.SwitchTemplate(rec.Name, template)
	if err != nil {
		writeAgentError(w, err)
		return
	}
	data, err := json.Marshal(result)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("marshaling switch result: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, Response{OK: true, Data: data})
}

// handleAgentStale reports which running agents would change if restarted,
// via GET /agents/stale. `leo update` calls it after swapping the binary to
// decide whether to offer a restart, and for which agents.
//
// The payload carries env KEY NAMES only, never values — see agent.StaleAgent.
func (s *Server) handleAgentStale(w http.ResponseWriter, r *http.Request) {
	if s.agentMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "agent manager not attached")
		return
	}
	stale := s.agentMgr.StaleAgents()
	if stale == nil {
		// Serialize as [] rather than null so callers can range without a
		// nil check.
		stale = []agent.StaleAgent{}
	}
	data, err := json.Marshal(stale)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("marshaling stale-agent response: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, Response{OK: true, Data: data})
}

// handleAgentRestartAll bounces every currently-running agent via POST
// /agents/restart, skipping suspended/stopped agents. Per-agent failures are
// reported in the response rather than aborting the batch.
func (s *Server) handleAgentRestartAll(w http.ResponseWriter, r *http.Request) {
	if s.agentMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "agent manager not attached")
		return
	}
	result := s.agentMgr.RestartAll()

	failed := make(map[string]string, len(result.Failed))
	for name, err := range result.Failed {
		failed[name] = err.Error()
	}
	resp := AgentRestartAllResponse{
		Restarted: result.Restarted,
		Skipped:   result.Skipped,
		Failed:    failed,
	}
	data, err := json.Marshal(resp)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("marshaling restart-all response: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, Response{OK: true, Data: data})
}

// handleAgentLogs returns the most recent `lines` lines of the agent's tmux
// pane. The `name` path segment may be a shorthand query; it is resolved to
// the canonical agent before capturing the pane. Defaults to 200 lines when
// the query param is missing.
func (s *Server) handleAgentLogs(w http.ResponseWriter, r *http.Request) {
	if s.agentMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "agent manager not attached")
		return
	}
	query := r.PathValue("name")
	if query == "" {
		writeError(w, http.StatusBadRequest, "agent name is required")
		return
	}

	lines := 200
	if raw := r.URL.Query().Get("lines"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid lines param: %v", err))
			return
		}
		lines = n
	}

	rec, ok := s.resolveAgentOrError(w, query)
	if !ok {
		return
	}
	output, err := s.agentMgr.Logs(rec.Name, lines)
	if err != nil {
		writeAgentError(w, err)
		return
	}
	data, err := json.Marshal(AgentLogsResponse{Output: output})
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("marshaling logs: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, Response{OK: true, Data: data})
}

// handleAgentSession returns the tmux session name for an agent. The `name`
// path segment may be a shorthand query; the server resolves it to the
// canonical name and echoes both back to the client.
func (s *Server) handleAgentSession(w http.ResponseWriter, r *http.Request) {
	if s.agentMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "agent manager not attached")
		return
	}
	query := r.PathValue("name")
	if query == "" {
		writeError(w, http.StatusBadRequest, "agent name is required")
		return
	}

	rec, ok := s.resolveAgentOrError(w, query)
	if !ok {
		return
	}

	data, err := json.Marshal(AgentSessionResponse{
		Session: s.agentMgr.SessionName(rec.Name),
		Name:    rec.Name,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("marshaling session: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, Response{OK: true, Data: data})
}

// handleAgentAttachSpec returns how the CLI should attach to a non-claude
// agent's live session: the driver's resolved TmuxSession (every harness's
// TUI runs in a leo tmux session, so this is always a plain tmux attach).
// Claude agents (Harness == "" or "claude") get an empty TmuxSession — the
// CLI already has its own tmux-based attach flow for those via AgentSession
// and never needs to reach this endpoint for them, but the response is still
// well-formed if it does.
func (s *Server) handleAgentAttachSpec(w http.ResponseWriter, r *http.Request) {
	if s.agentMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "agent manager not attached")
		return
	}
	query := r.PathValue("name")
	if query == "" {
		writeError(w, http.StatusBadRequest, "agent name is required")
		return
	}
	rec, ok := s.resolveAgentOrError(w, query)
	if !ok {
		return
	}

	resp := AgentAttachSpecResponse{Name: rec.Name}
	harnessName, h, resolved := s.agentMgr.ResolveHandle(rec.Name)
	if resolved && harnessName != "" && harnessName != "claude" {
		resp.Harness = harnessName
		if hd, err := harness.Get(harnessName); err == nil {
			if drv := hd.Driver(); drv != nil {
				if spec, err := drv.Attach(h); err == nil {
					resp.TmuxSession = spec.TmuxSession
				}
			}
		}
	}

	data, err := json.Marshal(resp)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("marshaling attach spec: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, Response{OK: true, Data: data})
}

// handleAgentResolve is a read-only lookup that maps a shorthand query to the
// canonical agent name and tmux session. Useful for remote clients that want
// to confirm an agent exists before taking an action.
func (s *Server) handleAgentResolve(w http.ResponseWriter, r *http.Request) {
	if s.agentMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "agent manager not attached")
		return
	}
	query := r.URL.Query().Get("q")
	if query == "" {
		writeError(w, http.StatusBadRequest, "query parameter 'q' is required")
		return
	}
	rec, ok := s.resolveAgentOrError(w, query)
	if !ok {
		return
	}
	data, err := json.Marshal(AgentResolveResponse{
		Name:    rec.Name,
		Session: s.agentMgr.SessionName(rec.Name),
		Repo:    rec.Repo,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("marshaling resolve: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, Response{OK: true, Data: data})
}

// handleAgentDelete removes the agentstore record — plus the worktree and
// branch when the agent has one — via DELETE /agents/{name}. Refuses a live
// agent. The `name` path segment must be an exact agent name because
// shorthand resolution only matches live agents and a deletable agent has
// already been stopped.
func (s *Server) handleAgentDelete(w http.ResponseWriter, r *http.Request) {
	if s.agentMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "agent manager not attached")
		return
	}
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "agent name is required")
		return
	}

	var req AgentDeleteRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid request: %v", err))
			return
		}
	}

	if err := s.agentMgr.Delete(r.Context(), name, agent.DeleteOptions{
		Force:        req.Force,
		DeleteBranch: req.DeleteBranch,
	}); err != nil {
		writeAgentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, Response{OK: true})
}

// handleAgentRename renames an agent. The {name} path segment may be a
// shorthand query; it is resolved to the canonical agent, then Rename applies
// the new name across supervisor state and the persisted record.
func (s *Server) handleAgentRename(w http.ResponseWriter, r *http.Request) {
	if s.agentMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "agent manager not attached")
		return
	}
	query := r.PathValue("name")
	if query == "" {
		writeError(w, http.StatusBadRequest, "agent name is required")
		return
	}
	var req AgentRenameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
		return
	}
	if req.NewName == "" {
		writeError(w, http.StatusBadRequest, "new_name is required")
		return
	}
	updated, err := s.agentMgr.Rename(query, req.NewName)
	if err != nil {
		var nf *agent.ErrNotFound
		var amb *agent.ErrAmbiguous
		switch {
		case errors.As(err, &nf):
			writeJSON(w, http.StatusNotFound, Response{OK: false, Error: err.Error(), Code: ErrorCodeNotFound})
		case errors.As(err, &amb):
			writeJSON(w, http.StatusConflict, Response{OK: false, Error: err.Error(), Code: ErrorCodeAmbiguous, Matches: amb.Matches})
		case errors.Is(err, agent.ErrAgentNameTaken):
			writeError(w, http.StatusConflict, err.Error())
		case errors.Is(err, agent.ErrAgentNameUnchanged), errors.Is(err, agent.ErrInvalidAgentName):
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	data, err := json.Marshal(updated)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("marshaling record: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, Response{OK: true, Data: data})
}

// writeAgentError translates agent-package typed errors into HTTP responses
// with stable machine-readable Code fields so the CLI client can reconstruct
// errors.Is matches on the other side of the socket.
func writeAgentError(w http.ResponseWriter, err error) {
	var nf *agent.ErrNotFound
	switch {
	case errors.As(err, &nf):
		writeJSON(w, http.StatusNotFound, Response{OK: false, Error: err.Error(), Code: ErrorCodeNotFound})
	case errors.Is(err, agent.ErrWorktreeRequiresSlash):
		writeJSON(w, http.StatusBadRequest, Response{OK: false, Error: err.Error(), Code: ErrorCodeWorktreeRequireSep})
	case errors.Is(err, agent.ErrAgentStillRunning):
		writeJSON(w, http.StatusConflict, Response{OK: false, Error: err.Error(), Code: ErrorCodeAgentStillRunning})
	case errors.Is(err, agent.ErrNotWorktreeAgent):
		writeJSON(w, http.StatusBadRequest, Response{OK: false, Error: err.Error(), Code: ErrorCodeNotWorktreeAgent})
	case errors.Is(err, agent.ErrWorktreeDirty):
		writeJSON(w, http.StatusConflict, Response{OK: false, Error: err.Error(), Code: ErrorCodeWorktreeDirty})
	case errors.Is(err, agent.ErrBranchCheckedOut):
		writeJSON(w, http.StatusConflict, Response{OK: false, Error: err.Error(), Code: ErrorCodeBranchCheckedOut})
	case errors.Is(err, agent.ErrBranchNotMerged):
		writeJSON(w, http.StatusConflict, Response{OK: false, Error: err.Error(), Code: ErrorCodeBranchNotMerged})
	case errors.Is(err, agent.ErrBranchNotFound):
		writeJSON(w, http.StatusNotFound, Response{OK: false, Error: err.Error(), Code: ErrorCodeBranchNotFound})
	case errors.Is(err, agent.ErrSourceAgentNotFound):
		writeJSON(w, http.StatusNotFound, Response{OK: false, Error: err.Error(), Code: ErrorCodeSourceAgentNotFound})
	case errors.Is(err, agent.ErrSourceNotGitRepo):
		writeJSON(w, http.StatusBadRequest, Response{OK: false, Error: err.Error(), Code: ErrorCodeSourceNotGitRepo})
	case errors.Is(err, agent.ErrAgentStopped):
		writeJSON(w, http.StatusConflict, Response{OK: false, Error: err.Error(), Code: ErrorCodeAgentStopped})
	case errors.Is(err, agent.ErrAgentNotStopped):
		writeJSON(w, http.StatusConflict, Response{OK: false, Error: err.Error(), Code: ErrorCodeAgentNotStopped})
	case errors.Is(err, agent.ErrAgentAlreadyRunning):
		writeJSON(w, http.StatusConflict, Response{OK: false, Error: err.Error(), Code: ErrorCodeAgentAlreadyRunning})
	case errors.Is(err, agent.ErrAgentNotRunning):
		writeJSON(w, http.StatusConflict, Response{OK: false, Error: err.Error(), Code: ErrorCodeAgentNotRunning})
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}
