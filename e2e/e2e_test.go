//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
)

var (
	leoBin      string
	fakeclaude  string
)

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "leo-e2e-bin-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(tmp)

	leoBin = filepath.Join(tmp, "leo")
	fakeclaude = filepath.Join(tmp, "claude")

	// Build leo
	build := exec.Command("go", "build", "-o", leoBin, "./cmd/leo")
	build.Dir = findRepoRoot()
	if out, err := build.CombinedOutput(); err != nil {
		panic("failed to build leo: " + string(out))
	}

	// Build fakeclaude as "claude"
	build = exec.Command("go", "build", "-o", fakeclaude, "./e2e/fakeclaude")
	build.Dir = findRepoRoot()
	if out, err := build.CombinedOutput(); err != nil {
		panic("failed to build fakeclaude: " + string(out))
	}

	os.Exit(m.Run())
}

func findRepoRoot() string {
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			panic("could not find repo root")
		}
		dir = parent
	}
}

// runLeo executes the leo binary with the given args and environment overrides.
// It prepends the fakeclaude directory to PATH so leo finds our mock claude.
func runLeo(t *testing.T, dir string, env []string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(leoBin, args...)
	cmd.Dir = dir

	binDir := filepath.Dir(fakeclaude)
	baseEnv := append(os.Environ(), "PATH="+binDir+":"+os.Getenv("PATH"))
	cmd.Env = append(baseEnv, env...)

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("failed to run leo: %v", err)
		}
	}

	return stdout.String(), stderr.String(), exitCode
}

// readArgLog reads and parses the JSON arg log written by fakeclaude.
func readArgLog(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read arg log: %v", err)
	}
	var args []string
	if err := json.Unmarshal(data, &args); err != nil {
		t.Fatalf("failed to parse arg log: %v", err)
	}
	return args
}

// setupWorkspace creates a temp workspace with leo.yaml and prompt files.
// Returns the workspace dir and a cleanup function.
func setupWorkspace(t *testing.T, yamlContent string, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "leo.yaml"), []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	return dir
}

// argValue returns the value following a flag in an arg list, or empty string.
func argValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

const minimalConfig = `agent:
  name: test-agent
  workspace: WORKSPACE_PLACEHOLDER
telegram:
  bot_token: "fake-bot-token"
  chat_id: "12345"
defaults:
  model: sonnet
  max_turns: 15
  bypass_permissions: true
tasks:
  heartbeat:
    schedule: "0 9 * * *"
    prompt_file: prompts/HEARTBEAT.md
    enabled: true
    silent: true
`

func TestRunHappyPath(t *testing.T) {
	argLog := filepath.Join(t.TempDir(), "args.json")

	ws := setupWorkspace(t,
		minimalConfig,
		map[string]string{
			"prompts/HEARTBEAT.md": "Check in with the user.\n",
		},
	)

	// Fix workspace to point to itself (since config loads workspace as-is)
	fixWorkspaceInConfig(t, ws)

	_, stderr, code := runLeo(t, ws, []string{
		"FAKECLAUDE_SCENARIO=success",
		"FAKECLAUDE_ARGLOG=" + argLog,
	}, "run", "heartbeat", "-c", filepath.Join(ws, "leo.yaml"))

	if code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, stderr)
	}

	args := readArgLog(t, argLog)

	// Verify key flags
	if v := argValue(args, "--agent"); v != "test-agent" {
		t.Errorf("expected --agent test-agent, got %q", v)
	}
	if v := argValue(args, "--model"); v != "sonnet" {
		t.Errorf("expected --model sonnet, got %q", v)
	}
	if v := argValue(args, "--max-turns"); v != "15" {
		t.Errorf("expected --max-turns 15, got %q", v)
	}
	if v := argValue(args, "--output-format"); v != "json" {
		t.Errorf("expected --output-format json, got %q", v)
	}
	if !slices.Contains(args, "--dangerously-skip-permissions") {
		t.Error("expected --dangerously-skip-permissions flag")
	}

	// Verify prompt contains the prompt file content
	prompt := argValue(args, "-p")
	if !strings.Contains(prompt, "Check in with the user.") {
		t.Error("prompt should contain prompt file content")
	}

	// Verify silent preamble is present
	if !strings.Contains(prompt, "SILENT SCHEDULED RUN") {
		t.Error("prompt should contain silent preamble for silent task")
	}

	// Verify Telegram protocol is injected
	if !strings.Contains(prompt, "fake-bot-token") {
		t.Error("prompt should contain Telegram bot token")
	}
	if !strings.Contains(prompt, "12345") {
		t.Error("prompt should contain Telegram chat ID")
	}

	// Verify log file was written
	logPath := filepath.Join(ws, "state", "heartbeat.log")
	if _, err := os.Stat(logPath); err != nil {
		t.Errorf("expected log file at %s: %v", logPath, err)
	}
}

func TestRunWithMCPConfig(t *testing.T) {
	argLog := filepath.Join(t.TempDir(), "args.json")

	ws := setupWorkspace(t, "", map[string]string{
		"prompts/HEARTBEAT.md":        "Hello.\n",
		"config/mcp-servers.json":     `{"servers":{}}`,
	})
	fixWorkspaceInConfig(t, ws)

	_, _, code := runLeo(t, ws, []string{
		"FAKECLAUDE_SCENARIO=success",
		"FAKECLAUDE_ARGLOG=" + argLog,
	}, "run", "heartbeat", "-c", filepath.Join(ws, "leo.yaml"))

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}

	args := readArgLog(t, argLog)
	mcpPath := argValue(args, "--mcp-config")
	if mcpPath == "" {
		t.Fatal("expected --mcp-config to be present when config/mcp-servers.json exists")
	}
	if !strings.HasSuffix(mcpPath, "config/mcp-servers.json") {
		t.Errorf("unexpected --mcp-config value: %s", mcpPath)
	}
}

func TestRunWithoutMCPConfig(t *testing.T) {
	argLog := filepath.Join(t.TempDir(), "args.json")

	ws := setupWorkspace(t, "", map[string]string{
		"prompts/HEARTBEAT.md": "Hello.\n",
	})
	fixWorkspaceInConfig(t, ws)

	_, _, code := runLeo(t, ws, []string{
		"FAKECLAUDE_SCENARIO=success",
		"FAKECLAUDE_ARGLOG=" + argLog,
	}, "run", "heartbeat", "-c", filepath.Join(ws, "leo.yaml"))

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}

	args := readArgLog(t, argLog)
	if argValue(args, "--mcp-config") != "" {
		t.Error("--mcp-config should not be present when no config file exists")
	}
}

func TestRunTaskNotFound(t *testing.T) {
	ws := setupWorkspace(t, "", map[string]string{
		"prompts/HEARTBEAT.md": "Hello.\n",
	})
	fixWorkspaceInConfig(t, ws)

	_, _, code := runLeo(t, ws, nil,
		"run", "nonexistent", "-c", filepath.Join(ws, "leo.yaml"))

	if code == 0 {
		t.Fatal("expected non-zero exit code for unknown task")
	}
}

func TestRunMissingPromptFile(t *testing.T) {
	ws := setupWorkspace(t, "", nil) // no prompt file
	fixWorkspaceInConfig(t, ws)

	_, _, code := runLeo(t, ws, nil,
		"run", "heartbeat", "-c", filepath.Join(ws, "leo.yaml"))

	if code == 0 {
		t.Fatal("expected non-zero exit code when prompt file is missing")
	}
}

func TestRunClaudeError(t *testing.T) {
	ws := setupWorkspace(t, "", map[string]string{
		"prompts/HEARTBEAT.md": "Hello.\n",
	})
	fixWorkspaceInConfig(t, ws)

	_, _, code := runLeo(t, ws, []string{
		"FAKECLAUDE_SCENARIO=error",
	}, "run", "heartbeat", "-c", filepath.Join(ws, "leo.yaml"))

	if code == 0 {
		t.Fatal("expected non-zero exit code when claude fails")
	}

	// Log should still be written even on error
	logPath := filepath.Join(ws, "state", "heartbeat.log")
	if _, err := os.Stat(logPath); err != nil {
		t.Errorf("expected log file written even on error: %v", err)
	}
}

func TestRunModelOverride(t *testing.T) {
	argLog := filepath.Join(t.TempDir(), "args.json")

	configYAML := `agent:
  name: test-agent
  workspace: %s
telegram:
  bot_token: "tok"
  chat_id: "1"
defaults:
  model: sonnet
  max_turns: 15
tasks:
  heartbeat:
    schedule: "0 9 * * *"
    prompt_file: prompts/HEARTBEAT.md
    model: opus
    enabled: true
`

	ws := setupWorkspace(t, configYAML, map[string]string{
		"prompts/HEARTBEAT.md": "Hello.\n",
	})
	fixWorkspaceInConfig(t, ws)

	_, _, code := runLeo(t, ws, []string{
		"FAKECLAUDE_SCENARIO=success",
		"FAKECLAUDE_ARGLOG=" + argLog,
	}, "run", "heartbeat", "-c", filepath.Join(ws, "leo.yaml"))

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}

	args := readArgLog(t, argLog)
	if v := argValue(args, "--model"); v != "opus" {
		t.Errorf("expected --model opus (per-task override), got %q", v)
	}
}

func TestRunMaxTurnsDefault(t *testing.T) {
	argLog := filepath.Join(t.TempDir(), "args.json")

	ws := setupWorkspace(t, "", map[string]string{
		"prompts/HEARTBEAT.md": "Hello.\n",
	})
	fixWorkspaceInConfig(t, ws)

	_, _, code := runLeo(t, ws, []string{
		"FAKECLAUDE_SCENARIO=success",
		"FAKECLAUDE_ARGLOG=" + argLog,
	}, "run", "heartbeat", "-c", filepath.Join(ws, "leo.yaml"))

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}

	args := readArgLog(t, argLog)
	if v := argValue(args, "--max-turns"); v != "15" {
		t.Errorf("expected --max-turns 15 from defaults, got %q", v)
	}
}

func TestVersionCommand(t *testing.T) {
	stdout, _, code := runLeo(t, t.TempDir(), nil, "version")

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.HasPrefix(stdout, "leo ") {
		t.Errorf("expected output starting with 'leo ', got: %s", stdout)
	}
}

func TestRunGroupIDOverridesChatID(t *testing.T) {
	argLog := filepath.Join(t.TempDir(), "args.json")

	configYAML := `agent:
  name: test-agent
  workspace: %s
telegram:
  bot_token: "tok"
  chat_id: "111"
  group_id: "222"
defaults:
  model: sonnet
  max_turns: 10
tasks:
  heartbeat:
    schedule: "0 9 * * *"
    prompt_file: prompts/HEARTBEAT.md
    enabled: true
`

	ws := setupWorkspace(t, configYAML, map[string]string{
		"prompts/HEARTBEAT.md": "Hello.\n",
	})
	fixWorkspaceInConfig(t, ws)

	_, _, code := runLeo(t, ws, []string{
		"FAKECLAUDE_SCENARIO=success",
		"FAKECLAUDE_ARGLOG=" + argLog,
	}, "run", "heartbeat", "-c", filepath.Join(ws, "leo.yaml"))

	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}

	args := readArgLog(t, argLog)
	prompt := argValue(args, "-p")
	if !strings.Contains(prompt, "222") {
		t.Error("prompt should use group_id when present")
	}
}

func TestConfigNotFound(t *testing.T) {
	emptyDir := t.TempDir()

	_, _, code := runLeo(t, emptyDir, nil, "run", "heartbeat")

	if code == 0 {
		t.Fatal("expected non-zero exit code when no config exists")
	}
}

func TestDaemonIPC(t *testing.T) {
	// Use /tmp to stay well under macOS 104-char Unix socket path limit.
	dir, err := os.MkdirTemp("/tmp", "leo-e2e-*")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	// Create state directory for the socket.
	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(stateDir, 0750); err != nil {
		t.Fatalf("creating state dir: %v", err)
	}

	// Write leo.yaml with the temp dir as workspace.
	cfgYAML := fmt.Sprintf(`agent:
  name: test-agent
  workspace: %s
telegram:
  bot_token: "fake-token"
  chat_id: "12345"
defaults:
  model: sonnet
  max_turns: 15
tasks:
  heartbeat:
    schedule: "0 9 * * *"
    prompt_file: HEARTBEAT.md
    enabled: true
`, dir)

	cfgPath := filepath.Join(dir, "leo.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0600); err != nil {
		t.Fatalf("writing leo.yaml: %v", err)
	}

	sockPath := filepath.Join(stateDir, "leo.sock")

	// Daemon should not be running yet.
	if daemon.IsRunning(dir) {
		t.Fatal("expected daemon not running before start")
	}

	// Start the daemon server.
	srv := daemon.New(sockPath, cfgPath)
	if err := srv.Start(); err != nil {
		t.Fatalf("starting daemon: %v", err)
	}
	t.Cleanup(func() { srv.Shutdown() }) //nolint:errcheck

	// Give the goroutine a moment to begin accepting.
	deadline := time.Now().Add(2 * time.Second)
	for !daemon.IsRunning(dir) {
		if time.Now().After(deadline) {
			t.Fatal("daemon did not become ready within 2s")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// GET /task/list — should include the "heartbeat" task from config.
	resp, err := daemon.Send(dir, "GET", "/task/list", nil)
	if err != nil {
		t.Fatalf("task/list: %v", err)
	}
	if !resp.OK {
		t.Fatalf("task/list not OK: %s", resp.Error)
	}
	var tasks map[string]config.TaskConfig
	if err := json.Unmarshal(resp.Data, &tasks); err != nil {
		t.Fatalf("unmarshaling task list: %v", err)
	}
	if _, ok := tasks["heartbeat"]; !ok {
		t.Error("expected heartbeat task in list")
	}

	// POST /task/add — add a "news" task.
	addResp, err := daemon.Send(dir, "POST", "/task/add", daemon.TaskAddRequest{
		Name:       "news",
		Schedule:   "0 8 * * *",
		PromptFile: "NEWS.md",
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("task/add: %v", err)
	}
	if !addResp.OK {
		t.Fatalf("task/add not OK: %s", addResp.Error)
	}

	// Verify "news" task persisted on disk.
	diskCfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("loading config after add: %v", err)
	}
	newsTask, ok := diskCfg.Tasks["news"]
	if !ok {
		t.Fatal("expected news task in config after add")
	}
	if newsTask.Schedule != "0 8 * * *" {
		t.Errorf("expected schedule '0 8 * * *', got %q", newsTask.Schedule)
	}
	if newsTask.PromptFile != "NEWS.md" {
		t.Errorf("expected prompt_file 'NEWS.md', got %q", newsTask.PromptFile)
	}
	if !newsTask.Enabled {
		t.Error("expected news task to be enabled")
	}

	// POST /task/disable — disable "news".
	disResp, err := daemon.Send(dir, "POST", "/task/disable", daemon.TaskNameRequest{Name: "news"})
	if err != nil {
		t.Fatalf("task/disable: %v", err)
	}
	if !disResp.OK {
		t.Fatalf("task/disable not OK: %s", disResp.Error)
	}

	// Verify disabled on disk.
	diskCfg, err = config.Load(cfgPath)
	if err != nil {
		t.Fatalf("loading config after disable: %v", err)
	}
	if diskCfg.Tasks["news"].Enabled {
		t.Error("expected news task to be disabled after disable call")
	}

	// POST /task/remove — remove "news".
	rmResp, err := daemon.Send(dir, "POST", "/task/remove", daemon.TaskNameRequest{Name: "news"})
	if err != nil {
		t.Fatalf("task/remove: %v", err)
	}
	if !rmResp.OK {
		t.Fatalf("task/remove not OK: %s", rmResp.Error)
	}

	// Verify removed from disk.
	diskCfg, err = config.Load(cfgPath)
	if err != nil {
		t.Fatalf("loading config after remove: %v", err)
	}
	if _, ok := diskCfg.Tasks["news"]; ok {
		t.Error("expected news task to be removed from config")
	}

	// Shutdown server.
	if err := srv.Shutdown(); err != nil {
		t.Errorf("shutting down daemon: %v", err)
	}

	// IsRunning should now return false.
	if daemon.IsRunning(dir) {
		t.Error("expected daemon not running after shutdown")
	}
}

// fixWorkspaceInConfig writes the config file with the workspace path set.
// If the config is empty, it uses the minimal default config.
func fixWorkspaceInConfig(t *testing.T, ws string) {
	t.Helper()
	cfgPath := filepath.Join(ws, "leo.yaml")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	content := string(data)
	if content == "" {
		content = minimalConfig
	}

	content = strings.ReplaceAll(content, "WORKSPACE_PLACEHOLDER", ws)
	content = strings.ReplaceAll(content, "%s", ws)
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
