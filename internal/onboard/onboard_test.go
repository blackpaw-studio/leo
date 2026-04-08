package onboard

import (
	"bufio"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/prereq"
)

func TestSmokeTestSuccess(t *testing.T) {
	original := execCommand
	defer func() { execCommand = original }()

	execCommand = func(name string, args ...string) *exec.Cmd {
		// Verify the right command is being built
		if name != "claude" {
			t.Errorf("command = %q, want %q", name, "claude")
		}
		// Use a real command that echoes the expected output
		return exec.Command("echo", "LEO_SMOKE_OK")
	}

	err := SmokeTest()
	if err != nil {
		t.Fatalf("SmokeTest() error: %v", err)
	}
}

func TestSmokeTestBadOutput(t *testing.T) {
	original := execCommand
	defer func() { execCommand = original }()

	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("echo", "something else")
	}

	err := SmokeTest()
	if err == nil {
		t.Fatal("expected error for bad output")
	}

	if !strings.Contains(err.Error(), "unexpected") {
		t.Errorf("error = %q, want to contain 'unexpected'", err.Error())
	}
}

func TestSmokeTestCommandFailure(t *testing.T) {
	original := execCommand
	defer func() { execCommand = original }()

	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("false")
	}

	err := SmokeTest()
	if err == nil {
		t.Fatal("expected error for command failure")
	}

	if !strings.Contains(err.Error(), "smoke test failed") {
		t.Errorf("error = %q, want to contain 'smoke test failed'", err.Error())
	}
}

func TestReconfigureTasksAddHeartbeat(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "leo.yaml")

	cfg := &config.Config{
		Agent: config.AgentConfig{
			Name:      "test",
			Workspace: dir,
		},
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 15},
		Tasks:    make(map[string]config.TaskConfig),
	}

	// Save initial config
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}

	// Simulate: "y" to add heartbeat
	reader := bufio.NewReader(strings.NewReader("y\n"))

	err := reconfigureTasks(reader, cfg, dir)
	if err != nil {
		t.Fatalf("reconfigureTasks() error: %v", err)
	}

	if !cfg.Heartbeat.Enabled {
		t.Error("expected heartbeat to be enabled")
	}

	// Verify it was saved
	loaded, err := config.LoadFromWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Heartbeat.Enabled {
		t.Error("heartbeat should be persisted as enabled")
	}
}

func TestReconfigureTasksSkipExisting(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "leo.yaml")

	cfg := &config.Config{
		Agent: config.AgentConfig{
			Name:      "test",
			Workspace: dir,
		},
		Heartbeat: config.HeartbeatConfig{
			Enabled:  true,
			Interval: "30m",
		},
		Tasks: map[string]config.TaskConfig{},
	}

	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}

	reader := bufio.NewReader(strings.NewReader("\n"))

	err := reconfigureTasks(reader, cfg, dir)
	if err != nil {
		t.Fatalf("reconfigureTasks() error: %v", err)
	}
}

func TestReconfigureSingleWorkspace(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "leo.yaml")

	cfg := &config.Config{
		Agent: config.AgentConfig{
			Name:      "test",
			Workspace: dir,
		},
		Telegram: config.TelegramConfig{
			BotToken: "token",
			ChatID:   "123",
		},
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 15},
		Tasks:    map[string]config.TaskConfig{},
	}

	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}

	// Choose option 2 (tasks), then "n" to skip heartbeat
	reader := bufio.NewReader(strings.NewReader("2\nn\n"))

	err := reconfigure(reader, []string{dir})
	if err != nil {
		t.Fatalf("reconfigure() error: %v", err)
	}
}

func TestReconfigureTelegramUpdate(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "leo.yaml")

	cfg := &config.Config{
		Agent: config.AgentConfig{
			Name:      "test",
			Workspace: dir,
		},
		Telegram: config.TelegramConfig{
			BotToken: "old-token",
			ChatID:   "old-chat",
		},
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 15},
		Tasks:    map[string]config.TaskConfig{},
	}

	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}

	// Enter new token, chat ID, no group, and "n" to skip test message
	reader := bufio.NewReader(strings.NewReader("new-token\nnew-chat\n\nn\n"))

	err := reconfigureTelegram(reader, cfg, dir)
	if err != nil {
		t.Fatalf("reconfigureTelegram() error: %v", err)
	}

	if cfg.Telegram.BotToken != "new-token" {
		t.Errorf("BotToken = %q, want %q", cfg.Telegram.BotToken, "new-token")
	}
	if cfg.Telegram.ChatID != "new-chat" {
		t.Errorf("ChatID = %q, want %q", cfg.Telegram.ChatID, "new-chat")
	}

	// Verify saved
	loaded, err := config.LoadFromWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Telegram.BotToken != "new-token" {
		t.Error("new token should be persisted")
	}
}

// --- Run() tests ---

func stubDefaults(t *testing.T) {
	t.Helper()
	origCheck := checkClaudeFn
	origOC := findOpenClawFn
	origWS := findWorkspacesFn
	origSetup := setupInteractiveFn
	origMigrate := migrateInteractiveFn
	origReader := newReaderFn
	t.Cleanup(func() {
		checkClaudeFn = origCheck
		findOpenClawFn = origOC
		findWorkspacesFn = origWS
		setupInteractiveFn = origSetup
		migrateInteractiveFn = origMigrate
		newReaderFn = origReader
	})
}

func TestRunClaudeNotFound(t *testing.T) {
	stubDefaults(t)
	newReaderFn = func() *bufio.Reader {
		return bufio.NewReader(strings.NewReader("\n"))
	}
	checkClaudeFn = func() prereq.ClaudeResult {
		return prereq.ClaudeResult{OK: false}
	}

	err := Run()
	if err == nil {
		t.Fatal("expected error when claude not found")
	}
	if !strings.Contains(err.Error(), "claude CLI not found") {
		t.Errorf("error = %q, want to contain 'claude CLI not found'", err.Error())
	}
}

func TestRunNothingFound(t *testing.T) {
	stubDefaults(t)
	newReaderFn = func() *bufio.Reader {
		return bufio.NewReader(strings.NewReader("\n"))
	}
	checkClaudeFn = func() prereq.ClaudeResult {
		return prereq.ClaudeResult{OK: true, Version: "1.0.0"}
	}
	findOpenClawFn = func() string { return "" }
	findWorkspacesFn = func() []string { return nil }

	var setupCalled bool
	setupInteractiveFn = func(r *bufio.Reader) error {
		setupCalled = true
		return nil
	}

	err := Run()
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if !setupCalled {
		t.Error("expected setupInteractiveFn to be called")
	}
}

func TestRunBothFound_FreshSetup(t *testing.T) {
	stubDefaults(t)
	newReaderFn = func() *bufio.Reader {
		return bufio.NewReader(strings.NewReader("1\n"))
	}
	checkClaudeFn = func() prereq.ClaudeResult {
		return prereq.ClaudeResult{OK: true, Version: "1.0.0"}
	}
	findOpenClawFn = func() string { return "/tmp/openclaw" }
	findWorkspacesFn = func() []string { return []string{"/tmp/ws"} }

	var setupCalled bool
	setupInteractiveFn = func(r *bufio.Reader) error {
		setupCalled = true
		return nil
	}

	err := Run()
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if !setupCalled {
		t.Error("expected setupInteractiveFn to be called for fresh setup")
	}
}

func TestRunBothFound_Migrate(t *testing.T) {
	stubDefaults(t)
	newReaderFn = func() *bufio.Reader {
		return bufio.NewReader(strings.NewReader("2\n"))
	}
	checkClaudeFn = func() prereq.ClaudeResult {
		return prereq.ClaudeResult{OK: true}
	}
	findOpenClawFn = func() string { return "/tmp/openclaw" }
	findWorkspacesFn = func() []string { return []string{"/tmp/ws"} }

	var migrateCalled bool
	migrateInteractiveFn = func(r *bufio.Reader) error {
		migrateCalled = true
		return nil
	}

	err := Run()
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if !migrateCalled {
		t.Error("expected migrateInteractiveFn to be called")
	}
}

func TestRunOnlyOpenClaw_Migrate(t *testing.T) {
	stubDefaults(t)
	newReaderFn = func() *bufio.Reader {
		return bufio.NewReader(strings.NewReader("1\n"))
	}
	checkClaudeFn = func() prereq.ClaudeResult {
		return prereq.ClaudeResult{OK: true}
	}
	findOpenClawFn = func() string { return "/tmp/openclaw" }
	findWorkspacesFn = func() []string { return nil }

	var migrateCalled bool
	migrateInteractiveFn = func(r *bufio.Reader) error {
		migrateCalled = true
		return nil
	}

	err := Run()
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if !migrateCalled {
		t.Error("expected migrateInteractiveFn to be called for migrate")
	}
}

func TestRunOnlyOpenClaw_FreshSetup(t *testing.T) {
	stubDefaults(t)
	newReaderFn = func() *bufio.Reader {
		return bufio.NewReader(strings.NewReader("2\n"))
	}
	checkClaudeFn = func() prereq.ClaudeResult {
		return prereq.ClaudeResult{OK: true}
	}
	findOpenClawFn = func() string { return "/tmp/openclaw" }
	findWorkspacesFn = func() []string { return nil }

	var setupCalled bool
	setupInteractiveFn = func(r *bufio.Reader) error {
		setupCalled = true
		return nil
	}

	err := Run()
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if !setupCalled {
		t.Error("expected setupInteractiveFn to be called")
	}
}

func TestRunOnlyWorkspaces_Reconfigure(t *testing.T) {
	stubDefaults(t)

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "leo.yaml")
	cfg := &config.Config{
		Agent:    config.AgentConfig{Name: "test", Workspace: dir},
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 15},
		Tasks:    map[string]config.TaskConfig{},
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}

	// Choose reconfigure (1), then tasks (2), then skip heartbeat (n)
	newReaderFn = func() *bufio.Reader {
		return bufio.NewReader(strings.NewReader("1\n2\nn\n"))
	}
	checkClaudeFn = func() prereq.ClaudeResult {
		return prereq.ClaudeResult{OK: true}
	}
	findOpenClawFn = func() string { return "" }
	findWorkspacesFn = func() []string { return []string{dir} }

	err := Run()
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
}

func TestRunOnlyWorkspaces_FreshSetup(t *testing.T) {
	stubDefaults(t)
	newReaderFn = func() *bufio.Reader {
		return bufio.NewReader(strings.NewReader("2\n"))
	}
	checkClaudeFn = func() prereq.ClaudeResult {
		return prereq.ClaudeResult{OK: true}
	}
	findOpenClawFn = func() string { return "" }
	findWorkspacesFn = func() []string { return []string{"/tmp/ws"} }

	var setupCalled bool
	setupInteractiveFn = func(r *bufio.Reader) error {
		setupCalled = true
		return nil
	}

	err := Run()
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if !setupCalled {
		t.Error("expected setupInteractiveFn to be called")
	}
}

func TestRunClaudeVersionEmpty(t *testing.T) {
	stubDefaults(t)
	newReaderFn = func() *bufio.Reader {
		return bufio.NewReader(strings.NewReader("\n"))
	}
	checkClaudeFn = func() prereq.ClaudeResult {
		return prereq.ClaudeResult{OK: true, Version: ""}
	}
	findOpenClawFn = func() string { return "" }
	findWorkspacesFn = func() []string { return nil }
	setupInteractiveFn = func(r *bufio.Reader) error { return nil }

	err := Run()
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
}

// --- reconfigure() tests ---

func TestReconfigureMultipleWorkspaces(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	for _, d := range []string{dir1, dir2} {
		cfg := &config.Config{
			Agent:    config.AgentConfig{Name: "test", Workspace: d},
			Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 15},
			Tasks:    map[string]config.TaskConfig{},
		}
		if err := config.Save(filepath.Join(d, "leo.yaml"), cfg); err != nil {
			t.Fatal(err)
		}
	}

	// Choose workspace 2, then tasks (2), then skip heartbeat (n)
	reader := bufio.NewReader(strings.NewReader("2\n2\nn\n"))

	err := reconfigure(reader, []string{dir1, dir2})
	if err != nil {
		t.Fatalf("reconfigure() error: %v", err)
	}
}

func TestReconfigureTelegramChoice(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		Agent:    config.AgentConfig{Name: "test", Workspace: dir},
		Telegram: config.TelegramConfig{BotToken: "tok", ChatID: "123"},
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 15},
		Tasks:    map[string]config.TaskConfig{},
	}
	if err := config.Save(filepath.Join(dir, "leo.yaml"), cfg); err != nil {
		t.Fatal(err)
	}

	origSend := sendMessageFn
	defer func() { sendMessageFn = origSend }()
	sendMessageFn = func(token, chatID, text string, topicID int) error { return nil }

	// Choose telegram (1), then enter new values, skip test message
	reader := bufio.NewReader(strings.NewReader("1\nnewtoken\nnewchat\n\nn\n"))

	err := reconfigure(reader, []string{dir})
	if err != nil {
		t.Fatalf("reconfigure() error: %v", err)
	}

	loaded, err := config.LoadFromWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Telegram.BotToken != "newtoken" {
		t.Errorf("BotToken = %q, want %q", loaded.Telegram.BotToken, "newtoken")
	}
}

func TestReconfigureFullSetup(t *testing.T) {
	stubDefaults(t)

	dir := t.TempDir()
	cfg := &config.Config{
		Agent:    config.AgentConfig{Name: "test", Workspace: dir},
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 15},
		Tasks:    map[string]config.TaskConfig{},
	}
	if err := config.Save(filepath.Join(dir, "leo.yaml"), cfg); err != nil {
		t.Fatal(err)
	}

	var setupCalled bool
	setupInteractiveFn = func(r *bufio.Reader) error {
		setupCalled = true
		return nil
	}

	// Choose full setup (3)
	reader := bufio.NewReader(strings.NewReader("3\n"))

	err := reconfigure(reader, []string{dir})
	if err != nil {
		t.Fatalf("reconfigure() error: %v", err)
	}
	if !setupCalled {
		t.Error("expected setupInteractiveFn to be called for full setup")
	}
}

// --- reconfigureTelegram() tests ---

func TestReconfigureTelegramWithTestMessage(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		Agent:    config.AgentConfig{Name: "test", Workspace: dir},
		Telegram: config.TelegramConfig{BotToken: "old-token", ChatID: "old-chat"},
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 15},
		Tasks:    map[string]config.TaskConfig{},
	}
	if err := config.Save(filepath.Join(dir, "leo.yaml"), cfg); err != nil {
		t.Fatal(err)
	}

	origSend := sendMessageFn
	defer func() { sendMessageFn = origSend }()

	var sentToken, sentChat, sentText string
	sendMessageFn = func(token, chatID, text string, topicID int) error {
		sentToken = token
		sentChat = chatID
		sentText = text
		return nil
	}

	// Enter new token, chat ID, no group, yes to test message
	reader := bufio.NewReader(strings.NewReader("newtoken\nnewchat\n\ny\n"))

	err := reconfigureTelegram(reader, cfg, dir)
	if err != nil {
		t.Fatalf("reconfigureTelegram() error: %v", err)
	}

	if sentToken != "newtoken" {
		t.Errorf("sentToken = %q, want %q", sentToken, "newtoken")
	}
	if sentChat != "newchat" {
		t.Errorf("sentChat = %q, want %q", sentChat, "newchat")
	}
	if sentText == "" {
		t.Error("expected test message text to be non-empty")
	}
}

func TestReconfigureTelegramWithGroupID(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		Agent:    config.AgentConfig{Name: "test", Workspace: dir},
		Telegram: config.TelegramConfig{},
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 15},
		Tasks:    map[string]config.TaskConfig{},
	}
	if err := config.Save(filepath.Join(dir, "leo.yaml"), cfg); err != nil {
		t.Fatal(err)
	}

	origSend := sendMessageFn
	defer func() { sendMessageFn = origSend }()

	var sentChat string
	sendMessageFn = func(token, chatID, text string, topicID int) error {
		sentChat = chatID
		return nil
	}

	// token, chat, group, yes to test
	reader := bufio.NewReader(strings.NewReader("tok\nchat123\ngroup456\ny\n"))

	err := reconfigureTelegram(reader, cfg, dir)
	if err != nil {
		t.Fatalf("reconfigureTelegram() error: %v", err)
	}

	// When group is set, test message should use group as effective chat ID
	if sentChat != "group456" {
		t.Errorf("sentChat = %q, want %q (should use groupID)", sentChat, "group456")
	}
}

func TestReconfigureTelegramSendFails(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		Agent:    config.AgentConfig{Name: "test", Workspace: dir},
		Telegram: config.TelegramConfig{},
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 15},
		Tasks:    map[string]config.TaskConfig{},
	}
	if err := config.Save(filepath.Join(dir, "leo.yaml"), cfg); err != nil {
		t.Fatal(err)
	}

	origSend := sendMessageFn
	defer func() { sendMessageFn = origSend }()

	sendMessageFn = func(token, chatID, text string, topicID int) error {
		return fmt.Errorf("network error")
	}

	// token, chat, no group, yes to test
	reader := bufio.NewReader(strings.NewReader("tok\nchat\n\ny\n"))

	// Should not return error even though send fails (it just warns)
	err := reconfigureTelegram(reader, cfg, dir)
	if err != nil {
		t.Fatalf("reconfigureTelegram() should not error on send failure: %v", err)
	}
}

func TestSmokeTestArgs(t *testing.T) {
	original := execCommand
	defer func() { execCommand = original }()

	var gotArgs []string
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotArgs = args
		return exec.Command("echo", "LEO_SMOKE_OK")
	}

	SmokeTest()

	expected := []string{"-p", "Reply with exactly: LEO_SMOKE_OK", "--max-turns", "1", "--output-format", "text"}
	if len(gotArgs) != len(expected) {
		t.Fatalf("args count = %d, want %d", len(gotArgs), len(expected))
	}

	for i, arg := range expected {
		if gotArgs[i] != arg {
			t.Errorf("args[%d] = %q, want %q", i, gotArgs[i], arg)
		}
	}
}
