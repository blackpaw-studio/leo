package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	robfigcron "github.com/robfig/cron/v3"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/history"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

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
			var pretty map[string]any
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

	// HX-Refresh triggers a full page reload, so the list's button
	// label/next-run/state all pick up the new value — the same convention
	// handleHostAdd/handleSessionAdd use. That makes the response body
	// itself moot (the page reloads before it's swapped in), but render the
	// flash normally anyway for tests/non-htmx callers.
	w.Header().Set("HX-Refresh", "true")
	flashType, flashMsg := appendReloadWarning("success", fmt.Sprintf("Task %q %s", name, action), warn)
	s.renderFlash(w, flashType, flashMsg)
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

type promptEditorData struct {
	TaskName    string
	PromptFile  string
	Files       []string
	Content     string
	NewFileName string // populated when creating a new file
}

// buildPromptEditorData assembles components/prompt_editor.html's data for a
// task: the list of files in its workspace and the selected file's content.
// selected overrides task.PromptFile (e.g. the prompt-file dropdown's change
// event); pass "" to show the task's configured file, or "__new__" for the
// blank new-file state. Shared by the standalone prompt-editor endpoint
// (handleTaskPromptGet) and the task edit page so the file-reading logic
// exists in exactly one place.
func (s *Server) buildPromptEditorData(cfg *config.Config, name string, task config.TaskConfig, selected string) (promptEditorData, error) {
	workspace := cfg.TaskWorkspace(task)
	files, _ := config.ListPromptFiles(workspace)

	if selected == "__new__" {
		return promptEditorData{TaskName: name, PromptFile: "__new__", Files: files}, nil
	}

	file := task.PromptFile
	if selected != "" {
		file = selected
	}

	data := promptEditorData{TaskName: name, PromptFile: file, Files: files}
	if file != "" {
		content, err := config.ReadPromptFile(workspace, file)
		if err != nil {
			return promptEditorData{}, err
		}
		data.Content = content
	}
	return data, nil
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

	data, err := s.buildPromptEditorData(cfg, name, task, r.URL.Query().Get("prompt_file"))
	if err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Failed to read prompt: %v", err))
		return
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

	s.restartNeeded.Store(false)
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

// entityNamePattern restricts config entity names (tasks, processes,
// templates, providers, hosts, sessions) to a safe, URL- and filesystem-path
// friendly character set. Without this, a name containing "/", "#", "?", or
// similar creates entries no route can address (e.g. /web/task/{name}/delete
// splits on an embedded "/") and, worse, task names flow straight into a
// prompt file path (prompts/<name>.md in handleTaskAdd) where "../x" would
// escape the workspace.
var entityNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// validEntityName reports whether name is safe to use as a config map key
// that also gets embedded in URLs and filesystem paths. It must be non-empty,
// match entityNamePattern, and not be the literal "." or ".." (both match
// the pattern above but are directory-traversal special cases).
func validEntityName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	return entityNamePattern.MatchString(name)
}

// entityNameError is the flash message shown when validEntityName rejects a
// submitted name, shared by every add handler so the guidance stays
// consistent.
const entityNameError = "Name may contain only letters, digits, dot, underscore, dash"

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

// handleTaskAdd creates a bare-minimum task (name + schedule) and redirects
// straight to its edit page, where every other TaskConfig field can be set
// through the schema-driven form. The prompt file is auto-named and starts
// out non-existent — authored from the edit page's prompt editor — and the
// task starts disabled so an incomplete config never fires on a cron tick.
// The add form is a plain (non-htmx-boosted) POST, so a 303 here is a normal
// browser redirect rather than an htmx swap.
func (s *Server) handleTaskAdd(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Invalid form: %v", err))
		return
	}

	name := r.FormValue("name")
	if !validEntityName(name) {
		s.renderFlash(w, "error", entityNameError)
		return
	}
	schedule := r.FormValue("schedule")
	if schedule == "" {
		s.renderFlash(w, "error", "Schedule is required")
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
		PromptFile: "prompts/" + name + ".md",
		Enabled:    false,
	}

	if errMsg := s.validateAndSave(cfg); errMsg != "" {
		s.renderFlash(w, "error", errMsg)
		return
	}
	s.reloadConfigOrWarn() // reload failures are logged server-side; nothing to attach a flash to across a redirect

	http.Redirect(w, r, "/tasks/"+url.PathEscape(name), http.StatusSeeOther)
}

// handleTaskDelete removes a task and sends htmx an HX-Redirect back to the
// task list — the edit page the delete button lives on no longer has
// anything to show once the task is gone.
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
	s.reloadConfigOrWarn()

	w.Header().Set("HX-Redirect", "/tasks")
	w.WriteHeader(http.StatusOK)
}

// --- Template config management ---

// handleTemplateAdd creates a bare-minimum template (name only) and redirects
// straight to its edit page, where every other TemplateConfig field can be
// set through the schema-driven form. Workspace is left empty ("") to
// inherit the default workspace, matching the empty-means-inherit convention
// used elsewhere in this file (e.g. handleHostAdd below).
func (s *Server) handleTemplateAdd(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Invalid form: %v", err))
		return
	}

	name := r.FormValue("name")
	if !validEntityName(name) {
		s.renderFlash(w, "error", entityNameError)
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

	cfg.Templates[name] = config.TemplateConfig{}

	if errMsg := s.validateAndSave(cfg); errMsg != "" {
		s.renderFlash(w, "error", errMsg)
		return
	}
	s.reloadConfigOrWarn() // reload failures are logged server-side; nothing to attach a flash to across a redirect

	http.Redirect(w, r, "/config/templates/"+url.PathEscape(name), http.StatusSeeOther)
}

// handleTemplateDelete removes a template and sends htmx an HX-Redirect back
// to the template list — the edit page the delete button lives on no longer
// has anything to show once the template is gone.
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
	s.reloadConfigOrWarn()

	w.Header().Set("HX-Redirect", "/config/templates")
	w.WriteHeader(http.StatusOK)
}

// --- Remote host config management ---
//
// Hosts live on the Settings page (config_settings.html) as a card list
// plus name-only add form (mirrors the retired providers page's shape).

// handleHostAdd creates a new remote-host entry and reports back on the
// settings page itself (HX-Refresh, not a redirect to a separate edit page).
// HostConfig has no fields Config.Validate() requires to be non-empty (no
// ssh-non-empty check exists), so a genuinely empty HostConfig{} round-trips
// through validateAndSave as-is, same as handleTemplateAdd.
// The flash message still tells the operator to fill in ssh via the card's
// inline form before the host is usable.
func (s *Server) handleHostAdd(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Invalid form: %v", err))
		return
	}

	name := r.FormValue("name")
	if !validEntityName(name) {
		s.renderFlash(w, "error", entityNameError)
		return
	}

	cfg, err := s.loadConfig()
	if err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Failed to load config: %v", err))
		return
	}

	if cfg.Client.Hosts == nil {
		cfg.Client.Hosts = make(map[string]config.HostConfig)
	}
	if _, exists := cfg.Client.Hosts[name]; exists {
		s.renderFlash(w, "error", fmt.Sprintf("Host %q already exists", name))
		return
	}

	cfg.Client.Hosts[name] = config.HostConfig{}

	if errMsg := s.validateAndSave(cfg); errMsg != "" {
		s.renderFlash(w, "error", errMsg)
		return
	}
	s.reloadConfigOrWarn()

	w.Header().Set("HX-Refresh", "true")
	s.renderFlash(w, "success", fmt.Sprintf("Host %q created — set its ssh target below", name))
}

// handleHostDelete removes a remote host and reports back on the settings
// page (HX-Refresh, not a redirect to a separate edit page). No
// reference-check scan runs here: nothing in the config schema references a
// host by name — client.default_host is a free-text hint for `leo agent`
// CLI dispatch, not a validated foreign key — so an optimistic delete is
// safe.
func (s *Server) handleHostDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	cfg, err := s.loadConfig()
	if err != nil {
		s.renderFlash(w, "error", fmt.Sprintf("Failed to load config: %v", err))
		return
	}

	if _, ok := cfg.Client.Hosts[name]; !ok {
		s.renderFlash(w, "error", fmt.Sprintf("Host %q not found", name))
		return
	}

	delete(cfg.Client.Hosts, name)

	if errMsg := s.validateAndSave(cfg); errMsg != "" {
		s.renderFlash(w, "error", errMsg)
		return
	}
	s.reloadConfigOrWarn()

	w.Header().Set("HX-Refresh", "true")
	s.renderFlash(w, "success", fmt.Sprintf("Host %q deleted", name))
}
