package web

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
)

// apiResponse is the standard JSON envelope for API endpoints.
type apiResponse struct {
	OK    bool        `json:"ok"`
	Data  interface{} `json:"data,omitempty"`
	Error string      `json:"error,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, resp apiResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(resp) //nolint:errcheck
}

// handleAPIAgentSpawn spawns an ephemeral agent from a template.
// POST /api/agent/spawn  {template: "coding", repo: "owner/repo" or "name"}
func (s *Server) handleAPIAgentSpawn(w http.ResponseWriter, r *http.Request) {
	if s.agentMgr == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiResponse{Error: "agent manager not available"})
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

	if req.Template == "" || req.Repo == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "template and repo are required"})
		return
	}

	cfg, err := s.loadConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: fmt.Sprintf("loading config: %v", err)})
		return
	}

	tmpl, ok := cfg.Templates[req.Template]
	if !ok {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: fmt.Sprintf("template %q not found", req.Template)})
		return
	}

	// Resolve workspace and agent name
	workspace, agentName, err := resolveAgentWorkspace(tmpl, req.Template, req.Repo, req.Name)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: err.Error()})
		return
	}

	// Deduplicate name
	existing := s.agentMgr.EphemeralAgents()
	if _, collision := existing[agentName]; collision {
		for i := 2; ; i++ {
			candidate := fmt.Sprintf("%s-%d", agentName, i)
			if _, ok := existing[candidate]; !ok {
				agentName = candidate
				break
			}
		}
	}

	// Build claude args from template
	claudeArgs := buildTemplateArgs(cfg, tmpl, agentName, workspace)

	spec := AgentSpawnRequest{
		Name:       agentName,
		ClaudeArgs: claudeArgs,
		WorkDir:    workspace,
		Env:        tmpl.Env,
		WebPort:    strconv.Itoa(cfg.WebPort()),
	}

	if err := s.agentMgr.SpawnAgent(spec); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: fmt.Sprintf("spawning agent: %v", err)})
		return
	}

	// Persist for daemon restart recovery
	agentstore.Save(cfg.HomePath, agentstore.Record{ //nolint:errcheck
		Name:       agentName,
		Template:   req.Template,
		Workspace:  workspace,
		ClaudeArgs: claudeArgs,
		Env:        tmpl.Env,
		WebPort:    strconv.Itoa(cfg.WebPort()),
	})

	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: map[string]string{
		"name":      agentName,
		"workspace": workspace,
	}})
}

// handleAPIAgentStop stops a running ephemeral agent.
// POST /api/agent/stop  {name: "agent-coding-leo"}
func (s *Server) handleAPIAgentStop(w http.ResponseWriter, r *http.Request) {
	if s.agentMgr == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiResponse{Error: "agent manager not available"})
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

	if err := s.agentMgr.StopAgent(req.Name); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: err.Error()})
		return
	}

	// Remove from persistence
	if cfg, err := s.loadConfig(); err == nil {
		agentstore.Remove(cfg.HomePath, req.Name)
	}

	writeJSON(w, http.StatusOK, apiResponse{OK: true})
}

// handleAPIAgentList returns all running ephemeral agents.
// GET /api/agent/list
func (s *Server) handleAPIAgentList(w http.ResponseWriter, r *http.Request) {
	if s.agentMgr == nil {
		writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: map[string]interface{}{}})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: s.agentMgr.EphemeralAgents()})
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
	if s.agentMgr != nil {
		for _, a := range s.agentMgr.EphemeralAgents() {
			agents = append(agents, agentData{
				Name:      a.Name,
				Status:    a.Status,
				StartedAt: a.StartedAt,
				Restarts:  a.Restarts,
			})
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.templates.ExecuteTemplate(w, "agents.html", struct { //nolint:errcheck
		Agents    []agentData
		Templates map[string]config.TemplateConfig
	}{Agents: agents, Templates: cfg.Templates})
}

type agentData struct {
	Name      string
	Status    string
	StartedAt time.Time
	Restarts  int
}

// handleWebAgentSpawn spawns an agent via the web UI (form post).
func (s *Server) handleWebAgentSpawn(w http.ResponseWriter, r *http.Request) {
	if s.agentMgr == nil {
		s.renderFlash(w, "error", "Agent manager not available")
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

	cfg, err := s.loadConfig()
	if err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Failed to load config: %v", err))
		return
	}

	tmpl, ok := cfg.Templates[templateName]
	if !ok {
		s.renderFlash(w, "error", fmt.Sprintf("Template %q not found", templateName))
		return
	}

	workspace, agentName, err := resolveAgentWorkspace(tmpl, templateName, repo, "")
	if err != nil {
		s.renderFlash(w, "error", err.Error())
		return
	}

	// Deduplicate name
	existing := s.agentMgr.EphemeralAgents()
	if _, collision := existing[agentName]; collision {
		for i := 2; ; i++ {
			candidate := fmt.Sprintf("%s-%d", agentName, i)
			if _, ok := existing[candidate]; !ok {
				agentName = candidate
				break
			}
		}
	}

	claudeArgs := buildTemplateArgs(cfg, tmpl, agentName, workspace)
	spec := AgentSpawnRequest{
		Name:       agentName,
		ClaudeArgs: claudeArgs,
		WorkDir:    workspace,
		Env:        tmpl.Env,
		WebPort:    strconv.Itoa(cfg.WebPort()),
	}

	if err := s.agentMgr.SpawnAgent(spec); err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Failed to spawn agent: %v", err))
		return
	}

	agentstore.Save(cfg.HomePath, agentstore.Record{ //nolint:errcheck
		Name:       agentName,
		Template:   templateName,
		Workspace:  workspace,
		ClaudeArgs: claudeArgs,
		Env:        tmpl.Env,
		WebPort:    strconv.Itoa(cfg.WebPort()),
	})

	s.renderFlash(w, "success", fmt.Sprintf("Agent %q spawned — check claude.ai/code", agentName))
}

// handleWebAgentStop stops an agent via the web UI (form post).
func (s *Server) handleWebAgentStop(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if s.agentMgr == nil {
		s.renderFlash(w, "error", "Agent manager not available")
		return
	}

	if err := s.agentMgr.StopAgent(name); err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Failed to stop agent: %v", err))
		return
	}

	if cfg, err := s.loadConfig(); err == nil {
		agentstore.Remove(cfg.HomePath, name)
	}

	s.renderFlash(w, "success", fmt.Sprintf("Agent %q stopped", name))
}

// resolveAgentWorkspace determines the workspace path and agent name.
// If repo contains "/", it's treated as owner/repo and cloned if needed.
// Otherwise, the template workspace is used directly.
func resolveAgentWorkspace(tmpl config.TemplateConfig, templateName, repo, nameOverride string) (workspace, agentName string, err error) {
	baseWorkspace := tmpl.Workspace
	if baseWorkspace == "" {
		home, _ := os.UserHomeDir()
		baseWorkspace = filepath.Join(home, ".leo", "agents")
	}

	if strings.Contains(repo, "/") {
		// owner/repo format — clone if needed
		parts := strings.SplitN(repo, "/", 2)
		repoShort := parts[1]

		workspace = filepath.Join(baseWorkspace, repoShort)
		agentName = fmt.Sprintf("agent-%s-%s", templateName, repoShort)

		if nameOverride != "" {
			agentName = nameOverride
		}

		// Check if already cloned
		if _, statErr := os.Stat(filepath.Join(workspace, ".git")); statErr != nil {
			// Need to clone
			if err := os.MkdirAll(baseWorkspace, 0750); err != nil {
				return "", "", fmt.Errorf("creating workspace dir: %w", err)
			}
			ghPath, lookErr := exec.LookPath("gh")
			if lookErr != nil {
				return "", "", fmt.Errorf("gh CLI not found — install with: brew install gh")
			}
			cmd := exec.Command(ghPath, "repo", "clone", repo, workspace)
			if output, runErr := cmd.CombinedOutput(); runErr != nil {
				return "", "", fmt.Errorf("cloning %s: %s", repo, strings.TrimSpace(string(output)))
			}
		}
	} else {
		// Plain name — use template workspace directly
		workspace = baseWorkspace
		agentName = fmt.Sprintf("agent-%s-%s", templateName, repo)

		if nameOverride != "" {
			agentName = nameOverride
		}

		// Ensure workspace exists
		if err := os.MkdirAll(workspace, 0750); err != nil {
			return "", "", fmt.Errorf("creating workspace dir: %w", err)
		}
	}

	return workspace, agentName, nil
}

// buildTemplateArgs builds claude CLI arguments from a template config.
func buildTemplateArgs(cfg *config.Config, tmpl config.TemplateConfig, agentName, workspace string) []string {
	var args []string

	// Model
	model := tmpl.Model
	if model == "" {
		model = cfg.Defaults.Model
	}
	if model == "" {
		model = config.DefaultModel
	}
	args = append(args, "--model", model)

	// Channels
	for _, ch := range tmpl.Channels {
		args = append(args, "--channels", ch)
	}

	// Workspace
	args = append(args, "--add-dir", workspace)
	for _, dir := range tmpl.AddDirs {
		args = append(args, "--add-dir", dir)
	}

	// Remote control (forced on for agents, using agent name as prefix)
	rc := true
	if tmpl.RemoteControl != nil {
		rc = *tmpl.RemoteControl
	}
	if rc {
		args = append(args, "--remote-control", "--remote-control-session-name-prefix", agentName)
	}

	// Permission mode
	permMode := tmpl.PermissionMode
	if permMode == "" {
		permMode = cfg.Defaults.PermissionMode
	}
	if permMode != "" {
		args = append(args, "--permission-mode", permMode)
	}

	// MCP config
	if tmpl.MCPConfig != "" {
		mcpPath := tmpl.MCPConfig
		if !filepath.IsAbs(mcpPath) {
			mcpPath = filepath.Join(workspace, mcpPath)
		}
		if config.HasMCPServers(mcpPath) {
			args = append(args, "--mcp-config", mcpPath)
		}
	}

	// Agent
	if tmpl.Agent != "" {
		args = append(args, "--agent", tmpl.Agent)
	}

	// Allowed tools
	allowedTools := tmpl.AllowedTools
	if len(allowedTools) == 0 {
		allowedTools = cfg.Defaults.AllowedTools
	}
	if len(allowedTools) > 0 {
		args = append(args, "--allowed-tools", strings.Join(allowedTools, ","))
	}

	// Disallowed tools
	disallowedTools := tmpl.DisallowedTools
	if len(disallowedTools) == 0 {
		disallowedTools = cfg.Defaults.DisallowedTools
	}
	if len(disallowedTools) > 0 {
		args = append(args, "--disallowed-tools", strings.Join(disallowedTools, ","))
	}

	// System prompt
	appendPrompt := tmpl.AppendSystemPrompt
	if appendPrompt == "" {
		appendPrompt = cfg.Defaults.AppendSystemPrompt
	}
	if appendPrompt != "" {
		args = append(args, "--append-system-prompt", appendPrompt)
	}

	// Max turns
	maxTurns := tmpl.MaxTurns
	if maxTurns == 0 {
		maxTurns = cfg.Defaults.MaxTurns
	}
	if maxTurns == 0 {
		maxTurns = config.DefaultMaxTurns
	}
	args = append(args, "--max-turns", strconv.Itoa(maxTurns))

	return args
}

// --- Task API endpoints (JSON, used by Telegram plugin) ---

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
	if s.reloader != nil {
		if reloadErr := s.reloader.ReloadConfig(); reloadErr != nil {
			log.Printf("scheduler reload failed: %v", reloadErr)
		}
	}

	action := "enabled"
	if !task.Enabled {
		action = "disabled"
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: map[string]string{"name": name, "status": action}})
}
