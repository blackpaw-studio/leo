package run

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/history"
	"github.com/blackpaw-studio/leo/internal/leomcp"
	"github.com/blackpaw-studio/leo/internal/provider"
	"github.com/blackpaw-studio/leo/internal/session"
	"github.com/blackpaw-studio/leo/internal/update"
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

// maxChannelInitAttempts caps the number of extra attempts granted when a
// configured channel plugin's MCP server fails to initialize. These retries
// are infra-flake recovery, not part of the task's own retry budget.
const maxChannelInitAttempts = 2

// channelInitBackoff is the pause between channel-init-failure retries. A
// package-level var (not const) so tests can shrink it to speed up the
// retry-exhaustion path.
var channelInitBackoff = 2 * time.Second

// notifyOutputSeparator prefixes the notify-on-fail child's captured output
// when it is appended to the task's log file.
const notifyOutputSeparator = "\n--- notify-on-fail output ---\n"

// errChannelMCPInit signals that a configured channel plugin's MCP server
// failed to initialize during a claude invocation (reported in the
// stream-json system/init event). The runner treats this as a distinguishable,
// retryable infra flake rather than a genuine task failure.
var errChannelMCPInit = errors.New("channel plugin MCP failed to initialize")

// claudeResult is the minimal structure for parsing the final "result" event
// from claude --output-format stream-json (newline-delimited JSON).
type claudeResult struct {
	SessionID string   `json:"session_id"`
	Result    string   `json:"result"`
	IsError   bool     `json:"is_error"`
	Errors    []string `json:"errors"`
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

	// Persistent tasks are dispatched through the daemon's session router
	// instead of spawning a fresh claude process. The seam keeps the legacy
	// one-shot path completely unaffected.
	if task.Runtime == "persistent" {
		return persistentImpl(cfg, taskName)
	}

	// Acquire task lock to prevent concurrent execution
	lockPath := filepath.Join(cfg.StatePath(), taskName+".lock")
	if err := acquireTaskLock(lockPath); err != nil {
		return fmt.Errorf("task %q is already running: %w", taskName, err)
	}
	defer releaseTaskLock(lockPath)

	provEnv, err := provider.Env(cfg, cfg.TaskProvider(task))
	if err != nil {
		return fmt.Errorf("resolving provider env: %w", err)
	}

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

	// Sync workspace templates (CLAUDE.md, skills/*.md) with this binary's
	// embedded versions. Content-diffed, so it's a no-op when up to date.
	// Covers cron-invoked `leo run <task>` paths that don't go through the
	// daemon (the daemon supervisor does its own refresh at startup).
	//
	// Only refresh the default workspace — a task that explicitly overrides
	// `workspace:` is user-managed; don't stomp on it with template files.
	if taskWorkspace == cfg.DefaultWorkspace() {
		if _, err := update.RefreshWorkspace(taskWorkspace); err != nil {
			fmt.Fprintf(os.Stderr, "warning: workspace refresh failed: %v\n", err)
		}
	}

	maxAttempts := task.Retries + 1
	var lastErr error
	var lastOutput []byte
	var lastLogContent string

	// channelPrefixes are the MCP server-name prefixes ("plugin:<name>:")
	// claude reports for this task's configured channel plugins. When one of
	// these fails to initialize, the attempt is aborted so it doesn't burn
	// max_turns talking to a channel that will never deliver.
	channelPrefixes := channelMCPPrefixes(task.Channels, task.DevChannels)
	channelInitAttempts := 0
	isChannelInitRetry := false

	var timedOut bool
	for attempt := 1; attempt <= maxAttempts; {
		if attempt > 1 && !isChannelInitRetry {
			fmt.Fprintf(os.Stderr, "retrying task %q (attempt %d/%d)\n", taskName, attempt, maxAttempts)
			// Clear session for retry attempts
			sessionID = ""
		}
		isChannelInitRetry = false

		ar := runTaskAttempt(cfg, task, taskName, prompt, sessionID, taskWorkspace, timeout, provEnv, channelPrefixes, sessions)
		timedOut = ar.timedOut
		lastLogContent = string(ar.output)

		// Store session ID for next run
		if sessions != nil && ar.result.SessionID != "" {
			if setErr := sessions.Set("task:"+taskName, ar.result.SessionID); setErr != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to store session ID: %v\n", setErr)
			}
		}

		if ar.execErr == nil {
			lastErr = nil
			lastOutput = nil
			break
		}

		lastErr = ar.execErr
		lastOutput = ar.output

		// A channel plugin's MCP server failing to initialize is an infra
		// flake, not a genuine task failure — retry up to
		// maxChannelInitAttempts times without consuming the task's own
		// retry budget or clearing its session.
		if errors.Is(ar.execErr, errChannelMCPInit) && channelInitAttempts < maxChannelInitAttempts {
			channelInitAttempts++
			isChannelInitRetry = true
			fmt.Fprintf(os.Stderr, "warning: task %q channel plugin MCP init failed (%v); retrying (%d/%d) without consuming retry budget\n",
				taskName, ar.execErr, channelInitAttempts, maxChannelInitAttempts)
			time.Sleep(channelInitBackoff)
			continue
		}

		attempt++
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
		switch {
		case timedOut:
			reason = history.ReasonTimeout
		case errors.Is(lastErr, errChannelMCPInit):
			reason = history.ReasonChannelInit
		default:
			reason = history.ReasonFailure
		}
	}
	hist := history.NewStore(cfg.HomePath)
	if histErr := hist.Record(taskName, exitCode, reason, logFile); histErr != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to record history: %v\n", histErr)
	}

	// Send failure notification if configured (via child claude invocation)
	if lastErr != nil && task.NotifyOnFail && len(task.Channels) > 0 {
		notifyFailure(cfg, taskName, task, taskWorkspace, lastErr, maxAttempts, provEnv, logFile)
	}

	if lastErr != nil {
		return fmt.Errorf("claude exited with error: %w\nOutput: %s", lastErr, string(lastOutput))
	}

	return nil
}

// attemptResult captures the outcome of one main-loop task attempt,
// including any in-place stale-session retry (see runTaskAttempt).
type attemptResult struct {
	output   []byte
	execErr  error
	result   claudeResult
	timedOut bool
}

// runTaskAttempt spawns claude for one attempt of the task, retrying
// in-place (same attempt, no session clear) without --resume if the initial
// spawn used a session that turns out to be stale.
func runTaskAttempt(cfg *config.Config, task config.TaskConfig, taskName, prompt, sessionID, taskWorkspace string, timeout time.Duration, provEnv map[string]string, channelPrefixes []string, sessions *session.Store) attemptResult {
	args := buildArgs(cfg, task, prompt, sessionID)

	// Per-attempt timeout so each retry gets the full timeout
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	output, execErr := executeCommand(ctx, taskWorkspace, args, task.Channels, task.DevChannels, provEnv, channelPrefixes)
	result := parseClaudeOutput(output)
	timedOut := ctx.Err() == context.DeadlineExceeded

	// If --resume failed with a stale session, retry without it.
	if execErr != nil && sessionID != "" && isSessionError(result, output) {
		if sessions != nil {
			if delErr := sessions.Delete("task:" + taskName); delErr != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to clear stale session: %v\n", delErr)
			}
		}

		args = buildArgs(cfg, task, prompt, "")
		output, execErr = executeCommand(ctx, taskWorkspace, args, task.Channels, task.DevChannels, provEnv, channelPrefixes)
		result = parseClaudeOutput(output)
	}

	return attemptResult{output: output, execErr: execErr, result: result, timedOut: timedOut}
}

// notifyFailure spawns a short, bounded claude invocation that asks the agent
// to deliver a failure notification via one of the task's configured channel
// plugins. All errors are logged and swallowed so notify failures don't cascade
// back to the parent task. Its output is appended to the task's log file
// (when logFile is non-empty) so operators can see what the notify child
// actually did. A single fresh-spawn retry is attempted on failure before
// giving up.
func notifyFailure(cfg *config.Config, taskName string, task config.TaskConfig, workspace string, taskErr error, attempts int, extraEnv map[string]string, logFile string) {
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
	args = appendDevChannelFlags(args, task.DevChannels)

	spawn := func() ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), notifyFailureTimeout)
		defer cancel()
		// No channel-init monitoring for the notify child: it's a short,
		// low-stakes invocation and killing it early would only reduce the
		// chance of the failure notice actually reaching the user.
		return executeCommand(ctx, workspace, args, task.Channels, task.DevChannels, extraEnv, nil)
	}

	output, err := spawn()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: notify-on-fail child invocation failed: %v; retrying once\n", err)
		var retryOutput []byte
		retryOutput, err = spawn()
		if len(retryOutput) > 0 {
			output = retryOutput
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: notify-on-fail child invocation failed after retry: %v\n", err)
		}
	}

	if logFile != "" && len(output) > 0 {
		appendNotifyOutputToLog(cfg, logFile, output)
	}
}

// appendNotifyOutputToLog appends the notify-on-fail child's captured output
// to the task's log file, separated by a marker line. Errors are logged and
// swallowed — the notify child already ran; failing to log it shouldn't
// surface as a task failure.
func appendNotifyOutputToLog(cfg *config.Config, logFile string, output []byte) {
	logPath := filepath.Join(cfg.StatePath(), "logs", logFile)
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to append notify-on-fail output to log: %v\n", err)
		return
	}
	defer f.Close()

	if _, err := f.WriteString(notifyOutputSeparator); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to append notify-on-fail output to log: %v\n", err)
		return
	}
	if _, err := f.Write(output); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to append notify-on-fail output to log: %v\n", err)
	}
}

// isSessionError checks whether a claude failure was caused by an invalid/stale
// session. It inspects the parsed result text, each entry of the "errors"
// array (populated on subtype "error_during_execution" results, which carry
// no "result" field), and finally the raw combined output as a last resort.
func isSessionError(result claudeResult, output []byte) bool {
	candidates := make([]string, 0, len(result.Errors)+2)
	candidates = append(candidates, result.Result)
	candidates = append(candidates, result.Errors...)
	candidates = append(candidates, string(output))

	for _, c := range candidates {
		if sessionErrorText(c) {
			return true
		}
	}
	return false
}

// sessionErrorText reports whether text (case-insensitively) matches a known
// stale/invalid-session error pattern.
func sessionErrorText(text string) bool {
	text = strings.ToLower(text)
	if text == "" {
		return false
	}
	if strings.Contains(text, "no conversation found") {
		return true
	}
	return strings.Contains(text, "session") &&
		(strings.Contains(text, "not found") || strings.Contains(text, "invalid") || strings.Contains(text, "expired"))
}

// syncBuffer is a mutex-guarded bytes.Buffer safe for concurrent writes from
// the stdout- and stderr-copying goroutines os/exec spawns internally.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) Bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Copy out to avoid returning a slice aliasing the internal buffer,
	// which could still be mutated concurrently by a slow-to-exit copier.
	out := make([]byte, s.buf.Len())
	copy(out, s.buf.Bytes())
	return out
}

// executeCommand spawns claude and returns its combined stdout+stderr output.
// When channelInitPrefixes is non-empty, the child's stdout is additionally
// scanned incrementally (line by line) for the stream-json system/init event;
// if a configured channel plugin's MCP server reports status "failed" there,
// the process is killed immediately and the returned error wraps
// errChannelMCPInit — the caller doesn't have to wait out the full timeout
// for a channel that will never come up. Pass nil to skip monitoring (e.g.
// for the notify-on-fail child, where it isn't worth the complexity).
func executeCommand(ctx context.Context, workDir string, args []string, channels, devChannels []string, extraEnv map[string]string, channelInitPrefixes []string) ([]byte, error) {
	cmd := execCommand("claude", args...)
	cmd.Dir = workDir
	env := append(os.Environ(), "CLAUDE_CODE_ENTRYPOINT=cli")
	if len(channels) > 0 {
		env = append(env, "LEO_CHANNELS="+strings.Join(channels, ","))
	}
	if len(devChannels) > 0 {
		env = append(env, "LEO_DEV_CHANNELS="+strings.Join(devChannels, ","))
	}
	for k, v := range extraEnv {
		env = append(env, k+"="+v)
	}
	cmd.Env = env

	// Use a done channel to coordinate context cancellation with process lifecycle.
	// Start the process explicitly so we can kill it on timeout.
	//
	// stdout is wrapped in a mutex because when channel-init monitoring is
	// active, cmd.Stdout becomes an io.MultiWriter (a distinct Writer value
	// from cmd.Stderr) — os/exec only serializes concurrent writes to a
	// shared output when Stdout and Stderr are the *same* Writer value, so
	// without the lock the stdout- and stderr-copying goroutines it spawns
	// internally would race on the underlying buffer.
	stdout := &syncBuffer{}
	cmd.Stderr = stdout

	var pw *io.PipeWriter
	var pr *io.PipeReader
	if len(channelInitPrefixes) > 0 {
		pr, pw = io.Pipe()
		cmd.Stdout = io.MultiWriter(stdout, pw)
		// Run in its own process group so a kill (from the monitor detecting
		// a failed channel MCP server, or from the context deadline below)
		// also reaches any children claude spawned that inherited its
		// stdout — otherwise an orphaned grandchild can keep the pipe open
		// and cmd.Wait() would hang waiting for EOF that never comes.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	} else {
		cmd.Stdout = stdout
	}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	kill := func() {
		if cmd.Process == nil {
			return
		}
		if cmd.SysProcAttr != nil && cmd.SysProcAttr.Setpgid {
			if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err == nil {
				return
			}
		}
		cmd.Process.Kill()
	}

	// Monitor context in background; kill process if deadline expires
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			kill()
		case <-done:
		}
	}()

	var monitorErr error
	var monitorDone chan struct{}
	if pw != nil {
		monitorDone = make(chan struct{})
		go func() {
			defer close(monitorDone)
			scanner := bufio.NewScanner(pr)
			scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
			for scanner.Scan() {
				if monitorErr != nil {
					continue // already found a failure; keep draining so writes don't block
				}
				if name, failed := failedChannelMCPServer(scanner.Bytes(), channelInitPrefixes); failed {
					monitorErr = fmt.Errorf("%w: %s", errChannelMCPInit, name)
					kill()
				}
			}
		}()
	}

	err := cmd.Wait()
	close(done) // stop the monitor goroutine

	if pw != nil {
		pw.Close() // unblocks the scanner goroutine (EOF) once the child has exited
		<-monitorDone
	}

	if monitorErr != nil {
		return stdout.Bytes(), monitorErr
	}
	if ctx.Err() != nil {
		return stdout.Bytes(), ctx.Err()
	}
	return stdout.Bytes(), err
}

// mcpServerStatus mirrors one entry in stream-json's system/init event
// "mcp_servers" array.
type mcpServerStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// initSystemEvent is the minimal shape of stream-json's first event (type
// "system", subtype "init"), which reports each MCP server's startup status.
type initSystemEvent struct {
	Type       string            `json:"type"`
	Subtype    string            `json:"subtype"`
	MCPServers []mcpServerStatus `json:"mcp_servers"`
}

// failedChannelMCPServer inspects a single stream-json output line; if it is
// the system/init event and one of its mcp_servers matches a configured
// channel prefix ("plugin:<name>:") with status "failed", it returns that
// server's full name.
func failedChannelMCPServer(line []byte, channelInitPrefixes []string) (string, bool) {
	if len(channelInitPrefixes) == 0 {
		return "", false
	}
	var evt initSystemEvent
	if json.Unmarshal(line, &evt) != nil {
		return "", false
	}
	if evt.Type != "system" || evt.Subtype != "init" {
		return "", false
	}
	for _, srv := range evt.MCPServers {
		if srv.Status != "failed" {
			continue
		}
		for _, prefix := range channelInitPrefixes {
			if strings.HasPrefix(srv.Name, prefix) {
				return srv.Name, true
			}
		}
	}
	return "", false
}

// channelMCPPrefixes converts configured channel plugin ids
// ("plugin:<name>@<marketplace>") into the MCP server-name prefixes claude
// reports them under ("plugin:<name>:<server-name>"). Duplicate prefixes are
// collapsed; channel ids that don't parse as "plugin:<name>@..." (e.g. any
// future non-plugin channel kind) are skipped since they have no MCP server
// counterpart to watch.
func channelMCPPrefixes(channelSets ...[]string) []string {
	seen := make(map[string]bool)
	var prefixes []string
	for _, channels := range channelSets {
		for _, ch := range channels {
			name, ok := channelPluginName(ch)
			if !ok {
				continue
			}
			prefix := "plugin:" + name + ":"
			if seen[prefix] {
				continue
			}
			seen[prefix] = true
			prefixes = append(prefixes, prefix)
		}
	}
	return prefixes
}

// channelPluginName extracts <name> from a channel id of the form
// "plugin:<name>@<marketplace>". Returns ok=false for anything else.
func channelPluginName(channel string) (string, bool) {
	rest, ok := strings.CutPrefix(channel, "plugin:")
	if !ok {
		return "", false
	}
	name, _, ok := strings.Cut(rest, "@")
	if !ok || name == "" {
		return "", false
	}
	return name, true
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
	if best.SessionID != "" || best.Result != "" || len(best.Errors) > 0 {
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

	args = appendDevChannelFlags(args, task.DevChannels)

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
	// Layer in the Leo-managed MCP server so task-mode runs also have access
	// to the universal slash-command tools (a long-running task can call
	// e.g. leo_list_agents to coordinate with the supervisor).
	args = leomcp.AppendArg(args, cfg)

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
	appendPrompt = leomcp.MergeSystemPrompt(cfg, appendPrompt)
	if appendPrompt != "" {
		args = append(args, "--append-system-prompt", appendPrompt)
	}

	return args
}

// appendDevChannelFlags appends one --dangerously-load-development-channels
// flag per dev channel. Used by both buildArgs and notifyFailure so the flag
// wiring lives in one place.
func appendDevChannelFlags(args []string, devChannels []string) []string {
	for _, ch := range devChannels {
		args = append(args, "--dangerously-load-development-channels", ch)
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

	if _, err := fmt.Fprintf(f, "%d", os.Getpid()); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return fmt.Errorf("writing pid to lock file %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("closing lock file %s: %w", path, err)
	}
	return nil
}

// releaseTaskLock removes the lock file.
func releaseTaskLock(path string) {
	os.Remove(path)
}
