package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
)

// resolveAgentQuery resolves a shorthand query to the canonical agent name
// using the configured AgentService. Returns a classified HTTP status so
// callers can respond consistently (404 not found, 409 ambiguous, 500 other).
func resolveAgentQuery(svc AgentService, query string) (agent.Record, int, error) {
	rec, err := svc.Resolve(query)
	if err == nil {
		return rec, http.StatusOK, nil
	}
	var nf *agent.ErrNotFound
	var amb *agent.ErrAmbiguous
	switch {
	case errors.As(err, &nf):
		return agent.Record{}, http.StatusNotFound, err
	case errors.As(err, &amb):
		return agent.Record{}, http.StatusConflict, err
	default:
		return agent.Record{}, http.StatusInternalServerError, err
	}
}

// apiResponse is the standard JSON envelope for API endpoints.
type apiResponse struct {
	OK    bool   `json:"ok"`
	Data  any    `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, resp apiResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(resp) //nolint:errcheck
}

// handleAPIAgentSpawn spawns an ephemeral agent from a template.
// POST /api/agent/spawn  {template: "coding", repo: "owner/repo" or "name"}
func (s *Server) handleAPIAgentSpawn(w http.ResponseWriter, r *http.Request) {
	if s.agentSvc == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiResponse{Error: "agent service not available"})
		return
	}

	var req struct {
		Template string `json:"template"`
		Repo     string `json:"repo"`
		Name     string `json:"name,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: fmt.Sprintf("invalid request: %v", err)})
		return
	}

	rec, err := s.agentSvc.Spawn(r.Context(), agent.SpawnSpec{Template: req.Template, Repo: req.Repo, Name: req.Name})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: map[string]string{
		"name":      rec.Name,
		"workspace": rec.Workspace,
	}})
}

// handleAPIAgentStop stops a running ephemeral agent.
// POST /api/agent/stop  {name: "agent-coding-leo"}
func (s *Server) handleAPIAgentStop(w http.ResponseWriter, r *http.Request) {
	if s.agentSvc == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiResponse{Error: "agent service not available"})
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: fmt.Sprintf("invalid request: %v", err)})
		return
	}

	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "name is required"})
		return
	}

	rec, status, err := resolveAgentQuery(s.agentSvc, req.Name)
	if err != nil {
		writeJSON(w, status, apiResponse{Error: err.Error()})
		return
	}
	if err := s.agentSvc.Stop(rec.Name); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, apiResponse{OK: true})
}

// handleAPIAgentList returns all running ephemeral agents.
// GET /api/agent/list
func (s *Server) handleAPIAgentList(w http.ResponseWriter, r *http.Request) {
	if s.agentSvc == nil {
		writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: []agent.Record{}})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: s.agentSvc.List()})
}

// handleAPITemplateList returns all configured templates.
// GET /api/template/list
func (s *Server) handleAPITemplateList(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.loadConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: cfg.Templates})
}

// handlePartialAgents renders the agents tab partial.
func (s *Server) handlePartialAgents(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.loadConfig()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var agents []agentData
	if s.agentSvc != nil {
		for _, a := range s.agentSvc.List() {
			agents = append(agents, agentData{
				Name:      a.Name,
				Status:    a.Status,
				StartedAt: a.StartedAt,
				Restarts:  a.Restarts,
				Branch:    a.Branch,
			})
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Re-fetch templates from config for the spawn form.
	s.templates.ExecuteTemplate(w, "agents.html", struct { //nolint:errcheck
		Agents    []agentData
		Templates any
	}{Agents: agents, Templates: cfg.Templates})
}

type agentData struct {
	Name      string
	Status    string
	StartedAt time.Time
	Restarts  int
	Branch    string
}

// handleWebAgentSpawn spawns an agent via the web UI (form post).
func (s *Server) handleWebAgentSpawn(w http.ResponseWriter, r *http.Request) {
	if s.agentSvc == nil {
		s.renderFlash(w, "error", "Agent service not available")
		return
	}

	if err := r.ParseForm(); err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Invalid form: %v", err))
		return
	}

	templateName := r.FormValue("template")
	repo := r.FormValue("repo")
	if templateName == "" || repo == "" {
		s.renderFlash(w, "error", "Template and name/repo are required")
		return
	}

	rec, err := s.agentSvc.Spawn(r.Context(), agent.SpawnSpec{Template: templateName, Repo: repo})
	if err != nil {
		s.renderFlash(w, "error", err.Error())
		return
	}

	s.renderFlash(w, "success", fmt.Sprintf("Agent %q spawned — connect via Claude web or app", rec.Name))
}

// handleWebAgentStop stops an agent via the web UI (form post).
func (s *Server) handleWebAgentStop(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if s.agentSvc == nil {
		s.renderFlash(w, "error", "Agent service not available")
		return
	}

	rec, _, err := resolveAgentQuery(s.agentSvc, name)
	if err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Failed to find agent: %v", err))
		return
	}
	if err := s.agentSvc.Stop(rec.Name); err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Failed to stop agent: %v", err))
		return
	}

	s.renderFlash(w, "success", fmt.Sprintf("Agent %q stopped", rec.Name))
}

// --- Task API endpoints (JSON, used by channel plugins and external clients) ---

// taskInfo is the JSON representation of a task for the API.
type taskInfo struct {
	Name     string `json:"name"`
	Schedule string `json:"schedule"`
	Enabled  bool   `json:"enabled"`
	NextRun  string `json:"next_run,omitempty"`
	LastExit *int   `json:"last_exit,omitempty"`
}

// handleAPITaskList returns all tasks with their status.
// GET /api/task/list
func (s *Server) handleAPITaskList(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.loadConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: err.Error()})
		return
	}

	cronMap := make(map[string]string)
	if s.scheduler != nil {
		for _, e := range s.scheduler.List() {
			cronMap[e.Name] = e.Next.Format(time.RFC3339)
		}
	}

	var tasks []taskInfo
	for name, task := range cfg.Tasks {
		ti := taskInfo{
			Name:     name,
			Schedule: task.Schedule,
			Enabled:  task.Enabled,
		}
		if next, ok := cronMap[name]; ok {
			ti.NextRun = next
		}
		tasks = append(tasks, ti)
	}

	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: tasks})
}

// handleAPITaskRun triggers a task via the API.
// POST /api/task/{name}/run
func (s *Server) handleAPITaskRun(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	cfg, err := s.loadConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: err.Error()})
		return
	}
	if _, ok := cfg.Tasks[name]; !ok {
		writeJSON(w, http.StatusNotFound, apiResponse{Error: fmt.Sprintf("task %q not found", name)})
		return
	}

	cmd := exec.Command(s.leoPath, "run", name, "--config", s.configPath)
	if err := cmd.Start(); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: fmt.Sprintf("starting task: %v", err)})
		return
	}
	go cmd.Wait() //nolint:errcheck

	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: map[string]string{"name": name, "status": "started"}})
}

// handleAPITaskToggle toggles a task's enabled state via the API.
// POST /api/task/{name}/toggle
func (s *Server) handleAPITaskToggle(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	cfg, err := s.loadConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: err.Error()})
		return
	}
	task, ok := cfg.Tasks[name]
	if !ok {
		writeJSON(w, http.StatusNotFound, apiResponse{Error: fmt.Sprintf("task %q not found", name)})
		return
	}

	task.Enabled = !task.Enabled
	cfg.Tasks[name] = task

	if errMsg := s.validateAndSave(cfg); errMsg != "" {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: errMsg})
		return
	}
	if warn := s.reloadConfigOrWarn(); warn != "" {
		writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: map[string]string{"name": name, "warning": warn}})
		return
	}

	action := "enabled"
	if !task.Enabled {
		action = "disabled"
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: map[string]string{"name": name, "status": action}})
}
