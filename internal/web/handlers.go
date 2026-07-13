package web

import (
	"bytes"
	"context"
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
	"sort"
	"strings"
	"time"

	robfigcron "github.com/robfig/cron/v3"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/cron"
	"github.com/blackpaw-studio/leo/internal/harness"
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

func (s *Server) handlePartialProcesses(w http.ResponseWriter, r *http.Request) {
	data, err := s.buildDashboardData()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.templates.ExecuteTemplate(w, "processes.html", data) //nolint:errcheck
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

func (s *Server) handleProcessInterrupt(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	sessionName := agent.SessionName(name)

	tmuxPath := findTmuxPath()
	escArgs := tmux.Args("send-keys", "-t", tmux.PaneTarget(sessionName), "Escape")
	// Send Escape immediately, then keep sending to catch state transitions.
	s.execCommand(tmuxPath, escArgs...).Run() //nolint:errcheck
	s.execCommand(tmuxPath, escArgs...).Run() //nolint:errcheck
	s.execCommand(tmuxPath, escArgs...).Run() //nolint:errcheck
	// Also send delayed Escapes in background to catch tool completions
	go func() {
		for i := 0; i < 5; i++ {
			time.Sleep(500 * time.Millisecond)
			s.execCommand(tmuxPath, escArgs...).Run() //nolint:errcheck
		}
	}()
	s.renderFlash(w, "success", fmt.Sprintf("Interrupted %s", name))
}

// handleProcessRestart kills the process tmux session so the supervisor's
// restart loop respawns it with a fresh claude invocation.
func (s *Server) handleProcessRestart(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	sessionName := agent.SessionName(name)

	tmuxPath := findTmuxPath()
	if err := s.execCommand(tmuxPath, tmux.Args("kill-session", "-t", tmux.Target(sessionName))...).Run(); err != nil {
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
	sessionName := agent.SessionName(name)

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
				if err := s.execCommand(tmuxPath, tmux.Args("send-keys", "-t", tmux.PaneTarget(sessionName), string(ch))...).Run(); err != nil {
					writeJSON(w, http.StatusInternalServerError, apiResponse{Error: fmt.Sprintf("send-keys failed: %v", err)})
					return
				}
				time.Sleep(30 * time.Millisecond)
			}
			continue
		}
		if err := s.execCommand(tmuxPath, tmux.Args("send-keys", "-t", tmux.PaneTarget(sessionName), key)...).Run(); err != nil {
			writeJSON(w, http.StatusInternalServerError, apiResponse{Error: fmt.Sprintf("send-keys failed: %v", err)})
			return
		}
	}

	writeJSON(w, http.StatusOK, apiResponse{OK: true})
}

// handleProcessMessage delivers a free-text message into a process/agent's
// live Claude prompt and submits it. Unlike handleProcessSendKeys (which types
// char-by-char to drive slash-command menus), this sends the body verbatim
// with `send-keys -l` so arbitrary text — including tmux key names like
// "Enter" or "C-c" — is typed literally, then submits with a separate Enter.
//
// POST /web/process/{name}/message  {"text": "hello"}
func (s *Server) handleProcessMessage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: fmt.Sprintf("invalid request: %v", err)})
		return
	}
	if req.Text == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "text is required"})
		return
	}

	// Resolve the target's harness FIRST, before any tmux-touching logic.
	// Claude targets (harnessName == "" from an unresolved/claude target)
	// fall straight through to the existing fast-path / suspended-resume
	// logic below, byte-identical to before this change. A resolved
	// non-claude target is routed to its SessionDriver and returns
	// immediately — it never touches tmux, and never suspends (sweep skips
	// non-claude records), so there is no resume branch to consider for it.
	if harnessName, handle, ok := s.resolveMessageTarget(name); ok && harnessName != "" && harnessName != "claude" {
		s.dispatchNonClaudeMessage(w, harnessName, handle, req.Text)
		return
	}

	// Validate the target against running sessions (processes + agents).
	// If the agent is not live but is a suspended agent, resume it first and
	// deliver via the readiness-probing path (InjectPrompt) — a just-resumed
	// claude takes tens of seconds to boot before its input box accepts input,
	// so the 2s fast-path below would silently drop the message.
	//
	// NOTE: a concurrent sweep suspend can race here and make the live send-keys
	// path 500; the sender retries and auto-wakes again.
	states := s.processes.States()
	if _, ok := states[name]; !ok {
		if s.agentSvc != nil {
			rec, err := s.agentSvc.Resume(name)
			if err != nil {
				// Not a suspended agent — unknown target.
				names := make([]string, 0, len(states))
				for n := range states {
					names = append(names, n)
				}
				sort.Strings(names)
				writeJSON(w, http.StatusNotFound, apiResponse{
					Error: fmt.Sprintf("no such agent or process %q; running: %s", name, strings.Join(names, ", ")),
				})
				return
			}
			// Resumed successfully. A cold-booting claude can take ~60s to load
			// plugins/MCP before its input box accepts input — longer than the
			// server's WriteTimeout — and the readiness-probing injector blocks
			// for that whole window. Deliver asynchronously on a detached context
			// (r.Context() is cancelled once this handler returns) and respond
			// now, so the caller isn't held on the connection and won't
			// false-timeout and retry into a duplicate message.
			const wakeDeliverTimeout = 3 * time.Minute
			sessionName := agent.SessionName(rec.Name)
			body := req.Text
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), wakeDeliverTimeout)
				defer cancel()
				if err := s.injectPrompt(ctx, sessionName, body); err != nil {
					log.Printf("web: async message delivery after resume of %q failed: %v", sessionName, err)
				}
			}()
			writeJSON(w, http.StatusAccepted, apiResponse{OK: true})
			return
		}
		names := make([]string, 0, len(states))
		for n := range states {
			names = append(names, n)
		}
		sort.Strings(names)
		writeJSON(w, http.StatusNotFound, apiResponse{
			Error: fmt.Sprintf("no such agent or process %q; running: %s", name, strings.Join(names, ", ")),
		})
		return
	}

	// Live (already-running) fast path: literal paste + readiness confirmation + Enter.
	sessionName := agent.SessionName(name)
	tmuxPath := findTmuxPath()

	// Literal paste of the message body.
	if err := s.execCommand(tmuxPath, tmux.Args("send-keys", "-t", tmux.PaneTarget(sessionName), "-l", req.Text)...).Run(); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: fmt.Sprintf("send message failed: %v", err)})
		return
	}

	// Wait until the input box reflects the typed text before submitting.
	// Claude's Ink REPL batches stdin; an Enter that lands in the same input
	// burst as the literal text is treated as a newline, not a submit, leaving
	// the message unsent (the intermittent "Enter not registered" bug).
	// Confirming the text rendered forces Enter to arrive as a discrete
	// keypress. Bounded, and falls open if the pane never confirms (busy
	// mid-turn or unreadable) so a message is never silently dropped.
	s.waitForInputContent(tmuxPath, sessionName)

	// Separate Enter to submit.
	if err := s.execCommand(tmuxPath, tmux.Args("send-keys", "-t", tmux.PaneTarget(sessionName), "Enter")...).Run(); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: fmt.Sprintf("submit message failed: %v", err)})
		return
	}

	writeJSON(w, http.StatusOK, apiResponse{OK: true})
}

// resolveMessageTarget resolves name to its harness name and SessionHandle,
// trying the agent resolver first (agentstore-backed, so a name that is both
// an agent and a process resolves as the agent — consistent with the
// agent-first precedence already used elsewhere in this handler for the
// suspended-resume check) and falling back to the process resolver seam.
// ok=false means neither resolver claims the name — the caller falls back to
// the existing tmux/claude logic, which does its own "unknown target" check.
func (s *Server) resolveMessageTarget(name string) (harnessName string, h harness.SessionHandle, ok bool) {
	if s.agentSvc != nil {
		if hn, handle, resolved := s.agentSvc.ResolveHandle(name); resolved {
			return hn, handle, true
		}
	}
	if s.resolveHandle != nil {
		if hn, handle, resolved := s.resolveHandle(name); resolved {
			return hn, handle, true
		}
	}
	return "", harness.SessionHandle{}, false
}

// nonClaudeInjectTimeout bounds a non-claude driver's Inject call (a
// readiness-probed tmux paste, not a synchronous turn — Inject returns as
// soon as the message lands in the pane) so a wedged pane or hung probe loop
// can't block the web handler indefinitely. Generous because the readiness
// probe itself may need to wait out a busy TUI before it can paste.
const nonClaudeInjectTimeout = 5 * time.Minute

// dispatchNonClaudeMessage delivers text to a non-claude session via its
// SessionDriver's Inject and never touches tmux. Used by handleProcessMessage
// once the target's harness has been resolved to something other than claude.
func (s *Server) dispatchNonClaudeMessage(w http.ResponseWriter, harnessName string, h harness.SessionHandle, text string) {
	hd, err := harness.Get(harnessName)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: fmt.Sprintf("resolving harness %q: %v", harnessName, err)})
		return
	}
	drv := hd.Driver()
	if drv == nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: fmt.Sprintf("harness %q has no session driver", harnessName)})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), nonClaudeInjectTimeout)
	defer cancel()
	if _, err := drv.Inject(ctx, h, text); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: fmt.Sprintf("delivering message: %v", err)})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true})
}

// messageInputAttempts / messageInputPoll bound how long handleProcessMessage
// waits for typed text to surface in claude's input box before submitting.
// ~messageInputAttempts*messageInputPoll (≈2s) is ample for an already-running
// session to echo input; package vars so tests can shrink them.
var (
	messageInputAttempts = 40
	messageInputPoll     = 50 * time.Millisecond
)

// waitForInputContent polls the session pane until the input box carries the
// just-typed text, then returns. Falls open after the attempt budget so a busy
// or unreadable pane never blocks (or drops) the submit.
func (s *Server) waitForInputContent(tmuxPath, sessionName string) {
	for i := 0; i < messageInputAttempts; i++ {
		out, err := s.execCommand(tmuxPath, tmux.Args("capture-pane", "-p", "-t", tmux.PaneTarget(sessionName))...).Output()
		if err == nil && tmux.PaneInputHasContent(string(out)) {
			return
		}
		time.Sleep(messageInputPoll)
	}
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
	if s.scheduler != nil {
		for _, e := range s.scheduler.List() {
			cronMap[e.Name] = e
		}
	}
	nextRunName, nextRunTime := s.nextScheduledRun()

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
		Agents:        s.agentList(),
		RestartNeeded: s.restartNeeded.Load(),
		NextRunName:   nextRunName,
		NextRunTime:   nextRunTime,
	}, nil
}

// handleProcessAdd creates a bare-minimum process (name only, disabled,
// workspace left empty to inherit the default) and redirects straight to its
// edit page, where every other ProcessConfig field can be set through the
// schema-driven form. The add form is a plain (non-htmx-boosted) POST, so a
// 303 here is a normal browser redirect rather than an htmx swap. Mirrors
// handleTaskAdd.
func (s *Server) handleProcessAdd(w http.ResponseWriter, r *http.Request) {
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

	if cfg.Processes == nil {
		cfg.Processes = make(map[string]config.ProcessConfig)
	}
	if _, exists := cfg.Processes[name]; exists {
		s.renderFlash(w, "error", fmt.Sprintf("Process %q already exists", name))
		return
	}

	// Workspace left empty ("") deliberately — ProcessWorkspace/callers
	// already treat that as "inherit the default workspace", matching every
	// other schema-driven section's empty-means-inherit convention.
	cfg.Processes[name] = config.ProcessConfig{
		Enabled: false,
	}

	if errMsg := s.validateAndSave(cfg); errMsg != "" {
		s.renderFlash(w, "error", errMsg)
		return
	}
	s.reloadConfigOrWarn() // reload failures are logged server-side; nothing to attach a flash to across a redirect
	s.restartNeeded.Store(true)

	http.Redirect(w, r, "/processes/"+url.PathEscape(name), http.StatusSeeOther)
}

// handleProcessDelete removes a process and sends htmx an HX-Redirect back to
// the process list — the edit page the delete button lives on no longer has
// anything to show once the process is gone. Mirrors handleTaskDelete.
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
	s.reloadConfigOrWarn()
	s.restartNeeded.Store(true)

	w.Header().Set("HX-Redirect", "/processes")
	w.WriteHeader(http.StatusOK)
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
// handleProcessAdd already established. Mirrors handleProcessAdd.
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
// has anything to show once the template is gone. Mirrors handleProcessDelete.
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
// through validateAndSave as-is, same as handleProcessAdd/handleTemplateAdd.
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
