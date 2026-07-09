package run

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/history"
	"github.com/blackpaw-studio/leo/internal/session"
)

func TestAssemblePrompt(t *testing.T) {
	dir := t.TempDir()

	promptContent := "Check the inbox and summarize new emails."
	promptFile := filepath.Join(dir, "HEARTBEAT.md")
	os.WriteFile(promptFile, []byte(promptContent), 0644)

	cfg := &config.Config{HomePath: dir}

	tests := []struct {
		name       string
		task       config.TaskConfig
		wantSilent bool
	}{
		{
			name: "basic task",
			task: config.TaskConfig{
				Workspace:  dir,
				PromptFile: "HEARTBEAT.md",
			},
			wantSilent: false,
		},
		{
			name: "silent task",
			task: config.TaskConfig{
				Workspace:  dir,
				PromptFile: "HEARTBEAT.md",
				Silent:     true,
			},
			wantSilent: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prompt, err := assemblePrompt(cfg, tt.task)
			if err != nil {
				t.Fatal(err)
			}

			if !strings.Contains(prompt, promptContent) {
				t.Error("prompt should contain prompt file content")
			}

			// v0.3: channel-agnostic — prompt must not embed telegram-specific artifacts.
			forbidden := []string{
				"Telegram Notification Protocol",
				"$TELEGRAM_BOT_TOKEN",
				"api.telegram.org",
				"message_thread_id",
				"curl -s -X POST",
			}
			for _, s := range forbidden {
				if strings.Contains(prompt, s) {
					t.Errorf("prompt must not contain channel-specific artifact %q", s)
				}
			}

			if tt.wantSilent && !strings.Contains(prompt, "SILENT SCHEDULED RUN") {
				t.Error("silent task should contain preamble")
			}
			if !tt.wantSilent && strings.Contains(prompt, "SILENT SCHEDULED RUN") {
				t.Error("non-silent task should not contain preamble")
			}
		})
	}
}

func TestAssemblePromptPathTraversal(t *testing.T) {
	dir := t.TempDir()

	cfg := &config.Config{HomePath: dir}

	task := config.TaskConfig{Workspace: dir, PromptFile: "../../../etc/passwd"}

	_, err := assemblePrompt(cfg, task)
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
	if !strings.Contains(err.Error(), "escapes workspace") {
		t.Errorf("error = %q, want to contain 'escapes workspace'", err.Error())
	}
}

func TestAssemblePromptMissingFile(t *testing.T) {
	dir := t.TempDir()

	cfg := &config.Config{HomePath: dir}

	task := config.TaskConfig{Workspace: dir, PromptFile: "nonexistent.md"}

	_, err := assemblePrompt(cfg, task)
	if err == nil {
		t.Error("expected error for missing prompt file")
	}
}

func makeTestConfig(dir string, bypassPermissions bool) *config.Config {
	return &config.Config{
		HomePath: dir,
		Defaults: config.DefaultsConfig{
			Model:             "sonnet",
			MaxTurns:          15,
			BypassPermissions: bypassPermissions,
		},
	}
}

func TestBuildArgs(t *testing.T) {
	dir := t.TempDir()
	// MCP config must be at <workspace>/config/mcp-servers.json.
	// Default workspace is <HomePath>/workspace, so create it there.
	mcpDir := filepath.Join(dir, "workspace", "config")
	os.MkdirAll(mcpDir, 0755)
	os.WriteFile(filepath.Join(mcpDir, "mcp-servers.json"), []byte(`{"mcpServers":{"test":{"command":"echo"}}}`), 0644)

	cfg := makeTestConfig(dir, true)
	task := config.TaskConfig{Model: "opus", MaxTurns: 20}
	args := buildArgs(cfg, task, "mytask", "test prompt", "", false)

	argsStr := strings.Join(args, " ")

	if strings.Contains(argsStr, "--agent") {
		t.Error("should not contain --agent flag")
	}
	if !strings.Contains(argsStr, "--model opus") {
		t.Error("should use task model override")
	}
	if !strings.Contains(argsStr, "--max-turns 20") {
		t.Error("should use task max-turns override")
	}
	if !strings.Contains(argsStr, "--dangerously-skip-permissions") {
		t.Error("missing permissions flag when bypass_permissions is true")
	}
	if !strings.Contains(argsStr, "--mcp-config") {
		t.Error("missing mcp-config when file exists")
	}
	if !strings.Contains(argsStr, "--add-dir") {
		t.Error("missing add-dir flag")
	}
	if !strings.Contains(argsStr, "--output-format stream-json") {
		t.Error("should use stream-json output format")
	}
	if !strings.Contains(argsStr, "--verbose") {
		t.Error("should include --verbose for stream-json output")
	}
}

func TestBuildArgsWithoutBypassPermissions(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, false)

	args := buildArgs(cfg, config.TaskConfig{}, "mytask", "test prompt", "", false)
	argsStr := strings.Join(args, " ")

	if strings.Contains(argsStr, "--dangerously-skip-permissions") {
		t.Error("should not contain --dangerously-skip-permissions when bypass_permissions is false")
	}
}

func TestBuildArgsWithoutMCPConfig(t *testing.T) {
	dir := t.TempDir()
	// No mcp-servers.json created

	cfg := makeTestConfig(dir, false)

	args := buildArgs(cfg, config.TaskConfig{}, "mytask", "test prompt", "", false)
	argsStr := strings.Join(args, " ")

	if strings.Contains(argsStr, "--mcp-config") {
		t.Error("should not contain --mcp-config when file doesn't exist")
	}
	if !strings.Contains(argsStr, "--model sonnet") {
		t.Error("should use default model")
	}
	if !strings.Contains(argsStr, "--max-turns 15") {
		t.Error("should use default max-turns")
	}
}

func TestBuildArgsWithSessionID(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, false)

	args := buildArgs(cfg, config.TaskConfig{}, "mytask", "test prompt", "session-abc-123", false)
	argsStr := strings.Join(args, " ")

	if !strings.Contains(argsStr, "--resume session-abc-123") {
		t.Error("should contain --resume with session ID")
	}
}

func TestBuildArgsWithoutSessionID(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, false)

	args := buildArgs(cfg, config.TaskConfig{}, "mytask", "test prompt", "", false)
	argsStr := strings.Join(args, " ")

	if strings.Contains(argsStr, "--resume") {
		t.Error("should not contain --resume without session ID")
	}
	if strings.Contains(argsStr, "--continue") {
		t.Error("should not contain --continue")
	}
}

func TestBuildArgsIncludesDevChannels(t *testing.T) {
	dir := t.TempDir()
	cfg := makeTestConfig(dir, false)
	task := config.TaskConfig{
		DevChannels: []string{
			"plugin:blackpaw-telegram@blackpaw-plugins",
			"plugin:experimental@local-dev",
		},
	}

	args := buildArgs(cfg, task, "mytask", "test prompt", "", false)

	count := 0
	for i, a := range args {
		if a == "--dangerously-load-development-channels" && i+1 < len(args) {
			count++
		}
	}
	if count != 2 {
		t.Errorf("expected 2 --dangerously-load-development-channels flags, got %d: %v", count, args)
	}
}

func TestParseClaudeOutput(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		wantSID  string
		wantText string
	}{
		{
			name:     "stream-json NDJSON",
			output:   "{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"abc-123\"}\n{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"Hi\"}]}}\n{\"type\":\"result\",\"session_id\":\"abc-123\",\"result\":\"Hello world\",\"is_error\":false}\n",
			wantSID:  "abc-123",
			wantText: "Hello world",
		},
		{
			name:     "stream-json error",
			output:   "{\"type\":\"result\",\"session_id\":\"def-456\",\"result\":\"failed\",\"is_error\":true}\n",
			wantSID:  "def-456",
			wantText: "failed",
		},
		{
			name:     "fallback single JSON object",
			output:   `{"session_id":"abc-123","result":"Hello world","is_error":false}`,
			wantSID:  "abc-123",
			wantText: "Hello world",
		},
		{
			name:    "invalid JSON",
			output:  "not json at all",
			wantSID: "",
		},
		{
			name:    "empty",
			output:  "",
			wantSID: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseClaudeOutput([]byte(tt.output))
			if result.SessionID != tt.wantSID {
				t.Errorf("SessionID = %q, want %q", result.SessionID, tt.wantSID)
			}
			if result.Result != tt.wantText {
				t.Errorf("Result = %q, want %q", result.Result, tt.wantText)
			}
		})
	}
}

func TestIsSessionError(t *testing.T) {
	tests := []struct {
		name   string
		result claudeResult
		output string
		want   bool
	}{
		{
			name:   "session not found in result",
			result: claudeResult{Result: "Session not found"},
			want:   true,
		},
		{
			name:   "invalid session in output",
			result: claudeResult{},
			output: "Error: invalid session ID",
			want:   true,
		},
		{
			name:   "expired session",
			result: claudeResult{Result: "session expired"},
			want:   true,
		},
		{
			name:   "unrelated error",
			result: claudeResult{Result: "model overloaded"},
			want:   false,
		},
		{
			name:   "empty",
			result: claudeResult{},
			want:   false,
		},
		// Pinning the raw-scan gating (finding: MINOR #3): the raw-output
		// fallback scan must only run when the parsed result carried no text
		// of its own. A result that did produce text ("did some work") must
		// not be second-guessed by a raw-output substring match, even when
		// that raw output happens to contain a stale-session phrase — e.g.
		// conversational text describing what happened, or a later unrelated
		// log line.
		{
			name:   "non-empty result text suppresses raw-scan fallback",
			result: claudeResult{Result: "did some work"},
			output: "did some work; by the way, no conversation found in an unrelated log line",
			want:   false,
		},
		// Inverse: when the parsed result carries no text at all (empty
		// Result and no Errors — e.g. claude crashed before emitting a result
		// event), the raw-scan fallback is the only signal available and
		// must still catch a stale-session phrase in the combined output.
		{
			name:   "empty result fields fall back to raw-scan match",
			result: claudeResult{},
			output: "fatal: no conversation found for session abc123",
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isSessionError(tt.result, []byte(tt.output))
			if got != tt.want {
				t.Errorf("isSessionError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPreview(t *testing.T) {
	dir := t.TempDir()
	// Default workspace is <HomePath>/workspace; create prompt file there.
	ws := filepath.Join(dir, "workspace")
	os.MkdirAll(ws, 0755)
	os.WriteFile(filepath.Join(ws, "HEARTBEAT.md"), []byte("Check inbox"), 0644)

	cfg := &config.Config{
		HomePath: dir,
		Defaults: config.DefaultsConfig{
			Model:    "sonnet",
			MaxTurns: 15,
		},
		Tasks: map[string]config.TaskConfig{
			"heartbeat": {
				Schedule:   "0 * * * *",
				PromptFile: "HEARTBEAT.md",
				Model:      "opus",
			},
		},
	}

	prompt, args, err := Preview(cfg, "heartbeat", nil)
	if err != nil {
		t.Fatalf("Preview() error: %v", err)
	}

	if !strings.Contains(prompt, "Check inbox") {
		t.Error("prompt should contain file content")
	}

	argsStr := strings.Join(args, " ")
	if !strings.Contains(argsStr, "--model opus") {
		t.Error("args should contain task model override")
	}
}

func TestPreviewTaskNotFound(t *testing.T) {
	cfg := &config.Config{Tasks: map[string]config.TaskConfig{}}

	_, _, err := Preview(cfg, "nonexistent", nil)
	if err == nil {
		t.Fatal("expected error for nonexistent task")
	}
}

func TestRunTaskNotFound(t *testing.T) {
	cfg := &config.Config{
		Tasks: map[string]config.TaskConfig{},
	}

	err := Run(cfg, "nonexistent", nil)
	if err == nil {
		t.Fatal("expected error for nonexistent task")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want to contain 'not found'", err.Error())
	}
}

func TestRunSuccess(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()

	dir := t.TempDir()
	// Default workspace is <HomePath>/workspace; create prompt file there.
	ws := filepath.Join(dir, "workspace")
	os.MkdirAll(ws, 0755)
	os.WriteFile(filepath.Join(ws, "task.md"), []byte("test prompt"), 0644)

	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("echo", "task output")
	}

	cfg := &config.Config{
		HomePath: dir,
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 15},
		Tasks: map[string]config.TaskConfig{
			"mytask": {PromptFile: "task.md", Schedule: "0 * * * *", Enabled: true},
		},
	}

	err := Run(cfg, "mytask", nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// Verify log was written; logs now go to state/logs/mytask-*.log
	logFiles, err := filepath.Glob(filepath.Join(dir, "state", "logs", "mytask-*.log"))
	if err != nil {
		t.Fatalf("globbing logs: %v", err)
	}
	if len(logFiles) == 0 {
		t.Fatal("no log files found")
	}
	logData, err := os.ReadFile(logFiles[0])
	if err != nil {
		t.Fatalf("reading log: %v", err)
	}
	if !strings.Contains(string(logData), "task output") {
		t.Errorf("log = %q, want to contain 'task output'", string(logData))
	}
}

func TestRunCommandError(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()

	dir := t.TempDir()
	ws := filepath.Join(dir, "workspace")
	os.MkdirAll(ws, 0755)
	os.WriteFile(filepath.Join(ws, "task.md"), []byte("test prompt"), 0644)

	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("false")
	}

	cfg := &config.Config{
		HomePath: dir,
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 15},
		Tasks: map[string]config.TaskConfig{
			"mytask": {PromptFile: "task.md", Schedule: "0 * * * *", Enabled: true},
		},
	}

	err := Run(cfg, "mytask", nil)
	if err == nil {
		t.Fatal("Run() should return error when command fails")
	}
	if !strings.Contains(err.Error(), "claude exited with error") {
		t.Errorf("error = %q, want to contain 'claude exited with error'", err.Error())
	}
}

func TestRunMissingPromptFile(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()

	dir := t.TempDir()

	cfg := &config.Config{
		HomePath: dir,
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 15},
		Tasks: map[string]config.TaskConfig{
			"mytask": {PromptFile: "nonexistent.md", Schedule: "0 * * * *"},
		},
	}

	err := Run(cfg, "mytask", nil)
	if err == nil {
		t.Fatal("Run() should return error for missing prompt file")
	}
	if !strings.Contains(err.Error(), "assembling prompt") {
		t.Errorf("error = %q, want to contain 'assembling prompt'", err.Error())
	}
}

// TestRunNotifyOnFailInvokesChannelChild verifies that a failing task with
// NotifyOnFail + Channels triggers a second claude invocation after the main
// task run, and that the child invocation receives LEO_CHANNELS via the env.
func TestRunNotifyOnFailInvokesChannelChild(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()

	dir := t.TempDir()
	ws := filepath.Join(dir, "workspace")
	os.MkdirAll(ws, 0755)
	os.WriteFile(filepath.Join(ws, "task.md"), []byte("test prompt"), 0644)

	var invocations int
	var sawNotifyArgs bool
	execCommand = func(name string, args ...string) *exec.Cmd {
		invocations++
		// The notify child invocation is short and has --max-turns 3 + acceptEdits.
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "--max-turns 3") && strings.Contains(joined, "--permission-mode acceptEdits") {
			sawNotifyArgs = true
			return exec.Command("true") // notify-on-fail child succeeds quickly
		}
		return exec.Command("false") // main task fails
	}

	cfg := &config.Config{
		HomePath: dir,
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 15},
		Tasks: map[string]config.TaskConfig{
			"mytask": {
				PromptFile:   "task.md",
				Schedule:     "0 * * * *",
				Enabled:      true,
				NotifyOnFail: true,
				Channels:     []string{"plugin:telegram@claude-plugins-official"},
			},
		},
	}

	err := Run(cfg, "mytask", nil)
	if err == nil {
		t.Fatal("Run() should return error when main command fails")
	}
	if invocations < 2 {
		t.Errorf("expected at least 2 exec invocations (main + notify), got %d", invocations)
	}
	if !sawNotifyArgs {
		t.Error("expected notify-on-fail child invocation with --max-turns 3 + acceptEdits")
	}
}

// TestRunNotifyOnFailSkippedWithoutChannels verifies that NotifyOnFail is a
// no-op when the task has no channels configured.
func TestRunNotifyOnFailSkippedWithoutChannels(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()

	dir := t.TempDir()
	ws := filepath.Join(dir, "workspace")
	os.MkdirAll(ws, 0755)
	os.WriteFile(filepath.Join(ws, "task.md"), []byte("test prompt"), 0644)

	var invocations int
	execCommand = func(name string, args ...string) *exec.Cmd {
		invocations++
		return exec.Command("false")
	}

	cfg := &config.Config{
		HomePath: dir,
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 15},
		Tasks: map[string]config.TaskConfig{
			"mytask": {
				PromptFile:   "task.md",
				Schedule:     "0 * * * *",
				Enabled:      true,
				NotifyOnFail: true,
				// No Channels set
			},
		},
	}

	_ = Run(cfg, "mytask", nil)
	if invocations != 1 {
		t.Errorf("expected exactly 1 exec invocation (no notify without channels), got %d", invocations)
	}
}

// TestRunStaleSessionFallbackRealWorldOutput reproduces the exact real-world
// claude output when a resumed session's transcript has been cleaned up: a
// stderr line "No conversation found with session ID: ..." followed by a
// stream-json result event of subtype "error_during_execution" that carries
// the message in the "errors" array (no "result" field). isSessionError must
// recognize this shape, the runner must clear the stale session and retry
// without --resume in the same attempt, and store the new session id from
// the successful fresh run.
func TestRunStaleSessionFallbackRealWorldOutput(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()

	dir := t.TempDir()
	ws := filepath.Join(dir, "workspace")
	os.MkdirAll(ws, 0755)
	os.WriteFile(filepath.Join(ws, "task.md"), []byte("test prompt"), 0644)

	staleSessionID := "a3813644-1111-2222-3333-444455556666"
	staleOutput := "No conversation found with session ID: " + staleSessionID + "\n" +
		`{"type":"result","subtype":"error_during_execution","is_error":true,"num_turns":0,"session_id":"` +
		staleSessionID + `","errors":["No conversation found with session ID: ` + staleSessionID + `"]}` + "\n"
	freshOutput := `{"type":"result","subtype":"success","is_error":false,"num_turns":1,"session_id":"fresh-session-999","result":"done"}` + "\n"

	staleFile := filepath.Join(dir, "stale.out")
	freshFile := filepath.Join(dir, "fresh.out")
	os.WriteFile(staleFile, []byte(staleOutput), 0644)
	os.WriteFile(freshFile, []byte(freshOutput), 0644)

	var invocations int
	execCommand = func(name string, args ...string) *exec.Cmd {
		invocations++
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "--resume") {
			return exec.Command("sh", "-c", "cat "+staleFile+"; exit 1")
		}
		return exec.Command("sh", "-c", "cat "+freshFile)
	}

	cfg := &config.Config{
		HomePath: dir,
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 15},
		Tasks: map[string]config.TaskConfig{
			"mytask": {PromptFile: "task.md", Schedule: "0 * * * *", Enabled: true},
		},
	}

	sessions := session.NewStore(dir)
	if err := sessions.Set("task:mytask", staleSessionID); err != nil {
		t.Fatalf("seeding stale session: %v", err)
	}

	err := Run(cfg, "mytask", sessions)
	if err != nil {
		t.Fatalf("Run() should succeed after stale-session fallback, got: %v", err)
	}
	if invocations != 2 {
		t.Fatalf("expected 2 exec invocations (resume + fresh retry), got %d", invocations)
	}

	newSID, _, getErr := sessions.Get("task:mytask")
	if getErr != nil {
		t.Fatalf("reading session after run: %v", getErr)
	}
	if newSID != "fresh-session-999" {
		t.Errorf("stored session id = %q, want %q", newSID, "fresh-session-999")
	}
}

func TestWriteLog(t *testing.T) {
	dir := t.TempDir()

	cfg := &config.Config{
		HomePath: dir,
	}

	filename := "test-task-20260410-120000.log"
	if err := writeLogFile(cfg, filename, []byte("test output")); err != nil {
		t.Fatal(err)
	}

	// Logs now go into state/logs/
	logPath := filepath.Join(dir, "state", "logs", filename)
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}

	if string(data) != "test output" {
		t.Errorf("log content = %q, want %q", string(data), "test output")
	}
}

func TestAcquireTaskLock(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "state", "test.lock")

	// First acquire should succeed
	if err := acquireTaskLock(lockPath); err != nil {
		t.Fatalf("first acquireTaskLock() error: %v", err)
	}

	// Lock file should contain our PID
	data, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("reading lock file: %v", err)
	}
	wantPid := fmt.Sprintf("%d", os.Getpid())
	if string(data) != wantPid {
		t.Errorf("lock file = %q, want %q", string(data), wantPid)
	}

	// Second acquire should fail (same PID is still alive)
	if err := acquireTaskLock(lockPath); err == nil {
		t.Fatal("second acquireTaskLock() should fail when lock is held")
	}

	// Release and reacquire should work
	releaseTaskLock(lockPath)
	if err := acquireTaskLock(lockPath); err != nil {
		t.Fatalf("acquireTaskLock() after release error: %v", err)
	}
	releaseTaskLock(lockPath)
}

func TestAcquireTaskLockStaleLock(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "state", "test.lock")

	// Create state dir and write a lock file with a dead PID
	os.MkdirAll(filepath.Dir(lockPath), 0750)
	// PID 2147483647 is unlikely to be a real running process
	os.WriteFile(lockPath, []byte("2147483647"), 0600)

	// Should succeed because the lock is stale
	if err := acquireTaskLock(lockPath); err != nil {
		t.Fatalf("acquireTaskLock() with stale lock error: %v", err)
	}

	// Verify it wrote our PID
	data, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("reading lock file: %v", err)
	}
	wantPid := fmt.Sprintf("%d", os.Getpid())
	if string(data) != wantPid {
		t.Errorf("lock file = %q, want %q", string(data), wantPid)
	}

	releaseTaskLock(lockPath)
}

func TestReleaseTaskLockNoFile(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "nonexistent.lock")

	// Should not panic or error
	releaseTaskLock(lockPath)
}

func TestRunConcurrencyGuard(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()

	dir := t.TempDir()
	ws := filepath.Join(dir, "workspace")
	os.MkdirAll(ws, 0755)
	os.WriteFile(filepath.Join(ws, "task.md"), []byte("test prompt"), 0644)

	cfg := &config.Config{
		HomePath: dir,
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 15},
		Tasks: map[string]config.TaskConfig{
			"mytask": {PromptFile: "task.md", Schedule: "0 * * * *", Enabled: true},
		},
	}

	// Pre-create a lock file with our own PID (simulating already-running task)
	stateDir := filepath.Join(dir, "state")
	os.MkdirAll(stateDir, 0750)
	lockPath := filepath.Join(stateDir, "mytask.lock")
	os.WriteFile(lockPath, []byte(fmt.Sprintf("%d", os.Getpid())), 0600)

	err := Run(cfg, "mytask", nil)
	if err == nil {
		t.Fatal("Run() should fail when task is already running")
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Errorf("error = %q, want to contain 'already running'", err.Error())
	}

	// Clean up lock
	os.Remove(lockPath)
}

func TestBuildArgsInjectsMessagingAwareness(t *testing.T) {
	cfg := &config.Config{HomePath: t.TempDir(), Web: config.WebConfig{Enabled: true}}
	args := buildArgs(cfg, config.TaskConfig{}, "mytask", "do the thing", "sess-1", false)

	found := false
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--append-system-prompt" && strings.Contains(args[i+1], "leo_send_message") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected messaging awareness in task args; got %v", args)
	}
}

// TestLeoMCPEnv covers the three-way gate: web disabled, web enabled but no
// token file, and web enabled with a readable non-empty token file.
func TestLeoMCPEnv(t *testing.T) {
	t.Run("web disabled", func(t *testing.T) {
		dir := t.TempDir()
		cfg := &config.Config{HomePath: dir, Web: config.WebConfig{Enabled: false}}
		os.MkdirAll(cfg.StatePath(), 0750)
		os.WriteFile(filepath.Join(cfg.StatePath(), "api.token"), []byte("tok123"), 0600)

		env, ok := leoMCPEnv(cfg, "mytask")
		if ok {
			t.Errorf("expected ok=false when web disabled, got env=%v", env)
		}
	})

	t.Run("web enabled, no token file", func(t *testing.T) {
		dir := t.TempDir()
		cfg := &config.Config{HomePath: dir, Web: config.WebConfig{Enabled: true}}

		env, ok := leoMCPEnv(cfg, "mytask")
		if ok {
			t.Errorf("expected ok=false when token file missing, got env=%v", env)
		}
	})

	t.Run("web enabled, empty token file", func(t *testing.T) {
		dir := t.TempDir()
		cfg := &config.Config{HomePath: dir, Web: config.WebConfig{Enabled: true}}
		os.MkdirAll(cfg.StatePath(), 0750)
		os.WriteFile(filepath.Join(cfg.StatePath(), "api.token"), []byte("   \n"), 0600)

		env, ok := leoMCPEnv(cfg, "mytask")
		if ok {
			t.Errorf("expected ok=false when token file is blank, got env=%v", env)
		}
	})

	t.Run("web enabled, token present", func(t *testing.T) {
		dir := t.TempDir()
		cfg := &config.Config{HomePath: dir, Web: config.WebConfig{Enabled: true, Port: 9999}}
		os.MkdirAll(cfg.StatePath(), 0750)
		os.WriteFile(filepath.Join(cfg.StatePath(), "api.token"), []byte("tok123\n"), 0600)

		env, ok := leoMCPEnv(cfg, "mytask")
		if !ok {
			t.Fatal("expected ok=true when web enabled and token present")
		}
		if env["LEO_PROCESS_NAME"] != "task:mytask" {
			t.Errorf("LEO_PROCESS_NAME = %q, want %q", env["LEO_PROCESS_NAME"], "task:mytask")
		}
		if env["LEO_WEB_PORT"] != "9999" {
			t.Errorf("LEO_WEB_PORT = %q, want %q", env["LEO_WEB_PORT"], "9999")
		}
		if env["LEO_API_TOKEN"] != "tok123" {
			t.Errorf("LEO_API_TOKEN = %q, want %q (whitespace-trimmed)", env["LEO_API_TOKEN"], "tok123")
		}
	})
}

// TestBuildArgsSkipsLeoMCPWithoutToken verifies buildArgs (and by extension
// Preview, which shares this code path) doesn't wire in the leo MCP server
// when the token gate fails, even though cfg.Web.Enabled is true.
func TestBuildArgsSkipsLeoMCPWithoutToken(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{HomePath: dir, Web: config.WebConfig{Enabled: true}}
	// No api.token file written.

	_, leoMCPOK := leoMCPEnv(cfg, "mytask")
	args := buildArgs(cfg, config.TaskConfig{}, "mytask", "do the thing", "", leoMCPOK)
	for i, a := range args {
		if a == "--mcp-config" && i+1 < len(args) && strings.HasSuffix(args[i+1], "leo-mcp.json") {
			t.Errorf("did not expect leo MCP config wired in without a readable token; args=%v", args)
		}
	}
}

// TestBuildArgsIncludesLeoMCPWithToken verifies buildArgs wires in the leo
// MCP server when the gate passes (web enabled + readable non-empty token).
func TestBuildArgsIncludesLeoMCPWithToken(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{HomePath: dir, Web: config.WebConfig{Enabled: true}}
	os.MkdirAll(cfg.StatePath(), 0750)
	os.WriteFile(filepath.Join(cfg.StatePath(), "api.token"), []byte("tok123"), 0600)

	_, leoMCPOK := leoMCPEnv(cfg, "mytask")
	args := buildArgs(cfg, config.TaskConfig{}, "mytask", "do the thing", "", leoMCPOK)
	found := false
	for i, a := range args {
		if a == "--mcp-config" && i+1 < len(args) && strings.HasSuffix(args[i+1], "leo-mcp.json") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected leo MCP config wired in when token is readable; args=%v", args)
	}
}

// TestRunPassesLeoMCPEnvToExecuteCommand verifies Run() injects
// LEO_PROCESS_NAME/LEO_WEB_PORT/LEO_API_TOKEN into the spawned claude's
// environment when the leo MCP gate passes.
func TestRunPassesLeoMCPEnvToExecuteCommand(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()

	dir := t.TempDir()
	ws := filepath.Join(dir, "workspace")
	os.MkdirAll(ws, 0755)
	os.WriteFile(filepath.Join(ws, "task.md"), []byte("test prompt"), 0644)

	cfg := &config.Config{
		HomePath: dir,
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 15},
		Web:      config.WebConfig{Enabled: true, Port: 8888},
		Tasks: map[string]config.TaskConfig{
			"mytask": {PromptFile: "task.md", Schedule: "0 * * * *", Enabled: true},
		},
	}
	os.MkdirAll(cfg.StatePath(), 0750)
	os.WriteFile(filepath.Join(cfg.StatePath(), "api.token"), []byte("tok-xyz"), 0600)

	execCommand = func(name string, args ...string) *exec.Cmd {
		// executeCommand sets cmd.Env after the seam returns the *exec.Cmd;
		// printing the actual process env (via `env`) is how we observe it.
		return exec.Command("sh", "-c", "env")
	}

	err := Run(cfg, "mytask", nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	logFiles, _ := filepath.Glob(filepath.Join(dir, "state", "logs", "mytask-*.log"))
	if len(logFiles) == 0 {
		t.Fatal("no log files found")
	}
	logData, err := os.ReadFile(logFiles[0])
	if err != nil {
		t.Fatalf("reading log: %v", err)
	}
	got := string(logData)
	if !strings.Contains(got, "LEO_PROCESS_NAME=task:mytask") {
		t.Errorf("log missing LEO_PROCESS_NAME:\n%s", got)
	}
	if !strings.Contains(got, "LEO_WEB_PORT=8888") {
		t.Errorf("log missing LEO_WEB_PORT:\n%s", got)
	}
	if !strings.Contains(got, "LEO_API_TOKEN=tok-xyz") {
		t.Errorf("log missing LEO_API_TOKEN:\n%s", got)
	}
}

func TestExecuteCommandInjectsExtraEnv(t *testing.T) {
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, args ...string) *exec.Cmd { return exec.Command("env") }

	out, err := executeCommand(context.Background(), t.TempDir(), nil, nil, nil,
		map[string]string{"ANTHROPIC_BASE_URL": "https://x.example", "ANTHROPIC_AUTH_TOKEN": "sk-t"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "ANTHROPIC_BASE_URL=https://x.example") ||
		!strings.Contains(string(out), "ANTHROPIC_AUTH_TOKEN=sk-t") {
		t.Fatalf("provider env missing from spawned env:\n%s", out)
	}
}

func TestExecuteCommandNoExtraEnv(t *testing.T) {
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, args ...string) *exec.Cmd { return exec.Command("env") }

	t.Setenv("ANTHROPIC_BASE_URL", "")
	os.Unsetenv("ANTHROPIC_BASE_URL")

	out, err := executeCommand(context.Background(), t.TempDir(), nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "ANTHROPIC_BASE_URL=") {
		t.Fatalf("unexpected provider env:\n%s", out)
	}
}

// TestChannelMCPPrefixes covers the channel-id -> MCP-server-prefix mapping:
// "plugin:<name>@<marketplace>" configured channels correspond to MCP servers
// reported as "plugin:<name>:<server-name>".
func TestChannelMCPPrefixes(t *testing.T) {
	tests := []struct {
		name     string
		channels []string
		dev      []string
		want     []string
	}{
		{
			name:     "single channel",
			channels: []string{"plugin:blackpaw-telegram@blackpaw-studio/claude-plugins"},
			want:     []string{"plugin:blackpaw-telegram:"},
		},
		{
			name:     "channels and dev channels combined, dedup",
			channels: []string{"plugin:blackpaw-telegram@marketplace-a"},
			dev:      []string{"plugin:blackpaw-telegram@marketplace-b", "plugin:experimental@local-dev"},
			want:     []string{"plugin:blackpaw-telegram:", "plugin:experimental:"},
		},
		{
			name:     "empty",
			channels: nil,
			want:     nil,
		},
		{
			name:     "malformed channel id skipped",
			channels: []string{"not-a-plugin-id", "plugin:no-at-sign", "plugin:@nothing"},
			want:     nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := channelMCPPrefixes(tt.channels, tt.dev)
			if len(got) != len(tt.want) {
				t.Fatalf("channelMCPPrefixes() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("channelMCPPrefixes()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestRunChannelMCPInitFailureRetriesAndRecordsHistory verifies that a
// channel plugin's MCP server reporting status "failed" in the init event
// causes the runner to kill the process, retry up to maxChannelInitAttempts
// times without clearing the session or consuming the task's own retry
// budget, and ultimately record history with ReasonChannelInit when every
// attempt (including the extra channel-init retries) fails the same way.
func TestRunChannelMCPInitFailureRetriesAndRecordsHistory(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()

	// Speed the test up: skip the real 2s backoff between channel-init retries.
	origBackoff := channelInitBackoff
	channelInitBackoff = 0
	defer func() { channelInitBackoff = origBackoff }()

	dir := t.TempDir()
	ws := filepath.Join(dir, "workspace")
	os.MkdirAll(ws, 0755)
	os.WriteFile(filepath.Join(ws, "task.md"), []byte("test prompt"), 0644)

	initFailedLine := `{"type":"system","subtype":"init","mcp_servers":[{"name":"plugin:blackpaw-telegram:blackpaw-telegram","status":"failed"}]}`

	var invocations int
	execCommand = func(name string, args ...string) *exec.Cmd {
		invocations++
		// Emit the failed-init event then sleep; the monitor should kill us
		// before the sleep completes.
		return exec.Command("sh", "-c", "echo '"+initFailedLine+"'; sleep 5")
	}

	cfg := &config.Config{
		HomePath: dir,
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 15},
		Tasks: map[string]config.TaskConfig{
			"mytask": {
				PromptFile: "task.md",
				Schedule:   "0 * * * *",
				Enabled:    true,
				Channels:   []string{"plugin:blackpaw-telegram@blackpaw-studio/claude-plugins"},
			},
		},
	}

	err := Run(cfg, "mytask", nil)
	if err == nil {
		t.Fatal("Run() should return error when channel MCP init keeps failing")
	}
	if !errors.Is(err, errChannelMCPInit) {
		t.Errorf("expected error to wrap errChannelMCPInit, got: %v", err)
	}
	// 1 initial attempt + maxChannelInitAttempts (2) extra retries = 3.
	wantInvocations := 1 + maxChannelInitAttempts
	if invocations != wantInvocations {
		t.Errorf("invocations = %d, want %d", invocations, wantInvocations)
	}

	hist := history.NewStore(cfg.HomePath)
	entry := hist.Get("mytask")
	if entry == nil {
		t.Fatal("expected a history entry")
	}
	if entry.Reason != history.ReasonChannelInit {
		t.Errorf("history reason = %q, want %q", entry.Reason, history.ReasonChannelInit)
	}
}

// TestRunChannelMCPInitNonMatchingServerNoAbort verifies that an init event
// reporting a failed MCP server that does NOT correspond to any configured
// channel plugin does not trigger the channel-init abort/retry path.
func TestRunChannelMCPInitNonMatchingServerNoAbort(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()

	dir := t.TempDir()
	ws := filepath.Join(dir, "workspace")
	os.MkdirAll(ws, 0755)
	os.WriteFile(filepath.Join(ws, "task.md"), []byte("test prompt"), 0644)

	// A failed MCP server that isn't one of the task's configured channels.
	initLine := `{"type":"system","subtype":"init","mcp_servers":[{"name":"plugin:some-other-plugin:some-other-plugin","status":"failed"}]}`
	resultLine := `{"type":"result","session_id":"sess-ok","result":"done","is_error":false}`

	var invocations int
	execCommand = func(name string, args ...string) *exec.Cmd {
		invocations++
		return exec.Command("sh", "-c", "echo '"+initLine+"'; echo '"+resultLine+"'")
	}

	cfg := &config.Config{
		HomePath: dir,
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 15},
		Tasks: map[string]config.TaskConfig{
			"mytask": {
				PromptFile: "task.md",
				Schedule:   "0 * * * *",
				Enabled:    true,
				Channels:   []string{"plugin:blackpaw-telegram@blackpaw-studio/claude-plugins"},
			},
		},
	}

	err := Run(cfg, "mytask", nil)
	if err != nil {
		t.Fatalf("Run() should succeed when the failed MCP server doesn't match a configured channel: %v", err)
	}
	if invocations != 1 {
		t.Errorf("invocations = %d, want 1 (no channel-init abort/retry)", invocations)
	}
}

// runExecuteCommandWithDeadline runs executeCommand in a goroutine and fails
// the test if it doesn't return within bound — used by the hang-regression
// tests below so assertions never depend on exact kill timing, only on
// executeCommand returning promptly relative to a generous ceiling.
func runExecuteCommandWithDeadline(t *testing.T, bound time.Duration, ctx context.Context, workDir string, args []string, channelInitPrefixes []string) ([]byte, error) {
	t.Helper()
	type result struct {
		out []byte
		err error
	}
	resCh := make(chan result, 1)
	go func() {
		out, err := executeCommand(ctx, workDir, args, nil, nil, nil, channelInitPrefixes)
		resCh <- result{out, err}
	}()

	select {
	case r := <-resCh:
		return r.out, r.err
	case <-time.After(bound):
		t.Fatalf("executeCommand did not return within %s — likely hung", bound)
		return nil, nil
	}
}

// TestExecuteCommandDrainsPipeAfterScannerOverflow is the regression test for
// finding 1 (CRITICAL): a single stream-json line exceeding the channel-init
// monitor's scanner buffer cap used to make `for scanner.Scan()` exit on
// bufio.ErrTooLong while nothing else read the io.Pipe, blocking the
// MultiWriter's write into it forever and hanging cmd.Wait() (and therefore
// executeCommand) even though the child had already finished writing and
// exited. executeCommand must now drain the pipe after the scan loop ends so
// the child is never blocked, and the full output (oversized line included)
// must still be captured via the independent syncBuffer.
func TestExecuteCommandDrainsPipeAfterScannerOverflow(t *testing.T) {
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })

	// Shrink the cap so the test doesn't need a multi-megabyte fixture to
	// exceed it.
	origMax := maxScannerBufferSize
	maxScannerBufferSize = 1024
	t.Cleanup(func() { maxScannerBufferSize = origMax })

	oversized := strings.Repeat("x", 4096)
	execCommand = func(name string, args ...string) *exec.Cmd {
		script := fmt.Sprintf("echo '%s'; echo done-marker; exit 0", oversized)
		return exec.Command("sh", "-c", script)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	out, err := runExecuteCommandWithDeadline(t, 8*time.Second, ctx, t.TempDir(), nil, []string{"plugin:doesnotmatter:"})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !strings.Contains(string(out), oversized) {
		t.Error("expected the oversized line to be captured in output")
	}
	if !strings.Contains(string(out), "done-marker") {
		t.Error("expected output written after the oversized line to be captured too")
	}
}

// TestExecuteCommandKillsGrandchildOnChannelInitFailure is the regression
// test for finding 8, pinning both the pgid-based kill (finding 2/4's
// always-Setpgid child) and the no-hang property together: the fake child
// backgrounds a grandchild that inherits (and holds open) the stdout pipe,
// then reports a failed channel MCP init and sleeps. If the kill only
// reached the direct child (not its process group), the orphaned grandchild
// would keep the stdout pipe's write end open and cmd.Wait() would hang
// waiting for EOF that never comes.
//
// Both the direct child's and the backgrounded grandchild's sleeps are long
// (30s) relative to the elapsed-time assertion below (~5s): a prior version
// of this test used sleep 5, which happened to expire *under* the 8s hang
// bound, so a reverted pgid-kill (an unkilled grandchild just running to
// completion on its own) still made the test pass — the hang bound alone
// didn't actually exercise the regression. With 30s sleeps, a leaked
// grandchild blows well past both the hang bound and the elapsed-time
// assertion, so the test fails on either a hang or on being slow, not only
// on a hang.
func TestExecuteCommandKillsGrandchildOnChannelInitFailure(t *testing.T) {
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })

	initFailedLine := `{"type":"system","subtype":"init","mcp_servers":[{"name":"plugin:blackpaw-telegram:blackpaw-telegram","status":"failed"}]}`

	execCommand = func(name string, args ...string) *exec.Cmd {
		script := "(sleep 30 >&2 &); echo '" + initFailedLine + "'; sleep 30"
		return exec.Command("sh", "-c", script)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	start := time.Now()
	_, err := runExecuteCommandWithDeadline(t, 8*time.Second, ctx, t.TempDir(), nil, []string{"plugin:blackpaw-telegram:"})
	elapsed := time.Since(start)

	if !errors.Is(err, errChannelMCPInit) {
		t.Errorf("expected errChannelMCPInit, got %v", err)
	}
	// The kill should complete in well under a second; 5s is a generous
	// ceiling that still fails fast if the grandchild leaks and its 30s
	// sleep is what actually let the process (and cmd.Wait) finish.
	if elapsed >= 5*time.Second {
		t.Errorf("executeCommand took %s to return, want < 5s — grandchild likely leaked and ran to completion instead of being killed", elapsed)
	}
}

// TestRunStaleSessionInAttemptRetryTimeoutReason is the regression test for
// finding 3: the in-attempt stale-session retry (runTaskAttempt) shares the
// same per-attempt context/deadline as the initial --resume spawn. If the
// *retry* (not the initial spawn) is what hits the deadline, the recorded
// history reason must still be "timeout", not a generic "failure" — which
// requires deriving the reason from the final error (errors.Is against
// context.DeadlineExceeded) rather than a flag captured right after the
// initial spawn.
func TestRunStaleSessionInAttemptRetryTimeoutReason(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()

	dir := t.TempDir()
	ws := filepath.Join(dir, "workspace")
	os.MkdirAll(ws, 0755)
	os.WriteFile(filepath.Join(ws, "task.md"), []byte("test prompt"), 0644)

	staleSessionID := "aaaa-bbbb-cccc"
	staleOutput := "No conversation found with session ID: " + staleSessionID + "\n" +
		`{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"` +
		staleSessionID + `","errors":["No conversation found with session ID: ` + staleSessionID + `"]}` + "\n"
	staleFile := filepath.Join(dir, "stale.out")
	os.WriteFile(staleFile, []byte(staleOutput), 0644)

	execCommand = func(name string, args ...string) *exec.Cmd {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "--resume") {
			// Initial spawn: fails fast with a stale-session error, well
			// within the task's short timeout.
			return exec.Command("sh", "-c", "cat "+staleFile+"; exit 1")
		}
		// In-attempt retry (no --resume): sleeps well past the timeout so
		// the shared per-attempt context deadline fires during the retry,
		// not the initial spawn.
		return exec.Command("sh", "-c", "sleep 2")
	}

	cfg := &config.Config{
		HomePath: dir,
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 15},
		Tasks: map[string]config.TaskConfig{
			"mytask": {PromptFile: "task.md", Schedule: "0 * * * *", Enabled: true, Timeout: "200ms"},
		},
	}

	sessions := session.NewStore(dir)
	if err := sessions.Set("task:mytask", staleSessionID); err != nil {
		t.Fatalf("seeding stale session: %v", err)
	}

	err := Run(cfg, "mytask", sessions)
	if err == nil {
		t.Fatal("Run() should return an error when the in-attempt retry times out")
	}

	hist := history.NewStore(cfg.HomePath)
	entry := hist.Get("mytask")
	if entry == nil {
		t.Fatal("expected a history entry")
	}
	if entry.Reason != history.ReasonTimeout {
		t.Errorf("history reason = %q, want %q (in-attempt retry hit the deadline)", entry.Reason, history.ReasonTimeout)
	}
}

// TestRunChannelInitExhaustionDoesNotClearSession is the regression test for
// finding 7: once the free channel-init retries (maxChannelInitAttempts) are
// exhausted and a further channel-init failure starts consuming the task's
// own retry budget, the next attempt must NOT clear a perfectly valid
// session — the failure says nothing about whether the session itself is
// stale. Verified by checking that the final attempt's args still carry
// --resume with the originally seeded session ID.
func TestRunChannelInitExhaustionDoesNotClearSession(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()

	origBackoff := channelInitBackoff
	channelInitBackoff = 0
	defer func() { channelInitBackoff = origBackoff }()

	dir := t.TempDir()
	ws := filepath.Join(dir, "workspace")
	os.MkdirAll(ws, 0755)
	os.WriteFile(filepath.Join(ws, "task.md"), []byte("test prompt"), 0644)

	seededSessionID := "seeded-session-123"
	initFailedLine := `{"type":"system","subtype":"init","mcp_servers":[{"name":"plugin:blackpaw-telegram:blackpaw-telegram","status":"failed"}]}`

	var allArgs [][]string
	execCommand = func(name string, args ...string) *exec.Cmd {
		allArgs = append(allArgs, append([]string(nil), args...))
		return exec.Command("sh", "-c", "echo '"+initFailedLine+"'; sleep 5")
	}

	cfg := &config.Config{
		HomePath: dir,
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 15},
		Tasks: map[string]config.TaskConfig{
			"mytask": {
				PromptFile: "task.md",
				Schedule:   "0 * * * *",
				Enabled:    true,
				Retries:    1, // maxAttempts = 2, so a real (non-channel-init) retry occurs
				Channels:   []string{"plugin:blackpaw-telegram@blackpaw-studio/claude-plugins"},
			},
		},
	}

	sessions := session.NewStore(dir)
	if err := sessions.Set("task:mytask", seededSessionID); err != nil {
		t.Fatalf("seeding session: %v", err)
	}

	err := Run(cfg, "mytask", sessions)
	if err == nil {
		t.Fatal("Run() should return an error when channel MCP init keeps failing")
	}

	// 1 initial + maxChannelInitAttempts channel-init retries (all attempt 1)
	// + 1 more attempt (attempt 2, consuming the task's own retry budget).
	wantInvocations := 1 + maxChannelInitAttempts + 1
	if len(allArgs) != wantInvocations {
		t.Fatalf("invocations = %d, want %d", len(allArgs), wantInvocations)
	}

	last := strings.Join(allArgs[len(allArgs)-1], " ")
	if !strings.Contains(last, "--resume "+seededSessionID) {
		t.Errorf("final attempt should still --resume the original session (channel-init failure isn't a session problem); args=%v", last)
	}
}

// TestRunInterruptStopsImmediatelyWithoutRetryOrNotify is the regression test
// for finding 1 (IMPORTANT): before this fix, a forwarded Ctrl-C
// (SIGINT/SIGTERM) killed the child, but Run's retry loop couldn't tell that
// apart from an ordinary failure — it cleared the session, respawned claude
// for any remaining retries, recorded history.ReasonFailure, and fired a
// notify-on-fail child, all *after* the user had already asked the task to
// stop.
//
// This drives Run() end-to-end (not just executeCommand) with a fake child
// that sleeps and traps nothing, so the only thing that can end it is the
// forwarded signal. The real test process is never signaled — signalNotifyFn
// is faked to hand back the channel executeCommand registers, and the test
// pushes a synthetic os.Signal into that channel directly.
func TestRunInterruptStopsImmediatelyWithoutRetryOrNotify(t *testing.T) {
	origExec := execCommand
	defer func() { execCommand = origExec }()

	origNotify := signalNotifyFn
	defer func() { signalNotifyFn = origNotify }()

	// Captures the channel executeCommand registers via signalNotifyFn, so
	// the test can push a fake signal into it once Run() is underway.
	registered := make(chan chan<- os.Signal, 1)
	signalNotifyFn = func(c chan<- os.Signal, sig ...os.Signal) {
		registered <- c
	}

	dir := t.TempDir()
	ws := filepath.Join(dir, "workspace")
	os.MkdirAll(ws, 0755)
	os.WriteFile(filepath.Join(ws, "task.md"), []byte("test prompt"), 0644)

	var invocations int
	execCommand = func(name string, args ...string) *exec.Cmd {
		invocations++
		// Traps nothing; the only thing that can end this is a forwarded
		// signal killing the process group.
		return exec.Command("sh", "-c", "sleep 30")
	}

	seededSessionID := "seeded-session-interrupt"
	sessions := session.NewStore(dir)
	if err := sessions.Set("task:mytask", seededSessionID); err != nil {
		t.Fatalf("seeding session: %v", err)
	}

	cfg := &config.Config{
		HomePath: dir,
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 15},
		Tasks: map[string]config.TaskConfig{
			"mytask": {
				PromptFile:   "task.md",
				Schedule:     "0 * * * *",
				Enabled:      true,
				Retries:      2, // plenty of retry budget the interrupt must preempt
				NotifyOnFail: true,
				Channels:     []string{"plugin:blackpaw-telegram@blackpaw-studio/claude-plugins"},
			},
		},
	}

	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- Run(cfg, "mytask", sessions)
	}()

	var sigCh chan<- os.Signal
	select {
	case sigCh = <-registered:
	case <-time.After(5 * time.Second):
		t.Fatal("signalNotifyFn was never invoked — executeCommand did not register a signal handler")
	}
	sigCh <- syscall.SIGINT

	var runErr error
	select {
	case runErr = <-runErrCh:
	case <-time.After(10 * time.Second):
		t.Fatal("Run() did not return after the interrupt — retry loop likely did not break immediately")
	}

	if runErr == nil {
		t.Fatal("expected Run() to return an error after an interrupt")
	}
	if !strings.Contains(runErr.Error(), "interrupted") {
		t.Errorf("expected error to mention interruption, got: %v", runErr)
	}

	if invocations != 1 {
		t.Errorf("invocations = %d, want 1 (no retry after interrupt)", invocations)
	}

	hist := history.NewStore(cfg.HomePath)
	entry := hist.Get("mytask")
	if entry == nil {
		t.Fatal("expected a history entry")
	}
	if entry.Reason != history.ReasonInterrupted {
		t.Errorf("history reason = %q, want %q", entry.Reason, history.ReasonInterrupted)
	}

	sid, _, err := sessions.Get("task:mytask")
	if err != nil {
		t.Fatalf("reading session store: %v", err)
	}
	if sid != seededSessionID {
		t.Errorf("session store should be untouched by an interrupt; got %q, want %q", sid, seededSessionID)
	}
}
