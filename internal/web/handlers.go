package web

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	robfigcron "github.com/robfig/cron/v3"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/cron"
	"github.com/blackpaw-studio/leo/internal/history"
)

// dashboardData is the template data for the full dashboard page.
type dashboardData struct {
	Version        string
	Processes      []processData
	Tasks          []taskData
	CronMap        map[string]cron.EntryInfo
	Config         *config.Config
	Agents         []string
	RestartNeeded  bool
	StartedAt      time.Time
	NextRunName    string
	NextRunTime  time.Time
}

type processData struct {
	Name      string
	Status    string
	StartedAt time.Time
	Restarts  int
	Enabled   bool
}

type taskData struct {
	Name     string
	Config   config.TaskConfig
	LastRun  *history.Entry
	NextRun  time.Time
	CronExpr string
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	data, err := s.buildDashboardData()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "layout.html", data); err != nil {
		fmt.Fprintf(w, "template error: %v", err)
	}
}

func (s *Server) handlePartialStatus(w http.ResponseWriter, r *http.Request) {
	data, err := s.buildDashboardData()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.templates.ExecuteTemplate(w, "status.html", data) //nolint:errcheck
}

func (s *Server) handlePartialProcesses(w http.ResponseWriter, r *http.Request) {
	data, err := s.buildDashboardData()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.templates.ExecuteTemplate(w, "processes.html", data) //nolint:errcheck
}

func (s *Server) handlePartialTasks(w http.ResponseWriter, r *http.Request) {
	data, err := s.buildDashboardData()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.templates.ExecuteTemplate(w, "tasks.html", data) //nolint:errcheck
}

func (s *Server) handlePartialConfigProcesses(w http.ResponseWriter, r *http.Request) {
	data, err := s.buildDashboardData()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.templates.ExecuteTemplate(w, "config_processes.html", data) //nolint:errcheck
}

func (s *Server) handlePartialConfigTasks(w http.ResponseWriter, r *http.Request) {
	data, err := s.buildDashboardData()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.templates.ExecuteTemplate(w, "config_tasks.html", data) //nolint:errcheck
}

func (s *Server) handlePartialConfigSettings(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.loadConfig()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.templates.ExecuteTemplate(w, "config_settings.html", cfg) //nolint:errcheck
}

func (s *Server) handlePartialTaskHistory(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	cfg, err := s.loadConfig()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	store := s.loadHistory(cfg)
	entries := store.GetAll(name)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.templates.ExecuteTemplate(w, "task_history.html", struct { //nolint:errcheck
		Name    string
		Entries []history.Entry
	}{Name: name, Entries: entries})
}

func (s *Server) handleTaskToggle(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	cfg, err := s.loadConfig()
	if err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Failed to load config: %v", err))
		return
	}

	task, ok := cfg.Tasks[name]
	if !ok {
		s.renderFlash(w, "error", fmt.Sprintf("Task %q not found", name))
		return
	}

	task.Enabled = !task.Enabled
	cfg.Tasks[name] = task

	if errMsg := s.validateAndSave(cfg); errMsg != "" {
		s.renderFlash(w, "error", errMsg)
		return
	}

	if s.reloader != nil {
		s.reloader.ReloadConfig() //nolint:errcheck
	}

	action := "enabled"
	if !task.Enabled {
		action = "disabled"
	}

	// Return updated task table
	data, err := s.buildDashboardData()
	if err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Failed to reload: %v", err))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<div id="flash-container" hx-swap-oob="innerHTML:#flash-container">`)
	s.templates.ExecuteTemplate(w, "flash.html", flashData{Type: "success", Message: fmt.Sprintf("Task %q %s", name, action)}) //nolint:errcheck
	fmt.Fprintf(w, `</div>`)
	s.templates.ExecuteTemplate(w, "tasks.html", data) //nolint:errcheck
}

func (s *Server) handleTaskRun(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	cfg, err := s.loadConfig()
	if err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Failed to load config: %v", err))
		return
	}

	if _, ok := cfg.Tasks[name]; !ok {
		s.renderFlash(w, "error", fmt.Sprintf("Task %q not found", name))
		return
	}

	// Spawn leo run as a detached subprocess
	cmd := s.execCommand(s.leoPath, "run", name, "--config", s.configPath)
	if err := cmd.Start(); err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Failed to start task: %v", err))
		return
	}

	// Don't wait for the process — it runs independently
	go cmd.Wait() //nolint:errcheck

	s.renderFlash(w, "success", fmt.Sprintf("Task %q triggered", name))
}

func (s *Server) handleConfigDefaults(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Invalid form: %v", err))
		return
	}

	cfg, err := s.loadConfig()
	if err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Failed to load config: %v", err))
		return
	}

	if model := r.FormValue("model"); model != "" {
		cfg.Defaults.Model = model
	}
	if mt := r.FormValue("max_turns"); mt != "" {
		if v, err := strconv.Atoi(mt); err == nil && v > 0 {
			cfg.Defaults.MaxTurns = v
		}
	}
	cfg.Defaults.PermissionMode = r.FormValue("permission_mode")
	cfg.Defaults.AllowedTools = parseCommaSeparated(r.FormValue("allowed_tools"))
	cfg.Defaults.DisallowedTools = parseCommaSeparated(r.FormValue("disallowed_tools"))
	cfg.Defaults.AppendSystemPrompt = r.FormValue("append_system_prompt")

	if errMsg := s.validateAndSave(cfg); errMsg != "" {
		s.renderFlash(w, "error", errMsg)
		return
	}
	if s.reloader != nil {
		s.reloader.ReloadConfig() //nolint:errcheck
	}
	s.restartNeeded = true
	s.renderFlash(w, "success", "Defaults saved — restart needed for processes to pick up changes")
}

func (s *Server) handleConfigProcess(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := r.ParseForm(); err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Invalid form: %v", err))
		return
	}

	cfg, err := s.loadConfig()
	if err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Failed to load config: %v", err))
		return
	}

	proc, ok := cfg.Processes[name]
	if !ok {
		s.renderFlash(w, "error", fmt.Sprintf("Process %q not found", name))
		return
	}

	proc.Enabled = r.FormValue("enabled") == "true"
	proc.Model = r.FormValue("model")
	proc.Workspace = r.FormValue("workspace")
	proc.Channels = parseCommaSeparated(r.FormValue("channels"))
	proc.Agent = r.FormValue("agent")
	proc.PermissionMode = r.FormValue("permission_mode")
	rc := r.FormValue("remote_control") == "true"
	proc.RemoteControl = &rc
	proc.AllowedTools = parseCommaSeparated(r.FormValue("allowed_tools"))
	proc.DisallowedTools = parseCommaSeparated(r.FormValue("disallowed_tools"))
	proc.AppendSystemPrompt = r.FormValue("append_system_prompt")
	proc.MCPConfig = r.FormValue("mcp_config")
	proc.AddDirs = parseCommaSeparated(r.FormValue("add_dirs"))
	proc.Env = parseEnvMap(r.FormValue("env"))
	if mt := r.FormValue("max_turns"); mt != "" {
		if v, err := strconv.Atoi(mt); err == nil {
			proc.MaxTurns = v
		}
	}
	// Clear bypass_permissions if permission_mode is set (permission_mode takes precedence)
	if proc.PermissionMode != "" {
		proc.BypassPermissions = nil
	} else {
		bp := r.FormValue("bypass_permissions") == "true"
		proc.BypassPermissions = &bp
	}
	cfg.Processes[name] = proc

	if errMsg := s.validateAndSave(cfg); errMsg != "" {
		s.renderFlash(w, "error", errMsg)
		return
	}
	if s.reloader != nil {
		s.reloader.ReloadConfig() //nolint:errcheck
	}
	s.restartNeeded = true
	s.renderFlash(w, "success", fmt.Sprintf("Process %q saved — restart needed", name))
}

func (s *Server) handleConfigTask(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := r.ParseForm(); err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Invalid form: %v", err))
		return
	}

	cfg, err := s.loadConfig()
	if err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Failed to load config: %v", err))
		return
	}

	task, ok := cfg.Tasks[name]
	if !ok {
		s.renderFlash(w, "error", fmt.Sprintf("Task %q not found", name))
		return
	}

	task.Enabled = r.FormValue("enabled") == "true"
	if sched := r.FormValue("schedule"); sched != "" {
		task.Schedule = sched
	}
	if pf := r.FormValue("prompt_file"); pf != "" {
		task.PromptFile = pf
	}
	task.Model = r.FormValue("model")
	if mt := r.FormValue("max_turns"); mt != "" {
		if v, err := strconv.Atoi(mt); err == nil {
			task.MaxTurns = v
		}
	}
	if to := r.FormValue("timeout"); to != "" {
		task.Timeout = to
	}
	task.Silent = r.FormValue("silent") == "true"
	task.NotifyOnFail = r.FormValue("notify_on_fail") == "true"
	task.Timezone = r.FormValue("timezone")
	task.Workspace = r.FormValue("workspace")
	if tid := r.FormValue("topic_id"); tid != "" {
		if v, err := strconv.Atoi(tid); err == nil {
			task.TopicID = v
		}
	}
	if ret := r.FormValue("retries"); ret != "" {
		if v, err := strconv.Atoi(ret); err == nil {
			task.Retries = v
		}
	}
	task.PermissionMode = r.FormValue("permission_mode")
	task.AllowedTools = parseCommaSeparated(r.FormValue("allowed_tools"))
	task.DisallowedTools = parseCommaSeparated(r.FormValue("disallowed_tools"))
	task.AppendSystemPrompt = r.FormValue("append_system_prompt")
	cfg.Tasks[name] = task

	if errMsg := s.validateAndSave(cfg); errMsg != "" {
		s.renderFlash(w, "error", errMsg)
		return
	}
	if s.reloader != nil {
		s.reloader.ReloadConfig() //nolint:errcheck
	}
	s.renderFlash(w, "success", fmt.Sprintf("Task %q saved", name))
}

func (s *Server) handleProcessInterrupt(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	sessionName := "leo-" + name

	tmuxPath := findTmuxPath()
	// kill-pane terminates all processes in the pane (Claude + tool subprocesses).
	// The supervisor will detect the exit and restart the process.
	if err := s.execCommand(tmuxPath, "kill-pane", "-t", sessionName).Run(); err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Failed to interrupt %s: %v", name, err))
		return
	}
	s.renderFlash(w, "success", fmt.Sprintf("Interrupted %s", name))
}

func findTmuxPath() string {
	if p, err := exec.LookPath("tmux"); err == nil {
		return p
	}
	for _, p := range []string{"/opt/homebrew/bin/tmux", "/usr/local/bin/tmux", "/usr/bin/tmux"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "tmux"
}

func (s *Server) handleServiceRestart(w http.ResponseWriter, r *http.Request) {
	cmd := s.execCommand(s.leoPath, "service", "restart", "--config", s.configPath)
	if err := cmd.Start(); err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Failed to restart: %v", err))
		return
	}
	go cmd.Wait() //nolint:errcheck

	s.restartNeeded = false
	s.renderFlash(w, "success", "Service restarting...")
}

func (s *Server) handleConfigReload(w http.ResponseWriter, r *http.Request) {
	if s.reloader != nil {
		if err := s.reloader.ReloadConfig(); err != nil {
			s.renderFlash(w, "error", fmt.Sprintf("Reload failed: %v", err))
			return
		}
	}
	s.renderFlash(w, "success", "Config reloaded successfully")
}

type flashData struct {
	Type    string // "success" or "error"
	Message string
}

func (s *Server) renderFlash(w http.ResponseWriter, typ, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.templates.ExecuteTemplate(w, "flash.html", flashData{Type: typ, Message: msg}) //nolint:errcheck
}

// validateAndSave validates the config and saves it. Returns an error message for the user, or empty on success.
func (s *Server) validateAndSave(cfg *config.Config) string {
	if err := cfg.Validate(); err != nil {
		return err.Error()
	}
	if err := config.Save(s.configPath, cfg); err != nil {
		return fmt.Sprintf("Failed to save: %v", err)
	}
	return ""
}

func (s *Server) handleCronPreview(w http.ResponseWriter, r *http.Request) {
	expr := r.URL.Query().Get("expr")
	if expr == "" {
		return
	}

	parser := robfigcron.NewParser(robfigcron.Minute | robfigcron.Hour | robfigcron.Dom | robfigcron.Month | robfigcron.Dow)
	schedule, err := parser.Parse(expr)
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<span class="cron-preview-error">Invalid: %s</span>`, err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<span class="cron-preview-desc">%s</span>`, describeCron(expr))
	fmt.Fprintf(w, `<span class="cron-preview-times">Next: `)
	t := time.Now()
	for i := 0; i < 3; i++ {
		t = schedule.Next(t)
		if i > 0 {
			fmt.Fprintf(w, `, `)
		}
		fmt.Fprintf(w, `%s`, t.Format("Mon Jan 2 3:04 PM"))
	}
	fmt.Fprintf(w, `</span>`)
}

func parseCommaSeparated(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

func parseEnvMap(s string) map[string]string {
	if s == "" {
		return nil
	}
	result := make(map[string]string)
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			result[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func (s *Server) buildDashboardData() (*dashboardData, error) {
	cfg, err := s.loadConfig()
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}

	// Process states
	var processes []processData
	states := make(map[string]ProcessStateInfo)
	if s.processes != nil {
		states = s.processes.States()
	}
	for name, proc := range cfg.Processes {
		pd := processData{
			Name:    name,
			Enabled: proc.Enabled,
			Status:  "stopped",
		}
		if state, ok := states[name]; ok {
			pd.Status = state.Status
			pd.StartedAt = state.StartedAt
			pd.Restarts = state.Restarts
		}
		if !proc.Enabled {
			pd.Status = "disabled"
		}
		processes = append(processes, pd)
	}
	sort.Slice(processes, func(i, j int) bool {
		return processes[i].Name < processes[j].Name
	})

	// Cron entries + find earliest next run
	cronMap := make(map[string]cron.EntryInfo)
	var nextRunName string
	var nextRunTime time.Time
	if s.scheduler != nil {
		for _, e := range s.scheduler.List() {
			cronMap[e.Name] = e
			if nextRunTime.IsZero() || e.Next.Before(nextRunTime) {
				nextRunTime = e.Next
				nextRunName = e.Name
			}
		}
	}

	// Tasks with history
	store := s.loadHistory(cfg)
	var tasks []taskData
	for name, task := range cfg.Tasks {
		td := taskData{
			Name:     name,
			Config:   task,
			LastRun:  store.Get(name),
			CronExpr: task.Schedule,
		}
		if entry, ok := cronMap[name]; ok {
			td.NextRun = entry.Next
		}
		tasks = append(tasks, td)
	}
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].Name < tasks[j].Name
	})

	return &dashboardData{
		Processes:     processes,
		Tasks:         tasks,
		CronMap:       cronMap,
		Config:        cfg,
		Agents:        s.agents,
		RestartNeeded: s.restartNeeded,
		NextRunName:   nextRunName,
		NextRunTime:   nextRunTime,
	}, nil
}

func (s *Server) handleProcessAdd(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Invalid form: %v", err))
		return
	}

	name := r.FormValue("name")
	if name == "" {
		s.renderFlash(w, "error", "Name is required")
		return
	}

	cfg, err := s.loadConfig()
	if err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Failed to load config: %v", err))
		return
	}

	if cfg.Processes == nil {
		cfg.Processes = make(map[string]config.ProcessConfig)
	}
	if _, exists := cfg.Processes[name]; exists {
		s.renderFlash(w, "error", fmt.Sprintf("Process %q already exists", name))
		return
	}

	cfg.Processes[name] = config.ProcessConfig{
		Workspace:          r.FormValue("workspace"),
		Channels:           parseCommaSeparated(r.FormValue("channels")),
		Model:              r.FormValue("model"),
		Agent:              r.FormValue("agent"),
		Enabled:            r.FormValue("enabled") == "true",
	}

	if errMsg := s.validateAndSave(cfg); errMsg != "" {
		s.renderFlash(w, "error", errMsg)
		return
	}
	if s.reloader != nil {
		s.reloader.ReloadConfig() //nolint:errcheck
	}

	s.restartNeeded = true

	// Re-render the processes config tab
	data, err := s.buildDashboardData()
	if err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Failed to reload: %v", err))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<div id="flash-container" hx-swap-oob="innerHTML:#flash-container">`)
	s.templates.ExecuteTemplate(w, "flash.html", flashData{Type: "success", Message: fmt.Sprintf("Process %q added — restart needed", name)}) //nolint:errcheck
	fmt.Fprintf(w, `</div>`)
	s.templates.ExecuteTemplate(w, "config_processes.html", data) //nolint:errcheck
}

func (s *Server) handleProcessDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	cfg, err := s.loadConfig()
	if err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Failed to load config: %v", err))
		return
	}

	if _, ok := cfg.Processes[name]; !ok {
		s.renderFlash(w, "error", fmt.Sprintf("Process %q not found", name))
		return
	}

	delete(cfg.Processes, name)

	if errMsg := s.validateAndSave(cfg); errMsg != "" {
		s.renderFlash(w, "error", errMsg)
		return
	}
	if s.reloader != nil {
		s.reloader.ReloadConfig() //nolint:errcheck
	}

	s.restartNeeded = true

	data, err := s.buildDashboardData()
	if err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Failed to reload: %v", err))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<div id="flash-container" hx-swap-oob="innerHTML:#flash-container">`)
	s.templates.ExecuteTemplate(w, "flash.html", flashData{Type: "success", Message: fmt.Sprintf("Process %q deleted — restart needed", name)}) //nolint:errcheck
	fmt.Fprintf(w, `</div>`)
	s.templates.ExecuteTemplate(w, "config_processes.html", data) //nolint:errcheck
}

func (s *Server) handleTaskAdd(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Invalid form: %v", err))
		return
	}

	name := r.FormValue("name")
	if name == "" {
		s.renderFlash(w, "error", "Name is required")
		return
	}
	schedule := r.FormValue("schedule")
	if schedule == "" {
		s.renderFlash(w, "error", "Schedule is required")
		return
	}
	promptFile := r.FormValue("prompt_file")
	if promptFile == "" {
		s.renderFlash(w, "error", "Prompt file is required")
		return
	}

	cfg, err := s.loadConfig()
	if err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Failed to load config: %v", err))
		return
	}

	if cfg.Tasks == nil {
		cfg.Tasks = make(map[string]config.TaskConfig)
	}
	if _, exists := cfg.Tasks[name]; exists {
		s.renderFlash(w, "error", fmt.Sprintf("Task %q already exists", name))
		return
	}

	cfg.Tasks[name] = config.TaskConfig{
		Schedule:   schedule,
		PromptFile: promptFile,
		Model:      r.FormValue("model"),
		Enabled:    r.FormValue("enabled") == "true",
	}

	if errMsg := s.validateAndSave(cfg); errMsg != "" {
		s.renderFlash(w, "error", errMsg)
		return
	}
	if s.reloader != nil {
		s.reloader.ReloadConfig() //nolint:errcheck
	}

	data, err := s.buildDashboardData()
	if err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Failed to reload: %v", err))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<div id="flash-container" hx-swap-oob="innerHTML:#flash-container">`)
	s.templates.ExecuteTemplate(w, "flash.html", flashData{Type: "success", Message: fmt.Sprintf("Task %q added", name)}) //nolint:errcheck
	fmt.Fprintf(w, `</div>`)
	s.templates.ExecuteTemplate(w, "config_tasks.html", data) //nolint:errcheck
}

func (s *Server) handleTaskDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	cfg, err := s.loadConfig()
	if err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Failed to load config: %v", err))
		return
	}

	if _, ok := cfg.Tasks[name]; !ok {
		s.renderFlash(w, "error", fmt.Sprintf("Task %q not found", name))
		return
	}

	delete(cfg.Tasks, name)

	if errMsg := s.validateAndSave(cfg); errMsg != "" {
		s.renderFlash(w, "error", errMsg)
		return
	}
	if s.reloader != nil {
		s.reloader.ReloadConfig() //nolint:errcheck
	}

	data, err := s.buildDashboardData()
	if err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Failed to reload: %v", err))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<div id="flash-container" hx-swap-oob="innerHTML:#flash-container">`)
	s.templates.ExecuteTemplate(w, "flash.html", flashData{Type: "success", Message: fmt.Sprintf("Task %q deleted", name)}) //nolint:errcheck
	fmt.Fprintf(w, `</div>`)
	s.templates.ExecuteTemplate(w, "config_tasks.html", data) //nolint:errcheck
}
