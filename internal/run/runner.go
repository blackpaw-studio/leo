package run

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/history"
	"github.com/blackpaw-studio/leo/internal/session"
)

var execCommand = exec.Command

const silentPreamble = `SILENT SCHEDULED RUN — You are running as a scheduled background task, not responding to a user message.
Work silently. Do not narrate your process or describe your tool usage.
When finished:
- If there is something the user needs to see, deliver ONLY the final user-facing message via a configured channel plugin (see $LEO_CHANNELS).
- If there is nothing to report, or no channel plugin is configured, output exactly: NO_REPLY
Do not include status updates, tool output, or process descriptions.
`

// notifyFailureTimeout bounds the notify-on-fail child invocation so a
// failing task doesn't cascade into an unbounded second run.
const notifyFailureTimeout = 60 * time.Second

// claudeResult is the minimal structure for parsing the final "result" event
// from claude --output-format stream-json (newline-delimited JSON).
type claudeResult struct {
	SessionID string `json:"session_id"`
	Result    string `json:"result"`
	IsError   bool   `json:"is_error"`
}

// streamEvent represents a single event line from stream-json output.
type streamEvent struct {
	Type string `json:"type"`
	claudeResult
}

// resolveTask looks up a task by name.
func resolveTask(cfg *config.Config, taskName string) (config.TaskConfig, error) {
	if task, ok := cfg.Tasks[taskName]; ok {
		return task, nil
	}
	return config.TaskConfig{}, fmt.Errorf("task %q not found in config", taskName)
}

// Preview returns the assembled prompt and CLI args without executing.
func Preview(cfg *config.Config, taskName string, sessions *session.Store) (string, []string, error) {
	task, err := resolveTask(cfg, taskName)
	if err != nil {
		return "", nil, err
	}

	prompt, err := assemblePrompt(cfg, task)
	if err != nil {
		return "", nil, fmt.Errorf("assembling prompt: %w", err)
	}

	var sessionID string
	if sessions != nil {
		sid, _, getErr := sessions.Get("task:" + taskName)
		if getErr != nil {
			fmt.Fprintf(os.Stderr, "warning: could not read session store: %v\n", getErr)
		}
		sessionID = sid
	}

	args := buildArgs(cfg, task, prompt, sessionID)
	return prompt, args, nil
}

// Run executes a scheduled task.
func Run(cfg *config.Config, taskName string, sessions *session.Store) error {
	task, err := resolveTask(cfg, taskName)
	if err != nil {
		return err
	}

	// Acquire task lock to prevent concurrent execution
	lockPath := filepath.Join(cfg.StatePath(), taskName+".lock")
	if err := acquireTaskLock(lockPath); err != nil {
		return fmt.Errorf("task %q is already running: %w", taskName, err)
	}
	defer releaseTaskLock(lockPath)

	prompt, err := assemblePrompt(cfg, task)
	if err != nil {
		return fmt.Errorf("assembling prompt: %w", err)
	}

	var sessionID string
	if sessions != nil {
		sid, _, getErr := sessions.Get("task:" + taskName)
		if getErr != nil {
			fmt.Fprintf(os.Stderr, "warning: could not read session store: %v\n", getErr)
		}
		sessionID = sid
	}

	timeout := cfg.TaskTimeout(task)
	taskWorkspace := cfg.TaskWorkspace(task)

	maxAttempts := task.Retries + 1
	var lastErr error
	var lastOutput []byte
	var lastLogContent string

	var timedOut bool
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			fmt.Fprintf(os.Stderr, "retrying task %q (attempt %d/%d)\n", taskName, attempt, maxAttempts)
			// Clear session for retry attempts
			sessionID = ""
		}

		args := buildArgs(cfg, task, prompt, sessionID)

		// Per-attempt timeout so each retry gets the full timeout
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		output, execErr := executeCommand(ctx, taskWorkspace, args, task.Channels, task.DevChannels)
		result := parseClaudeOutput(output)
		timedOut = ctx.Err() == context.DeadlineExceeded

		// If --resume failed with a stale session, retry without it.
		if execErr != nil && sessionID != "" && isSessionError(result, output) {
			if sessions != nil {
				if delErr := sessions.Delete("task:" + taskName); delErr != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to clear stale session: %v\n", delErr)
				}
			}

			args = buildArgs(cfg, task, prompt, "")
			output, execErr = executeCommand(ctx, taskWorkspace, args, task.Channels, task.DevChannels)
			result = parseClaudeOutput(output)
		}

		// Store session ID for next run
		if sessions != nil && result.SessionID != "" {
			if setErr := sessions.Set("task:"+taskName, result.SessionID); setErr != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to store session ID: %v\n", setErr)
			}
		}

		// Capture full stream output for logging (includes all conversation events)
		lastLogContent = string(output)

		cancel()

		if execErr == nil {
			lastErr = nil
			lastOutput = nil
			break
		}

		lastErr = execErr
		lastOutput = output
	}

	// Write log for the final attempt only (avoids orphaned files on retries)
	logFile := logFileName(taskName)
	if logErr := writeLogFile(cfg, logFile, []byte(lastLogContent)); logErr != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to write log: %v\n", logErr)
		logFile = ""
	}

	// Record execution history
	exitCode := 0
	reason := history.ReasonSuccess
	if lastErr != nil {
		exitCode = 1
		if timedOut {
			reason = history.ReasonTimeout
		} else {
			reason = history.ReasonFailure
		}
	}
	hist := history.NewStore(cfg.HomePath)
	if histErr := hist.Record(taskName, exitCode, reason, logFile); histErr != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to record history: %v\n", histErr)
	}

	// Send failure notification if configured (via child claude invocation)
	if lastErr != nil && task.NotifyOnFail && len(task.Channels) > 0 {
		notifyFailure(taskName, task, taskWorkspace, lastErr, maxAttempts)
	}

	if lastErr != nil {
		return fmt.Errorf("claude exited with error: %w\nOutput: %s", lastErr, string(lastOutput))
	}

	return nil
}

// notifyFailure spawns a short, bounded claude invocation that asks the agent
// to deliver a failure notification via one of the task's configured channel
// plugins. All errors are logged and swallowed so notify failures don't cascade
// back to the parent task.
func notifyFailure(taskName string, task config.TaskConfig, workspace string, taskErr error, attempts int) {
	prompt := fmt.Sprintf(
		"Task %q failed after %d attempt(s): %v.\n"+
			"Use a messaging tool from one of your configured channel plugins (see $LEO_CHANNELS) "+
			"to deliver this failure notification to the user. Keep it concise.\n"+
			"When done, reply NO_REPLY.",
		taskName, attempts, taskErr,
	)

	args := []string{
		"-p", prompt,
		"--max-turns", "3",
		"--permission-mode", "acceptEdits",
		"--output-format", "text",
	}
	for _, ch := range task.DevChannels {
		args = append(args, "--dangerously-load-development-channels", ch)
	}

	ctx, cancel := context.WithTimeout(context.Background(), notifyFailureTimeout)
	defer cancel()

	if _, err := executeCommand(ctx, workspace, args, task.Channels, task.DevChannels); err != nil {
		fmt.Fprintf(os.Stderr, "warning: notify-on-fail child invocation failed: %v\n", err)
	}
}

// isSessionError checks whether a claude failure was caused by an invalid/stale session.
func isSessionError(result claudeResult, output []byte) bool {
	text := strings.ToLower(result.Result)
	if text == "" {
		text = strings.ToLower(string(output))
	}
	return strings.Contains(text, "session") &&
		(strings.Contains(text, "not found") || strings.Contains(text, "invalid") || strings.Contains(text, "expired"))
}

func executeCommand(ctx context.Context, workDir string, args []string, channels, devChannels []string) ([]byte, error) {
	cmd := execCommand("claude", args...)
	cmd.Dir = workDir
	env := append(os.Environ(), "CLAUDE_CODE_ENTRYPOINT=cli")
	if len(channels) > 0 {
		env = append(env, "LEO_CHANNELS="+strings.Join(channels, ","))
	}
	if len(devChannels) > 0 {
		env = append(env, "LEO_DEV_CHANNELS="+strings.Join(devChannels, ","))
	}
	cmd.Env = env

	// Use a done channel to coordinate context cancellation with process lifecycle.
	// Start the process explicitly so we can kill it on timeout.
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stdout

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	// Monitor context in background; kill process if deadline expires
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			cmd.Process.Kill()
		case <-done:
		}
	}()

	err := cmd.Wait()
	close(done) // stop the monitor goroutine

	if ctx.Err() != nil {
		return stdout.Bytes(), ctx.Err()
	}
	return stdout.Bytes(), err
}

// parseClaudeOutput extracts the final result from stream-json (NDJSON) output.
// It scans for the last line with "type":"result" to get session_id and result text.
// Falls back to single-object JSON parsing for backwards compatibility.
func parseClaudeOutput(output []byte) claudeResult {
	var best claudeResult
	for _, line := range bytes.Split(output, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var evt streamEvent
		if json.Unmarshal(line, &evt) == nil && evt.Type == "result" {
			best = evt.claudeResult
		}
	}
	if best.SessionID != "" || best.Result != "" {
		return best
	}
	// Fallback: try parsing as a single JSON object (old --output-format json).
	_ = json.Unmarshal(output, &best)
	return best
}

func assemblePrompt(cfg *config.Config, task config.TaskConfig) (string, error) {
	taskWorkspace := cfg.TaskWorkspace(task)

	absPrompt, err := config.ResolvePromptPath(taskWorkspace, task.PromptFile)
	if err != nil {
		return "", err
	}

	promptData, err := os.ReadFile(absPrompt)
	if err != nil {
		return "", fmt.Errorf("reading prompt file %s: %w", absPrompt, err)
	}

	var parts []string

	if task.Silent {
		parts = append(parts, silentPreamble)
	}

	parts = append(parts, string(promptData))

	return strings.Join(parts, "\n"), nil
}

func buildArgs(cfg *config.Config, task config.TaskConfig, prompt string, sessionID string) []string {
	args := []string{
		"-p", prompt,
		"--model", cfg.TaskModel(task),
		"--max-turns", strconv.Itoa(cfg.TaskMaxTurns(task)),
		"--output-format", "stream-json",
		"--verbose",
	}

	for _, ch := range task.DevChannels {
		args = append(args, "--dangerously-load-development-channels", ch)
	}

	if sessionID != "" {
		args = append(args, "--resume", sessionID)
	}

	// Permission mode: task > defaults > bypass_permissions legacy
	permMode := task.PermissionMode
	if permMode == "" {
		permMode = cfg.Defaults.PermissionMode
	}
	if permMode != "" {
		args = append(args, "--permission-mode", permMode)
	} else if cfg.Defaults.BypassPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}

	mcpConfig := cfg.TaskMCPConfigPath(task)
	if config.HasMCPServers(mcpConfig) {
		args = append(args, "--mcp-config", mcpConfig)
	}

	taskWorkspace := cfg.TaskWorkspace(task)
	args = append(args, "--add-dir", taskWorkspace)

	// Allowed tools: task overrides defaults
	allowedTools := task.AllowedTools
	if len(allowedTools) == 0 {
		allowedTools = cfg.Defaults.AllowedTools
	}
	if len(allowedTools) > 0 {
		args = append(args, "--allowed-tools", strings.Join(allowedTools, ","))
	}

	// Disallowed tools: task overrides defaults
	disallowedTools := task.DisallowedTools
	if len(disallowedTools) == 0 {
		disallowedTools = cfg.Defaults.DisallowedTools
	}
	if len(disallowedTools) > 0 {
		args = append(args, "--disallowed-tools", strings.Join(disallowedTools, ","))
	}

	// System prompt: task overrides defaults
	appendPrompt := task.AppendSystemPrompt
	if appendPrompt == "" {
		appendPrompt = cfg.Defaults.AppendSystemPrompt
	}
	if appendPrompt != "" {
		args = append(args, "--append-system-prompt", appendPrompt)
	}

	return args
}

// logFileName returns a timestamped log filename for the current run.
func logFileName(taskName string) string {
	return fmt.Sprintf("%s-%s.log", taskName, time.Now().UTC().Format("20060102-150405.000"))
}

func writeLogFile(cfg *config.Config, filename string, output []byte) error {
	logDir := filepath.Join(cfg.StatePath(), "logs")
	if err := os.MkdirAll(logDir, 0750); err != nil {
		return err
	}

	logPath := filepath.Join(logDir, filename)
	return os.WriteFile(logPath, output, 0600)
}

// acquireTaskLock creates an exclusive lock file to prevent concurrent task execution.
// If a lock file exists but the owning process is dead, the stale lock is removed.
func acquireTaskLock(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return err
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		if os.IsExist(err) {
			// Check if the lock is stale (owning process is dead)
			data, readErr := os.ReadFile(path)
			if readErr == nil {
				pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
				if parseErr == nil {
					proc, findErr := os.FindProcess(pid)
					if findErr == nil && proc.Signal(syscall.Signal(0)) != nil {
						// Process is dead, remove stale lock and retry once
						os.Remove(path)
						return acquireTaskLock(path)
					}
				}
			}
			return fmt.Errorf("lock file exists at %s", path)
		}
		return err
	}

	fmt.Fprintf(f, "%d", os.Getpid())
	f.Close()
	return nil
}

// releaseTaskLock removes the lock file.
func releaseTaskLock(path string) {
	os.Remove(path)
}
