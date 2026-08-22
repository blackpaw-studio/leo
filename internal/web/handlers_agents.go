package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"sort"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/redact"
)

// resolveAgentQuery resolves a shorthand query to the canonical agent name
// using the configured AgentService. Resolve itself matches dormant agents
// exactly like live ones, so no separate fallback is needed here. Returns a
// classified HTTP status so callers can respond consistently (404 not found,
// 409 ambiguous, 500 other).
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
		Template string            `json:"template"`
		Repo     string            `json:"repo"`
		Name     string            `json:"name,omitempty"`
		Branch   string            `json:"branch,omitempty"`
		Base     string            `json:"base,omitempty"`
		Prompt   string            `json:"prompt,omitempty"`
		Env      map[string]string `json:"env,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: fmt.Sprintf("invalid request: %v", err)})
		return
	}

	rec, err := s.agentSvc.Spawn(r.Context(), agent.SpawnSpec{
		Template: req.Template,
		Repo:     req.Repo,
		Name:     req.Name,
		Branch:   req.Branch,
		Base:     req.Base,
		Prompt:   req.Prompt,
		Env:      req.Env,
	})
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
	if err := s.agentSvc.Stop(rec.Name, agent.StopOptions{}); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, apiResponse{OK: true})
}

// handleAPIAgentSuspend suspends a running agent via JSON.
// POST /api/agent/suspend  {name: "agent-name"}
//
// A running agent resolves normally, so shorthand queries work here just like
// stop. (Resume, by contrast, cannot resolve — see handleAPIAgentResume.)
func (s *Server) handleAPIAgentSuspend(w http.ResponseWriter, r *http.Request) {
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
	if err := s.agentSvc.Stop(rec.Name, agent.StopOptions{WakeOnMessage: true}); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, apiResponse{OK: true})
}

// handleAPIAgentResume starts a dormant agent via JSON.
// POST /api/agent/resume  {name: "agent-name"}
//
// Unlike stop/suspend this does NOT resolve shorthand: Manager.Resolve matches
// live agents only, and a dormant agent is not live. Callers pass the exact
// agent name; Start looks it up in the agentstore itself.
func (s *Server) handleAPIAgentResume(w http.ResponseWriter, r *http.Request) {
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

	if err := s.agentSvc.Start(req.Name); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: map[string]string{
		"name":   req.Name,
		"status": "starting",
	}})
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

// templateInfo is the trimmed public view of a template — enough to pick one
// to spawn from, and nothing more.
//
// It deliberately carries EnvKeys rather than the env map: /api/template/list
// is what the leo_list_templates MCP tool serves, so every value in this
// payload lands in the calling agent's context (and its transcript, and any
// summary it writes). Template env routinely holds live credentials. Key names
// answer "what does this template configure?" without disclosing any of them.
type templateInfo struct {
	Name      string   `json:"name"`
	Workspace string   `json:"workspace,omitempty"`
	Model     string   `json:"model,omitempty"`
	Harness   string   `json:"harness,omitempty"`
	MaxTurns  int      `json:"max_turns,omitempty"`
	Channels  []string `json:"channels,omitempty"`
	AddDirs   []string `json:"add_dirs,omitempty"`
	EnvKeys   []string `json:"env_keys,omitempty"`
}

// handleAPITemplateList returns all configured templates.
// GET /api/template/list
func (s *Server) handleAPITemplateList(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.loadConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: err.Error()})
		return
	}

	templates := make([]templateInfo, 0, len(cfg.Templates))
	for name, tmpl := range cfg.Templates {
		templates = append(templates, templateInfo{
			Name:      name,
			Workspace: tmpl.Workspace,
			Model:     tmpl.Model,
			Harness:   tmpl.Harness,
			MaxTurns:  tmpl.MaxTurns,
			Channels:  tmpl.Channels,
			AddDirs:   tmpl.AddDirs,
			EnvKeys:   redact.Keys(tmpl.Env),
		})
	}
	// Config maps iterate in random order; sort so the listing is stable
	// across calls (and so tests can index it).
	sort.Slice(templates, func(i, j int) bool { return templates[i].Name < templates[j].Name })

	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: templates})
}

// handlePartialAgents renders the agents.html fragment. It's no longer a
// standalone route (the /agents page renders it via handlePage), but
// handleWebAgentRename still calls it directly to re-render the list in
// place after a rename.
func (s *Server) handlePartialAgents(w http.ResponseWriter, r *http.Request) {
	data, err := s.buildAgentsData(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.templates.ExecuteTemplate(w, "agents.html", data) //nolint:errcheck
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
	if templateName == "" {
		s.renderFlash(w, "error", "Template is required")
		return
	}

	rec, err := s.agentSvc.Spawn(r.Context(), agent.SpawnSpec{Template: templateName, Repo: repo})
	if err != nil {
		s.renderFlash(w, "error", err.Error())
		return
	}

	s.renderFlash(w, "success", fmt.Sprintf("Agent %q spawned — connect via Claude web or app", agent.DisplayName(rec.Name)))
}

// handleWebAgentStop stops an agent via the web UI (form post). Resolve
// matches dormant agents (including a failed-restore record kept Stopped by
// RestoreAgents) exactly like live ones, so it can still be removed from the
// web UI rather than being a permanently stuck entry.
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
	if err := s.agentSvc.Stop(rec.Name, agent.StopOptions{}); err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Failed to stop agent: %v", err))
		return
	}

	s.renderFlash(w, "success", fmt.Sprintf("Agent %q stopped", agent.DisplayName(rec.Name)))
}

// handleWebAgentSuspend suspends a running agent via the web UI (form post),
// then re-renders the agents partial so the status flips to "suspended" and the
// action becomes Resume. Both this and handleWebAgentResume take the canonical
// name straight from the path — the agents template only ever posts {{.Name}},
// and unlike stop/rename we cannot round-trip through resolveAgentQuery for the
// resume case (Manager.Resolve matches live agents only, and a suspended agent
// is not live). The Suspend/Resume methods look up the agentstore themselves.
//
// On success the button's hx-target (#agents-content, outerHTML swap) receives
// the re-rendered list; the error path retargets to #flash-container, mirroring
// handleWebAgentRename's swap strategy.
func (s *Server) handleWebAgentSuspend(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if s.agentSvc == nil {
		s.renderFlashToContainer(w, "error", "Agent service not available")
		return
	}
	if err := s.agentSvc.Stop(name, agent.StopOptions{WakeOnMessage: true}); err != nil {
		s.renderFlashToContainer(w, "error", fmt.Sprintf("Failed to suspend agent: %v", err))
		return
	}
	// Re-render so the suspended agent shows its new status and Resume button.
	s.handlePartialAgents(w, r)
}

// handleWebAgentResume resumes a suspended agent via the web UI (form post),
// rejoining its prior session, then re-renders the agents partial so the status
// flips back to running. See handleWebAgentSuspend for the name/swap rationale.
func (s *Server) handleWebAgentResume(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if s.agentSvc == nil {
		s.renderFlashToContainer(w, "error", "Agent service not available")
		return
	}
	if err := s.agentSvc.Start(name); err != nil {
		s.renderFlashToContainer(w, "error", fmt.Sprintf("Failed to resume agent: %v", err))
		return
	}
	s.handlePartialAgents(w, r)
}

// renameErrorStatus classifies a Manager.Rename error into an HTTP status,
// mirroring how the daemon maps the same sentinels: collisions/unchanged/invalid
// names are client errors (4xx), everything else is a server error.
func renameErrorStatus(err error) int {
	switch {
	case errors.Is(err, agent.ErrAgentNameTaken):
		return http.StatusConflict
	case errors.Is(err, agent.ErrAgentNameUnchanged), errors.Is(err, agent.ErrInvalidAgentName):
		return http.StatusBadRequest
	default:
		// Resolve failures (not found / ambiguous) surface here too; treat an
		// unresolvable agent as a bad request from the caller's perspective.
		return http.StatusBadRequest
	}
}

// handleAPIAgentRename renames an agent via JSON.
// POST /api/agent/{name}/rename  {new_name: "renamed-agent"}
func (s *Server) handleAPIAgentRename(w http.ResponseWriter, r *http.Request) {
	if s.agentSvc == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiResponse{Error: "agent service not available"})
		return
	}

	name := r.PathValue("name")

	var req struct {
		NewName string `json:"new_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: fmt.Sprintf("invalid request: %v", err)})
		return
	}
	if req.NewName == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "new_name is required"})
		return
	}

	updated, err := s.agentSvc.Rename(name, req.NewName)
	if err != nil {
		writeJSON(w, renameErrorStatus(err), apiResponse{Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: map[string]string{
		"name":      updated.Name,
		"workspace": updated.Workspace,
	}})
}

// handleWebAgentRename renames an agent via the web UI (form post), then
// re-renders the agents partial so the new name shows immediately.
//
// The rename form targets #agents-content with an outerHTML swap so the SUCCESS
// path can re-render the list in place. On the ERROR path that target is wrong:
// outerHTML-swapping a flash fragment into #agents-content would destroy the
// agents tab (spawn form + list) and remove the swap target itself. To stay
// consistent with every other flash-emitting action in this UI, the error path
// retargets the response to the shared #flash-container (innerHTML) via htmx
// response headers, then renders the flash exactly like stop/spawn do. Stock
// htmx only swaps 2xx responses and there is no global error-swap hook, so the
// flash is returned with a 200 status (the message conveys the failure).
func (s *Server) handleWebAgentRename(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if s.agentSvc == nil {
		s.renderFlashToContainer(w, "error", "Agent service not available")
		return
	}

	newName := r.FormValue("new_name")
	if newName == "" {
		s.renderFlashToContainer(w, "error", "New name is required")
		return
	}

	if _, err := s.agentSvc.Rename(name, newName); err != nil {
		s.renderFlashToContainer(w, "error", fmt.Sprintf("Failed to rename agent: %v", err))
		return
	}

	// Re-render the agents partial so the renamed agent appears in place.
	s.handlePartialAgents(w, r)
}

// renderFlashToContainer renders a flash for an action whose form targets a
// non-#flash-container element, redirecting the swap to the shared flash
// container so the tab/list it would otherwise replace stays intact. It mirrors
// the rest of the UI's convention (#flash-container / innerHTML, 200 status).
func (s *Server) renderFlashToContainer(w http.ResponseWriter, typ, msg string) {
	w.Header().Set("HX-Retarget", "#flash-container")
	w.Header().Set("HX-Reswap", "innerHTML")
	s.renderFlash(w, typ, msg)
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
