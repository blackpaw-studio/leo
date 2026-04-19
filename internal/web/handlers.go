package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	robfigcron "github.com/robfig/cron/v3"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/cron"
	"github.com/blackpaw-studio/leo/internal/history"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

// dashboardData is the template data for the full dashboard page.
type dashboardData struct {
	Version       string
	Processes     []processData
	Tasks         []taskData
	CronMap       map[string]cron.EntryInfo
	Config        *config.Config
	Agents        []string
	RestartNeeded bool
	StartedAt     time.Time
	NextRunName   string
	NextRunTime   time.Time
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

// logEvent represents a parsed conversation event for the log viewer template.
type logEvent struct {
	Type    string // "assistant", "user", "tool_use", "tool_result", "system", "result"
	Content string // text content or tool output
	Tool    string // tool name (for tool_use events)
	Input   string // tool input as formatted string (for tool_use events)
	Cost    string // cost (for result events)
	Turns   int    // num_turns (for result events)
}

func (s *Server) handleTaskRunLog(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	logFile := r.URL.Query().Get("file")
	if logFile == "" {
		http.Error(w, "missing file parameter", http.StatusBadRequest)
		return
	}

	// Sanitize: only allow the basename to prevent path traversal
	logFile = filepath.Base(logFile)

	cfg, err := s.loadConfig()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Validate task exists in config
	if _, ok := cfg.Tasks[name]; !ok {
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}

	// Verify the file belongs to the expected task (filename starts with task name)
	if !strings.HasPrefix(logFile, name+"-") {
		http.Error(w, "log file does not match task", http.StatusBadRequest)
		return
	}

	logPath := filepath.Join(cfg.StatePath(), "logs", logFile)
	content, err := os.ReadFile(logPath)
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(w, `<div class="log-empty">Log file not found</div>`)
		} else {
			fmt.Fprintf(w, `<div class="log-empty">Error reading log file</div>`)
		}
		return
	}

	events := parseLogEvents(content)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "task_log.html", struct {
		Name   string
		Events []logEvent
	}{Name: name, Events: events}); err != nil {
		fmt.Fprintf(os.Stderr, "error rendering task_log.html: %v\n", err)
	}
}

// unmarshalString extracts a string value from a raw JSON message.
func unmarshalString(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

// parseLogEvents converts NDJSON stream-json output into template-friendly events.
// Falls back to a single raw-content event if the log isn't valid NDJSON.
func parseLogEvents(data []byte) []logEvent {
	var events []logEvent
	parsed := false

	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}

		var raw map[string]json.RawMessage
		if json.Unmarshal(line, &raw) != nil {
			continue
		}

		evtType := unmarshalString(raw["type"])

		switch evtType {
		case "assistant":
			if evts := parseAssistantEvent(raw); len(evts) > 0 {
				events = append(events, evts...)
				parsed = true
			}

		case "user":
			if evt, ok := parseUserEvent(raw); ok {
				events = append(events, evt)
				parsed = true
			}

		case "system":
			if unmarshalString(raw["subtype"]) == "init" {
				events = append(events, logEvent{
					Type:    "system",
					Content: fmt.Sprintf("Session started (ID: %s)", unmarshalString(raw["session_id"])),
				})
				parsed = true
			}

		case "tool_result":
			var content string
			if c, ok := raw["content"]; ok {
				// content can be a string or a JSON array of content blocks
				if json.Unmarshal(c, &content) != nil {
					content = string(c)
				}
			}
			if content != "" {
				events = append(events, logEvent{Type: "tool_result", Content: content})
				parsed = true
			}

		case "result":
			if evt, ok := parseResultEvent(raw); ok {
				events = append(events, evt)
				parsed = true
			}
		}
	}

	// Fallback: not NDJSON, show raw content
	if !parsed {
		trimmed := strings.TrimSpace(string(data))
		if trimmed != "" {
			events = []logEvent{{Type: "raw", Content: trimmed}}
		}
	}

	return events
}

func parseAssistantEvent(raw map[string]json.RawMessage) []logEvent {
	msgBytes, ok := raw["message"]
	if !ok {
		return nil
	}

	var msg struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	}
	if json.Unmarshal(msgBytes, &msg) != nil {
		return nil
	}

	var events []logEvent
	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			if strings.TrimSpace(block.Text) != "" {
				events = append(events, logEvent{
					Type:    "assistant",
					Content: block.Text,
				})
			}
		case "tool_use":
			inputStr := string(block.Input)
			// Pretty-print JSON input if possible
			var pretty map[string]interface{}
			if json.Unmarshal(block.Input, &pretty) == nil {
				if formatted, err := json.MarshalIndent(pretty, "", "  "); err == nil {
					inputStr = string(formatted)
				}
			}
			events = append(events, logEvent{
				Type:  "tool_use",
				Tool:  block.Name,
				Input: inputStr,
			})
		}
	}

	return events
}

func parseUserEvent(raw map[string]json.RawMessage) (logEvent, bool) {
	msgBytes, ok := raw["message"]
	if !ok {
		return logEvent{}, false
	}

	var msg struct {
		Content []struct {
			Type    string `json:"type"`
			Text    string `json:"text"`
			Content string `json:"content"`
		} `json:"content"`
	}
	if json.Unmarshal(msgBytes, &msg) != nil {
		return logEvent{}, false
	}

	var parts []string
	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			if block.Text != "" {
				parts = append(parts, block.Text)
			}
		case "tool_result":
			// Tool results appear as user messages in Claude's API
			text := block.Content
			if text == "" {
				text = block.Text
			}
			if text != "" {
				return logEvent{Type: "tool_result", Content: text}, true
			}
		}
	}

	if len(parts) > 0 {
		return logEvent{Type: "user", Content: strings.Join(parts, "\n")}, true
	}
	return logEvent{}, false
}

func parseResultEvent(raw map[string]json.RawMessage) (logEvent, bool) {
	result := unmarshalString(raw["result"])

	var costUSD float64
	if c, ok := raw["cost_usd"]; ok {
		_ = json.Unmarshal(c, &costUSD)
	}

	var numTurns int
	if n, ok := raw["num_turns"]; ok {
		_ = json.Unmarshal(n, &numTurns)
	}

	costStr := ""
	if costUSD > 0 {
		costStr = fmt.Sprintf("$%.4f", costUSD)
	}

	return logEvent{
		Type:    "result",
		Content: result,
		Cost:    costStr,
		Turns:   numTurns,
	}, true
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

	warn := s.reloadConfigOrWarn()

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
	flashType, flashMsg := appendReloadWarning("success", fmt.Sprintf("Task %q %s", name, action), warn)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<div id="flash-container" hx-swap-oob="innerHTML:#flash-container">`)
	s.templates.ExecuteTemplate(w, "flash.html", flashData{Type: flashType, Message: flashMsg}) //nolint:errcheck
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
	warn := s.reloadConfigOrWarn()
	s.restartNeeded = true
	typ, msg := appendReloadWarning("success", "Defaults saved", warn)
	s.renderFlash(w, typ, msg)
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
	proc.DevChannels = parseCommaSeparated(r.FormValue("dev_channels"))
	proc.Agent = r.FormValue("agent")
	proc.PermissionMode = r.FormValue("permission_mode")
	proc.RemoteControl = parseOptionalBool(r.FormValue("remote_control"))
	proc.AllowedTools = parseCommaSeparated(r.FormValue("allowed_tools"))
	proc.DisallowedTools = parseCommaSeparated(r.FormValue("disallowed_tools"))
	proc.AppendSystemPrompt = r.FormValue("append_system_prompt")
	proc.MCPConfig = r.FormValue("mcp_config")
	proc.AddDirs = parseCommaSeparated(r.FormValue("add_dirs"))
	proc.Env = parseEnvMap(r.FormValue("env"))
	if mt := r.FormValue("max_turns"); mt != "" {
		v, err := strconv.Atoi(mt)
		if err != nil {
			s.renderFlash(w, "error", fmt.Sprintf("Invalid max turns: %q is not a number", mt))
			return
		}
		proc.MaxTurns = v
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
	warn := s.reloadConfigOrWarn()
	s.restartNeeded = true
	typ, msg := appendReloadWarning("success", fmt.Sprintf("Process %q saved", name), warn)
	s.renderFlash(w, typ, msg)
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
	task.Channels = parseCommaSeparated(r.FormValue("channels"))
	task.DevChannels = parseCommaSeparated(r.FormValue("dev_channels"))
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
	warn := s.reloadConfigOrWarn()
	typ, msg := appendReloadWarning("success", fmt.Sprintf("Task %q saved", name), warn)
	s.renderFlash(w, typ, msg)
}

type promptEditorData struct {
	TaskName    string
	PromptFile  string
	Files       []string
	Content     string
	NewFileName string // populated when creating a new file
}

func (s *Server) handleTaskPromptGet(w http.ResponseWriter, r *http.Request) {
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

	workspace := cfg.TaskWorkspace(task)
	files, _ := config.ListPromptFiles(workspace)

	// If a specific file was requested (e.g. from dropdown change), use that
	selectedFile := task.PromptFile
	if qf := r.URL.Query().Get("prompt_file"); qf != "" && qf != "__new__" {
		selectedFile = qf
	}

	data := promptEditorData{
		TaskName:   name,
		PromptFile: selectedFile,
		Files:      files,
	}

	if r.URL.Query().Get("prompt_file") == "__new__" {
		data.PromptFile = "__new__"
		data.NewFileName = ""
	} else if selectedFile != "" {
		content, err := config.ReadPromptFile(workspace, selectedFile)
		if err != nil {
			s.renderFlash(w, "error", fmt.Sprintf("Failed to read prompt: %v", err))
			return
		}
		data.Content = content
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.templates.ExecuteTemplate(w, "prompt_editor.html", data) //nolint:errcheck
}

func (s *Server) handleTaskPromptSave(w http.ResponseWriter, r *http.Request) {
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

	// Determine the prompt file: selected existing file, or new file name
	promptFile := r.FormValue("prompt_file")
	newFile := r.FormValue("new_file_name")
	if promptFile == "__new__" && newFile != "" {
		promptFile = newFile
	}
	if promptFile == "" || promptFile == "__new__" {
		s.renderFlash(w, "error", "No prompt file selected")
		return
	}

	// Update config if prompt_file changed
	var reloadWarn string
	if task.PromptFile != promptFile {
		task.PromptFile = promptFile
		cfg.Tasks[name] = task
		if errMsg := s.validateAndSave(cfg); errMsg != "" {
			s.renderFlash(w, "error", errMsg)
			return
		}
		reloadWarn = s.reloadConfigOrWarn()
	}

	workspace := cfg.TaskWorkspace(task)
	content := r.FormValue("prompt_content")

	if err := config.WritePromptFile(workspace, promptFile, content); err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Failed to save prompt: %v", err))
		return
	}

	typ, msg := appendReloadWarning("success", fmt.Sprintf("Prompt saved for %q", name), reloadWarn)
	s.renderFlash(w, typ, msg)
}

func (s *Server) handleProcessInterrupt(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	sessionName := "leo-" + name

	tmuxPath := findTmuxPath()
	// Send Escape immediately, then keep sending to catch state transitions.
	s.execCommand(tmuxPath, "send-keys", "-t", sessionName, "Escape").Run() //nolint:errcheck
	s.execCommand(tmuxPath, "send-keys", "-t", sessionName, "Escape").Run() //nolint:errcheck
	s.execCommand(tmuxPath, "send-keys", "-t", sessionName, "Escape").Run() //nolint:errcheck
	// Also send delayed Escapes in background to catch tool completions
	go func() {
		for i := 0; i < 5; i++ {
			time.Sleep(500 * time.Millisecond)
			s.execCommand(tmuxPath, "send-keys", "-t", sessionName, "Escape").Run() //nolint:errcheck
		}
	}()
	s.renderFlash(w, "success", fmt.Sprintf("Interrupted %s", name))
}

// handleProcessRestart kills the process tmux session so the supervisor's
// restart loop respawns it with a fresh claude invocation.
func (s *Server) handleProcessRestart(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	sessionName := "leo-" + name

	tmuxPath := findTmuxPath()
	if err := s.execCommand(tmuxPath, "kill-session", "-t", sessionName).Run(); err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Failed to restart %s: %v", name, err))
		return
	}
	s.renderFlash(w, "success", fmt.Sprintf("Restarting %s...", name))
}

// handleProcessSendKeys sends arbitrary keys/text to a process tmux session.
// POST /web/process/{name}/send  {"keys": ["/clear", "Enter"]}
//
// Multi-char literal strings (e.g. "/clear") are split into individual
// keystrokes with a small inter-key delay. Claude Code's Ink-based REPL
// treats rapid bulk send-keys as pasted text and won't activate slash-command
// menus; per-char sends make each key register as a real keypress.
func (s *Server) handleProcessSendKeys(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	sessionName := "leo-" + name

	var req struct {
		Keys []string `json:"keys"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: fmt.Sprintf("invalid request: %v", err)})
		return
	}
	if len(req.Keys) == 0 {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "keys is required"})
		return
	}

	tmuxPath := findTmuxPath()
	for _, key := range req.Keys {
		if needsCharSplit(key) {
			for _, ch := range key {
				if err := s.execCommand(tmuxPath, "send-keys", "-t", sessionName, string(ch)).Run(); err != nil {
					writeJSON(w, http.StatusInternalServerError, apiResponse{Error: fmt.Sprintf("send-keys failed: %v", err)})
					return
				}
				time.Sleep(30 * time.Millisecond)
			}
			continue
		}
		if err := s.execCommand(tmuxPath, "send-keys", "-t", sessionName, key).Run(); err != nil {
			writeJSON(w, http.StatusInternalServerError, apiResponse{Error: fmt.Sprintf("send-keys failed: %v", err)})
			return
		}
	}

	writeJSON(w, http.StatusOK, apiResponse{OK: true})
}

// needsCharSplit reports whether a send-keys arg is a multi-char literal
// string that should be typed one character at a time. Single chars and
// tmux key names (Enter, Escape, BSpace, F1, C-u, M-a, …) are sent as one
// keypress. Heuristic: key names begin with an uppercase letter, literals
// do not.
func needsCharSplit(s string) bool {
	if len(s) <= 1 {
		return false
	}
	r := rune(s[0])
	return r < 'A' || r > 'Z'
}

func findTmuxPath() string {
	// Fall back to bare "tmux" on error — preserves prior behavior for call
	// sites that pass this directly to exec.Command without checking errors.
	if p, err := tmux.Locate(); err == nil {
		return p
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

// reloadConfigOrWarn invokes the in-process scheduler reloader. It returns
// an empty string on success or when no reloader is configured. On failure
// it logs the error and returns a human-readable warning that callers
// should surface via their flash message so operators notice that the
// saved config didn't actually take effect in the scheduler.
func (s *Server) reloadConfigOrWarn() string {
	if s.reloader == nil {
		return ""
	}
	if err := s.reloader.ReloadConfig(); err != nil {
		msg := fmt.Sprintf("scheduler reload failed: %v", err)
		log.Printf("web: %s", msg)
		return msg
	}
	return ""
}

// appendReloadWarning elevates a success flash to a warning when a reload
// produced a warning, appending the warning to the original message.
// Pass-through when warn is empty, or when typ is already an error.
func appendReloadWarning(typ, msg, warn string) (string, string) {
	if warn == "" || typ == "error" {
		return typ, msg
	}
	return "warning", fmt.Sprintf("%s — %s", msg, warn)
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
		fmt.Fprintf(w, `<span class="cron-preview-error">Invalid: %s</span>`, template.HTMLEscapeString(err.Error()))
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<span class="cron-preview-desc">%s</span>`, template.HTMLEscapeString(describeCron(expr)))
	fmt.Fprintf(w, `<span class="cron-preview-times">Next: `)
	t := time.Now()
	for i := 0; i < 3; i++ {
		t = schedule.Next(t)
		if i > 0 {
			fmt.Fprintf(w, `, `)
		}
		fmt.Fprintf(w, `%s`, template.HTMLEscapeString(t.Format("Mon Jan 2 3:04 PM")))
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

// parseOptionalBool parses a three-state form value into *bool.
// "true" → &true, "false" → &false, "" → nil (inherit from defaults).
func parseOptionalBool(s string) *bool {
	switch s {
	case "true":
		v := true
		return &v
	case "false":
		v := false
		return &v
	default:
		return nil
	}
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
		Workspace:   r.FormValue("workspace"),
		Channels:    parseCommaSeparated(r.FormValue("channels")),
		DevChannels: parseCommaSeparated(r.FormValue("dev_channels")),
		Model:       r.FormValue("model"),
		Agent:       r.FormValue("agent"),
		Enabled:     r.FormValue("enabled") == "true",
	}

	if errMsg := s.validateAndSave(cfg); errMsg != "" {
		s.renderFlash(w, "error", errMsg)
		return
	}
	warn := s.reloadConfigOrWarn()

	s.restartNeeded = true

	// Re-render the processes config tab
	data, err := s.buildDashboardData()
	if err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Failed to reload: %v", err))
		return
	}
	flashType, flashMsg := appendReloadWarning("success", fmt.Sprintf("Process %q added", name), warn)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<div id="flash-container" hx-swap-oob="innerHTML:#flash-container">`)
	s.templates.ExecuteTemplate(w, "flash.html", flashData{Type: flashType, Message: flashMsg}) //nolint:errcheck
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
	warn := s.reloadConfigOrWarn()

	s.restartNeeded = true

	data, err := s.buildDashboardData()
	if err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Failed to reload: %v", err))
		return
	}
	flashType, flashMsg := appendReloadWarning("success", fmt.Sprintf("Process %q deleted", name), warn)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<div id="flash-container" hx-swap-oob="innerHTML:#flash-container">`)
	s.templates.ExecuteTemplate(w, "flash.html", flashData{Type: flashType, Message: flashMsg}) //nolint:errcheck
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

	task := config.TaskConfig{
		Schedule:   schedule,
		PromptFile: promptFile,
		Model:      r.FormValue("model"),
		Enabled:    r.FormValue("enabled") == "true",
	}
	cfg.Tasks[name] = task

	if errMsg := s.validateAndSave(cfg); errMsg != "" {
		s.renderFlash(w, "error", errMsg)
		return
	}
	reloadWarn := s.reloadConfigOrWarn()

	data, err := s.buildDashboardData()
	if err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Failed to reload: %v", err))
		return
	}

	// Warn (don't block) if the prompt file doesn't exist yet — users often
	// create the task before authoring the prompt.
	flashType := "success"
	message := fmt.Sprintf("Task %q added", name)
	if missing := promptFileMissing(cfg, task); missing != "" {
		flashType = "warning"
		message = fmt.Sprintf("Task %q added, but prompt file %s does not exist yet", name, missing)
	}
	flashType, message = appendReloadWarning(flashType, message, reloadWarn)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<div id="flash-container" hx-swap-oob="innerHTML:#flash-container">`)
	s.templates.ExecuteTemplate(w, "flash.html", flashData{Type: flashType, Message: message}) //nolint:errcheck
	fmt.Fprintf(w, `</div>`)
	s.templates.ExecuteTemplate(w, "config_tasks.html", data) //nolint:errcheck
}

// promptFileMissing returns the absolute prompt path when a task's prompt
// file does not exist on disk. Returns "" when the file exists or the path
// cannot be resolved (the caller treats resolution failures as "not missing"
// because validateAndSave already rejected invalid paths).
func promptFileMissing(cfg *config.Config, task config.TaskConfig) string {
	ws := cfg.TaskWorkspace(task)
	abs, err := config.ResolvePromptPath(ws, task.PromptFile)
	if err != nil {
		return ""
	}
	if _, err := os.Stat(abs); err != nil {
		return abs
	}
	return ""
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
	warn := s.reloadConfigOrWarn()

	data, err := s.buildDashboardData()
	if err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Failed to reload: %v", err))
		return
	}
	flashType, flashMsg := appendReloadWarning("success", fmt.Sprintf("Task %q deleted", name), warn)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<div id="flash-container" hx-swap-oob="innerHTML:#flash-container">`)
	s.templates.ExecuteTemplate(w, "flash.html", flashData{Type: flashType, Message: flashMsg}) //nolint:errcheck
	fmt.Fprintf(w, `</div>`)
	s.templates.ExecuteTemplate(w, "config_tasks.html", data) //nolint:errcheck
}

// --- Template config management ---

func (s *Server) handlePartialConfigTemplates(w http.ResponseWriter, r *http.Request) {
	data, err := s.buildDashboardData()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.templates.ExecuteTemplate(w, "config_templates.html", data) //nolint:errcheck
}

func (s *Server) handleConfigTemplate(w http.ResponseWriter, r *http.Request) {
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

	tmpl, ok := cfg.Templates[name]
	if !ok {
		s.renderFlash(w, "error", fmt.Sprintf("Template %q not found", name))
		return
	}

	tmpl.Model = r.FormValue("model")
	tmpl.Workspace = r.FormValue("workspace")
	tmpl.Channels = parseCommaSeparated(r.FormValue("channels"))
	tmpl.DevChannels = parseCommaSeparated(r.FormValue("dev_channels"))
	tmpl.Agent = r.FormValue("agent")
	tmpl.PermissionMode = r.FormValue("permission_mode")
	tmpl.RemoteControl = parseOptionalBool(r.FormValue("remote_control"))
	tmpl.AllowedTools = parseCommaSeparated(r.FormValue("allowed_tools"))
	tmpl.DisallowedTools = parseCommaSeparated(r.FormValue("disallowed_tools"))
	tmpl.AppendSystemPrompt = r.FormValue("append_system_prompt")
	tmpl.MCPConfig = r.FormValue("mcp_config")
	tmpl.AddDirs = parseCommaSeparated(r.FormValue("add_dirs"))
	tmpl.Env = parseEnvMap(r.FormValue("env"))
	if mt := r.FormValue("max_turns"); mt != "" {
		v, err := strconv.Atoi(mt)
		if err != nil {
			s.renderFlash(w, "error", fmt.Sprintf("Invalid max turns: %q is not a number", mt))
			return
		}
		tmpl.MaxTurns = v
	}
	cfg.Templates[name] = tmpl

	if errMsg := s.validateAndSave(cfg); errMsg != "" {
		s.renderFlash(w, "error", errMsg)
		return
	}
	warn := s.reloadConfigOrWarn()
	typ, msg := appendReloadWarning("success", fmt.Sprintf("Template %q saved", name), warn)
	s.renderFlash(w, typ, msg)
}

func (s *Server) handleTemplateAdd(w http.ResponseWriter, r *http.Request) {
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

	if cfg.Templates == nil {
		cfg.Templates = make(map[string]config.TemplateConfig)
	}
	if _, exists := cfg.Templates[name]; exists {
		s.renderFlash(w, "error", fmt.Sprintf("Template %q already exists", name))
		return
	}

	tmpl := config.TemplateConfig{
		Workspace:          r.FormValue("workspace"),
		Channels:           parseCommaSeparated(r.FormValue("channels")),
		DevChannels:        parseCommaSeparated(r.FormValue("dev_channels")),
		Model:              r.FormValue("model"),
		Agent:              r.FormValue("agent"),
		PermissionMode:     r.FormValue("permission_mode"),
		RemoteControl:      parseOptionalBool(r.FormValue("remote_control")),
		AllowedTools:       parseCommaSeparated(r.FormValue("allowed_tools")),
		DisallowedTools:    parseCommaSeparated(r.FormValue("disallowed_tools")),
		AppendSystemPrompt: r.FormValue("append_system_prompt"),
		MCPConfig:          r.FormValue("mcp_config"),
		AddDirs:            parseCommaSeparated(r.FormValue("add_dirs")),
		Env:                parseEnvMap(r.FormValue("env")),
	}
	if mt := r.FormValue("max_turns"); mt != "" {
		v, err := strconv.Atoi(mt)
		if err != nil {
			s.renderFlash(w, "error", fmt.Sprintf("Invalid max turns: %q is not a number", mt))
			return
		}
		tmpl.MaxTurns = v
	}
	cfg.Templates[name] = tmpl

	if errMsg := s.validateAndSave(cfg); errMsg != "" {
		s.renderFlash(w, "error", errMsg)
		return
	}
	warn := s.reloadConfigOrWarn()

	data, err := s.buildDashboardData()
	if err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Failed to reload: %v", err))
		return
	}
	flashType, flashMsg := appendReloadWarning("success", fmt.Sprintf("Template %q added", name), warn)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<div id="flash-container" hx-swap-oob="innerHTML:#flash-container">`)
	s.templates.ExecuteTemplate(w, "flash.html", flashData{Type: flashType, Message: flashMsg}) //nolint:errcheck
	fmt.Fprintf(w, `</div>`)
	s.templates.ExecuteTemplate(w, "config_templates.html", data) //nolint:errcheck
}

func (s *Server) handleTemplateDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	cfg, err := s.loadConfig()
	if err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Failed to load config: %v", err))
		return
	}

	if _, ok := cfg.Templates[name]; !ok {
		s.renderFlash(w, "error", fmt.Sprintf("Template %q not found", name))
		return
	}

	delete(cfg.Templates, name)

	if errMsg := s.validateAndSave(cfg); errMsg != "" {
		s.renderFlash(w, "error", errMsg)
		return
	}
	warn := s.reloadConfigOrWarn()

	data, err := s.buildDashboardData()
	if err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Failed to reload: %v", err))
		return
	}
	flashType, flashMsg := appendReloadWarning("success", fmt.Sprintf("Template %q deleted", name), warn)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<div id="flash-container" hx-swap-oob="innerHTML:#flash-container">`)
	s.templates.ExecuteTemplate(w, "flash.html", flashData{Type: flashType, Message: flashMsg}) //nolint:errcheck
	fmt.Fprintf(w, `</div>`)
	s.templates.ExecuteTemplate(w, "config_templates.html", data) //nolint:errcheck
}
