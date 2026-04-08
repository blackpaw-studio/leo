package setup

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/prereq"
	"github.com/blackpaw-studio/leo/internal/service"
	"github.com/blackpaw-studio/leo/internal/templates"
)

// --- helpers ---

func mockAllPrereqs(t *testing.T) {
	t.Helper()
	origClaude := checkClaudeFn
	origTmux := checkTmuxFn
	origBun := checkBunFn
	t.Cleanup(func() {
		checkClaudeFn = origClaude
		checkTmuxFn = origTmux
		checkBunFn = origBun
	})
	checkClaudeFn = func() prereq.ClaudeResult { return prereq.ClaudeResult{OK: true, Version: "1.0.0"} }
	checkTmuxFn = func() bool { return true }
	checkBunFn = func() bool { return true }
}

func readerFrom(input string) *bufio.Reader {
	return bufio.NewReader(strings.NewReader(input))
}

// --- findExistingConfig ---

func TestFindExistingConfigNone(t *testing.T) {
	dir := t.TempDir()

	orig := findExistingWorkspacesFn
	t.Cleanup(func() { findExistingWorkspacesFn = orig })
	findExistingWorkspacesFn = func() []string { return nil }

	cfg, defaultWs := findExistingConfig(dir)

	if cfg != nil {
		t.Error("expected nil config when none exists")
	}
	if defaultWs != filepath.Join(dir, ".leo") {
		t.Errorf("defaultWorkspace = %q, want %q", defaultWs, filepath.Join(dir, ".leo"))
	}
}

func TestFindExistingConfigFound(t *testing.T) {
	dir := t.TempDir()
	leoDir := filepath.Join(dir, ".leo")
	os.MkdirAll(leoDir, 0755)

	cfg := &config.Config{
		Agent: config.AgentConfig{
			Name:      "test",
			Workspace: leoDir,
		},
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 15},
		Tasks:    map[string]config.TaskConfig{},
	}
	config.Save(filepath.Join(leoDir, "leo.yaml"), cfg)

	orig := findExistingWorkspacesFn
	t.Cleanup(func() { findExistingWorkspacesFn = orig })
	findExistingWorkspacesFn = func() []string { return nil }

	found, ws := findExistingConfig(dir)
	if found == nil {
		t.Fatal("expected to find existing config")
	}
	if found.Agent.Name != "test" {
		t.Errorf("agent name = %q, want %q", found.Agent.Name, "test")
	}
	if ws != leoDir {
		t.Errorf("workspace = %q, want %q", ws, leoDir)
	}
}

func TestFindExistingConfigViaWorkspaces(t *testing.T) {
	dir := t.TempDir()
	// No .leo dir at home, but a workspace elsewhere
	wsDir := filepath.Join(dir, "other-workspace")
	os.MkdirAll(wsDir, 0755)

	cfg := &config.Config{
		Agent: config.AgentConfig{
			Name:      "found-via-ws",
			Workspace: wsDir,
		},
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 15},
		Tasks:    map[string]config.TaskConfig{},
	}
	config.Save(filepath.Join(wsDir, "leo.yaml"), cfg)

	orig := findExistingWorkspacesFn
	t.Cleanup(func() { findExistingWorkspacesFn = orig })
	findExistingWorkspacesFn = func() []string { return []string{wsDir} }

	found, _ := findExistingConfig(dir)
	if found == nil {
		t.Fatal("expected to find config via workspaces")
	}
	if found.Agent.Name != "found-via-ws" {
		t.Errorf("agent name = %q, want %q", found.Agent.Name, "found-via-ws")
	}
}

// --- scaffoldWorkspace ---

func TestScaffoldWorkspace(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	agentDir := filepath.Join(home, ".claude", "agents")
	agentPath := filepath.Join(agentDir, "test.md")

	cfg := &config.Config{
		Agent: config.AgentConfig{
			Name:      "test",
			Workspace: dir,
		},
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 15},
		Tasks:    map[string]config.TaskConfig{},
	}

	err := scaffoldWorkspace(scaffoldOptions{
		workspace: dir, home: home, name: "test", cfg: cfg,
		agentDir: agentDir, agentPath: agentPath, agentContent: "# Test Agent",
		userPath: filepath.Join(dir, "USER.md"), userName: "TestUser",
		role: "developer", about: "about me", preferences: "concise", timezone: "UTC",
	})
	if err != nil {
		t.Fatalf("scaffoldWorkspace() error: %v", err)
	}

	// Verify directories created
	for _, subdir := range []string{"daily", "reports", "state", "config", "scripts"} {
		if _, err := os.Stat(filepath.Join(dir, subdir)); err != nil {
			t.Errorf("directory %s not created", subdir)
		}
	}

	// Verify leo.yaml
	if _, err := os.Stat(filepath.Join(dir, "leo.yaml")); err != nil {
		t.Error("leo.yaml not created")
	}

	// Verify agent file
	data, err := os.ReadFile(agentPath)
	if err != nil {
		t.Fatal("agent file not created")
	}
	if string(data) != "# Test Agent" {
		t.Errorf("agent content = %q", string(data))
	}

	// Verify USER.md
	data, err = os.ReadFile(filepath.Join(dir, "USER.md"))
	if err != nil {
		t.Fatal("USER.md not created")
	}
	if len(data) == 0 {
		t.Error("USER.md is empty")
	}

	// Verify HEARTBEAT.md
	if _, err := os.Stat(filepath.Join(dir, "HEARTBEAT.md")); err != nil {
		t.Error("HEARTBEAT.md not created")
	}

	// Verify MCP config
	if _, err := os.Stat(filepath.Join(dir, "config", "mcp-servers.json")); err != nil {
		t.Error("mcp-servers.json not created")
	}

	// Verify CLAUDE.md
	data, err = os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if err != nil {
		t.Fatal("CLAUDE.md not created")
	}
	if len(data) == 0 {
		t.Error("CLAUDE.md is empty")
	}

	// Verify skills directory and files
	for _, skill := range templates.SkillFiles() {
		if _, err := os.Stat(filepath.Join(dir, "skills", skill)); err != nil {
			t.Errorf("skill file %s not created", skill)
		}
	}
}

func TestScaffoldWorkspaceSkipsExisting(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	agentDir := filepath.Join(home, ".claude", "agents")

	cfg := &config.Config{
		Agent: config.AgentConfig{
			Name:      "test",
			Workspace: dir,
		},
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 15},
		Tasks:    map[string]config.TaskConfig{},
	}

	// Pre-create HEARTBEAT.md and CLAUDE.md to verify they're not overwritten
	heartbeatPath := filepath.Join(dir, "HEARTBEAT.md")
	os.WriteFile(heartbeatPath, []byte("custom heartbeat"), 0644)

	claudeMDPath := filepath.Join(dir, "CLAUDE.md")
	os.WriteFile(claudeMDPath, []byte("custom claude"), 0644)

	// Pre-create a skill file
	os.MkdirAll(filepath.Join(dir, "skills"), 0755)
	customSkillPath := filepath.Join(dir, "skills", templates.SkillFiles()[0])
	os.WriteFile(customSkillPath, []byte("custom skill"), 0644)

	// No agent content, no user profile — should skip those
	err := scaffoldWorkspace(scaffoldOptions{
		workspace: dir, home: home, name: "test", cfg: cfg,
		agentDir: agentDir, agentPath: filepath.Join(agentDir, "test.md"),
		userPath: filepath.Join(dir, "USER.md"),
	})
	if err != nil {
		t.Fatalf("scaffoldWorkspace() error: %v", err)
	}

	// HEARTBEAT.md should be unchanged
	data, _ := os.ReadFile(heartbeatPath)
	if string(data) != "custom heartbeat" {
		t.Errorf("HEARTBEAT.md was overwritten: %q", string(data))
	}

	// CLAUDE.md should be unchanged
	data, _ = os.ReadFile(claudeMDPath)
	if string(data) != "custom claude" {
		t.Errorf("CLAUDE.md was overwritten: %q", string(data))
	}

	// Custom skill file should be unchanged
	data, _ = os.ReadFile(customSkillPath)
	if string(data) != "custom skill" {
		t.Errorf("skill file was overwritten: %q", string(data))
	}
}

func TestChooseAgentTemplateReturnsContent(t *testing.T) {
	names := templates.AgentTemplates()
	if len(names) == 0 {
		t.Skip("no agent templates available")
	}
	content, err := templates.RenderAgent(names[0], templates.AgentData{
		Name:      "test",
		Workspace: "/tmp",
	})
	if err != nil {
		t.Fatalf("RenderAgent() error: %v", err)
	}
	if content == "" {
		t.Error("expected non-empty template content")
	}
}

// --- checkPrerequisites ---

func TestCheckPrerequisitesAllFound(t *testing.T) {
	mockAllPrereqs(t)
	if err := checkPrerequisites(); err != nil {
		t.Errorf("checkPrerequisites() error: %v", err)
	}
}

func TestCheckPrerequisitesClaudeNotFound(t *testing.T) {
	mockAllPrereqs(t)
	checkClaudeFn = func() prereq.ClaudeResult { return prereq.ClaudeResult{OK: false} }

	err := checkPrerequisites()
	if err == nil {
		t.Fatal("expected error when claude not found")
	}
	if !strings.Contains(err.Error(), "claude CLI not found") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCheckPrerequisitesTmuxNotFound(t *testing.T) {
	mockAllPrereqs(t)
	checkTmuxFn = func() bool { return false }

	err := checkPrerequisites()
	if err == nil {
		t.Fatal("expected error when tmux not found")
	}
	if !strings.Contains(err.Error(), "tmux not found") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCheckPrerequisitesBunNotFound(t *testing.T) {
	mockAllPrereqs(t)
	checkBunFn = func() bool { return false }

	err := checkPrerequisites()
	if err == nil {
		t.Fatal("expected error when bun not found")
	}
	if !strings.Contains(err.Error(), "bun not found") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCheckPrerequisitesClaudeNoVersion(t *testing.T) {
	mockAllPrereqs(t)
	checkClaudeFn = func() prereq.ClaudeResult { return prereq.ClaudeResult{OK: true, Version: ""} }

	if err := checkPrerequisites(); err != nil {
		t.Errorf("checkPrerequisites() should succeed with no version: %v", err)
	}
}

// --- promptUserProfile ---

func TestPromptUserProfile(t *testing.T) {
	reader := readerFrom("John\nDeveloper\nLoves Go\nDirect\nUTC\n")
	name, role, about, prefs, tz := promptUserProfile(reader, templates.UserProfileData{})

	if name != "John" {
		t.Errorf("name = %q, want %q", name, "John")
	}
	if role != "Developer" {
		t.Errorf("role = %q, want %q", role, "Developer")
	}
	if about != "Loves Go" {
		t.Errorf("about = %q, want %q", about, "Loves Go")
	}
	if prefs != "Direct" {
		t.Errorf("preferences = %q, want %q", prefs, "Direct")
	}
	if tz != "UTC" {
		t.Errorf("timezone = %q, want %q", tz, "UTC")
	}
}

func TestPromptUserProfileDefaults(t *testing.T) {
	// Empty inputs -> defaults
	reader := readerFrom("\n\n\n\n\n")
	name, _, _, prefs, tz := promptUserProfile(reader, templates.UserProfileData{})

	if name != "" {
		t.Errorf("name = %q, want empty", name)
	}
	if prefs != "Direct and concise" {
		t.Errorf("preferences = %q, want %q", prefs, "Direct and concise")
	}
	if tz != "America/New_York" {
		t.Errorf("timezone = %q, want %q", tz, "America/New_York")
	}
}

// --- parseUserProfile ---

func TestParseUserProfileValid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "USER.md")
	content := "# User Profile\n\n## Name\nAlice\n\n## Role\nEngineer\n\n## About\nLoves Go\n\n## Preferences\nTerse\n\n## Timezone\nUTC\n"
	os.WriteFile(path, []byte(content), 0644)

	result := parseUserProfile(path)
	if result.UserName != "Alice" {
		t.Errorf("UserName = %q, want %q", result.UserName, "Alice")
	}
	if result.Role != "Engineer" {
		t.Errorf("Role = %q, want %q", result.Role, "Engineer")
	}
	if result.About != "Loves Go" {
		t.Errorf("About = %q, want %q", result.About, "Loves Go")
	}
	if result.Preferences != "Terse" {
		t.Errorf("Preferences = %q, want %q", result.Preferences, "Terse")
	}
	if result.Timezone != "UTC" {
		t.Errorf("Timezone = %q, want %q", result.Timezone, "UTC")
	}
}

func TestParseUserProfileMissing(t *testing.T) {
	result := parseUserProfile("/nonexistent/path/USER.md")
	if result.UserName != "" || result.Role != "" {
		t.Error("expected empty result for missing file")
	}
}

func TestParseUserProfileEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "USER.md")
	os.WriteFile(path, []byte(""), 0644)

	result := parseUserProfile(path)
	if result.UserName != "" {
		t.Errorf("expected empty UserName, got %q", result.UserName)
	}
}

func TestParseUserProfilePartial(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "USER.md")
	content := "# User Profile\n\n## Name\nBob\n\n## Timezone\nEurope/London\n"
	os.WriteFile(path, []byte(content), 0644)

	result := parseUserProfile(path)
	if result.UserName != "Bob" {
		t.Errorf("UserName = %q, want %q", result.UserName, "Bob")
	}
	if result.Role != "" {
		t.Errorf("Role = %q, want empty", result.Role)
	}
	if result.Timezone != "Europe/London" {
		t.Errorf("Timezone = %q, want %q", result.Timezone, "Europe/London")
	}
}

// --- promptTelegramConfig ---

func TestPromptTelegramConfigNoExisting(t *testing.T) {
	origPoll := pollChatIDFn
	t.Cleanup(func() { pollChatIDFn = origPoll })
	pollChatIDFn = func(token string, timeout time.Duration) (string, error) {
		return "12345", nil
	}

	// No existing config: token, then poll picks up chatID, then groupID
	reader := readerFrom("bottoken123\nmygroup\n")
	botToken, chatID, groupID := promptTelegramConfig(reader, nil)

	if botToken != "bottoken123" {
		t.Errorf("botToken = %q, want %q", botToken, "bottoken123")
	}
	if chatID != "12345" {
		t.Errorf("chatID = %q, want %q", chatID, "12345")
	}
	if groupID != "mygroup" {
		t.Errorf("groupID = %q, want %q", groupID, "mygroup")
	}
}

func TestPromptTelegramConfigKeepExisting(t *testing.T) {
	existing := &config.Config{
		Telegram: config.TelegramConfig{
			BotToken: "existing-token-12345678",
			ChatID:   "existing-chat",
			GroupID:  "existing-group",
		},
	}

	// User says "n" to reconfigure
	reader := readerFrom("n\n")
	botToken, chatID, groupID := promptTelegramConfig(reader, existing)

	if botToken != "existing-token-12345678" {
		t.Errorf("botToken = %q, want %q", botToken, "existing-token-12345678")
	}
	if chatID != "existing-chat" {
		t.Errorf("chatID = %q, want %q", chatID, "existing-chat")
	}
	if groupID != "existing-group" {
		t.Errorf("groupID = %q, want %q", groupID, "existing-group")
	}
}

func TestPromptTelegramConfigReconfigure(t *testing.T) {
	existing := &config.Config{
		Telegram: config.TelegramConfig{
			BotToken: "old-token-12345678",
			ChatID:   "old-chat",
			GroupID:  "old-group",
		},
	}

	// User says "y" to reconfigure, then enters new values
	reader := readerFrom("y\nnew-token\nnewchat\nnewgroup\n")
	botToken, chatID, groupID := promptTelegramConfig(reader, existing)

	if botToken != "new-token" {
		t.Errorf("botToken = %q, want %q", botToken, "new-token")
	}
	if chatID != "newchat" {
		t.Errorf("chatID = %q, want %q", chatID, "newchat")
	}
	if groupID != "newgroup" {
		t.Errorf("groupID = %q, want %q", groupID, "newgroup")
	}
}

// --- promptTelegram ---

func TestPromptTelegramPollsChat(t *testing.T) {
	origPoll := pollChatIDFn
	t.Cleanup(func() { pollChatIDFn = origPoll })
	pollChatIDFn = func(token string, timeout time.Duration) (string, error) {
		return "polled-chat-id", nil
	}

	// botToken, then empty chatDefault triggers poll, then groupID
	reader := readerFrom("bot-token-value\nmy-group\n")
	botToken, chatID, groupID := promptTelegram(reader, "", "", "")

	if botToken != "bot-token-value" {
		t.Errorf("botToken = %q, want %q", botToken, "bot-token-value")
	}
	if chatID != "polled-chat-id" {
		t.Errorf("chatID = %q, want %q", chatID, "polled-chat-id")
	}
	if groupID != "my-group" {
		t.Errorf("groupID = %q, want %q", groupID, "my-group")
	}
}

func TestPromptTelegramPollFails(t *testing.T) {
	origPoll := pollChatIDFn
	t.Cleanup(func() { pollChatIDFn = origPoll })
	pollChatIDFn = func(token string, timeout time.Duration) (string, error) {
		return "", fmt.Errorf("timeout")
	}

	// Poll fails -> manual entry for chatID
	reader := readerFrom("bot-token\nmanual-chat\nmy-group\n")
	botToken, chatID, groupID := promptTelegram(reader, "", "", "")

	if botToken != "bot-token" {
		t.Errorf("botToken = %q, want %q", botToken, "bot-token")
	}
	if chatID != "manual-chat" {
		t.Errorf("chatID = %q, want %q", chatID, "manual-chat")
	}
	if groupID != "my-group" {
		t.Errorf("groupID = %q, want %q", groupID, "my-group")
	}
}

func TestPromptTelegramExistingChat(t *testing.T) {
	// chatDefault provided, skips poll
	reader := readerFrom("bot-token\nchat-id\ngroup-id\n")
	botToken, chatID, groupID := promptTelegram(reader, "default-token", "default-chat", "default-group")

	if botToken != "bot-token" {
		t.Errorf("botToken = %q, want %q", botToken, "bot-token")
	}
	if chatID != "chat-id" {
		t.Errorf("chatID = %q, want %q", chatID, "chat-id")
	}
	if groupID != "group-id" {
		t.Errorf("groupID = %q, want %q", groupID, "group-id")
	}
}

func TestPromptTelegramEmptyToken(t *testing.T) {
	// Empty token, skips everything
	reader := readerFrom("\n\n\n")
	botToken, chatID, groupID := promptTelegram(reader, "", "", "")

	if botToken != "" {
		t.Errorf("botToken = %q, want empty", botToken)
	}
	if chatID != "" {
		t.Errorf("chatID = %q, want empty", chatID)
	}
	if groupID != "" {
		t.Errorf("groupID = %q, want empty", groupID)
	}
}

// --- installDaemon ---

func TestInstallDaemonSuccess(t *testing.T) {
	origExec := osExecutableFn
	origEnv := envCaptureFn
	origInstall := installDaemonFn
	origStatus := daemonStatusFn
	t.Cleanup(func() {
		osExecutableFn = origExec
		envCaptureFn = origEnv
		installDaemonFn = origInstall
		daemonStatusFn = origStatus
	})

	osExecutableFn = func() (string, error) { return "/usr/local/bin/leo", nil }
	envCaptureFn = func() map[string]string { return map[string]string{"PATH": "/usr/bin"} }
	installDaemonFn = func(sc service.ServiceConfig) error { return nil }
	daemonStatusFn = func(name string) (string, error) { return "running", nil }

	// Should not panic
	installDaemon("test-agent", "/tmp/workspace", "/tmp/workspace/leo.yaml", "bot-token")
}

func TestInstallDaemonFailure(t *testing.T) {
	origExec := osExecutableFn
	origEnv := envCaptureFn
	origInstall := installDaemonFn
	t.Cleanup(func() {
		osExecutableFn = origExec
		envCaptureFn = origEnv
		installDaemonFn = origInstall
	})

	osExecutableFn = func() (string, error) { return "/usr/local/bin/leo", nil }
	envCaptureFn = func() map[string]string { return map[string]string{} }
	installDaemonFn = func(sc service.ServiceConfig) error { return fmt.Errorf("install failed") }

	// Should not panic even on error
	installDaemon("test-agent", "/tmp/workspace", "/tmp/workspace/leo.yaml", "")
}

func TestInstallDaemonNoExecutable(t *testing.T) {
	origExec := osExecutableFn
	origEnv := envCaptureFn
	origInstall := installDaemonFn
	origStatus := daemonStatusFn
	t.Cleanup(func() {
		osExecutableFn = origExec
		envCaptureFn = origEnv
		installDaemonFn = origInstall
		daemonStatusFn = origStatus
	})

	osExecutableFn = func() (string, error) { return "", fmt.Errorf("no executable") }
	envCaptureFn = func() map[string]string { return map[string]string{} }

	var capturedSC service.ServiceConfig
	installDaemonFn = func(sc service.ServiceConfig) error {
		capturedSC = sc
		return nil
	}
	daemonStatusFn = func(name string) (string, error) { return "running", nil }

	installDaemon("test", "/tmp/ws", "/tmp/ws/leo.yaml", "")
	if capturedSC.LeoPath != "leo" {
		t.Errorf("LeoPath = %q, want %q (fallback)", capturedSC.LeoPath, "leo")
	}
}

func TestInstallDaemonWithBotToken(t *testing.T) {
	origExec := osExecutableFn
	origEnv := envCaptureFn
	origInstall := installDaemonFn
	origStatus := daemonStatusFn
	t.Cleanup(func() {
		osExecutableFn = origExec
		envCaptureFn = origEnv
		installDaemonFn = origInstall
		daemonStatusFn = origStatus
	})

	osExecutableFn = func() (string, error) { return "/usr/local/bin/leo", nil }
	envCaptureFn = func() map[string]string { return map[string]string{} }

	var capturedSC service.ServiceConfig
	installDaemonFn = func(sc service.ServiceConfig) error {
		capturedSC = sc
		return nil
	}
	daemonStatusFn = func(name string) (string, error) { return "running", nil }

	installDaemon("test", "/tmp/ws", "/tmp/ws/leo.yaml", "my-bot-token")
	if capturedSC.Env["TELEGRAM_BOT_TOKEN"] != "my-bot-token" {
		t.Errorf("TELEGRAM_BOT_TOKEN = %q, want %q", capturedSC.Env["TELEGRAM_BOT_TOKEN"], "my-bot-token")
	}
}

// --- installTelegramPlugin ---

func TestInstallTelegramPluginFreshInstall(t *testing.T) {
	home := t.TempDir()

	origHome := userHomeDirFn
	origStat := statFn
	origLookPath := lookPathFn
	origExecCmd := execCommandFn
	origMkdir := mkdirAllFn
	origWrite := writeFileFn
	origRead := readFileFn
	t.Cleanup(func() {
		userHomeDirFn = origHome
		statFn = origStat
		lookPathFn = origLookPath
		execCommandFn = origExecCmd
		mkdirAllFn = origMkdir
		writeFileFn = origWrite
		readFileFn = origRead
	})

	userHomeDirFn = func() (string, error) { return home, nil }
	// Plugin dir does not exist
	statFn = func(name string) (os.FileInfo, error) {
		return os.Stat(name) // Use real stat - plugin dir won't exist in temp
	}
	lookPathFn = func(file string) (string, error) {
		return "/usr/bin/claude", nil
	}
	execCommandFn = func(name string, arg ...string) *exec.Cmd {
		return exec.Command("true") // no-op instead of real plugin install
	}
	// Use real file operations on the temp dir
	mkdirAllFn = os.MkdirAll
	writeFileFn = os.WriteFile
	readFileFn = os.ReadFile

	err := installTelegramPlugin("test-bot-token", "test-chat-id", "test-group-id", filepath.Join(home, "workspace"))
	if err != nil {
		t.Fatalf("installTelegramPlugin() error: %v", err)
	}

	// Verify .env was written
	envPath := filepath.Join(home, ".claude", "channels", "telegram", ".env")
	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("reading .env: %v", err)
	}
	if !strings.Contains(string(data), "TELEGRAM_BOT_TOKEN=test-bot-token") {
		t.Errorf(".env content = %q, missing bot token", string(data))
	}

	// Verify access.json was written
	accessPath := filepath.Join(home, ".claude", "channels", "telegram", "access.json")
	data, err = os.ReadFile(accessPath)
	if err != nil {
		t.Fatalf("reading access.json: %v", err)
	}
	var accessDoc map[string]any
	if err := json.Unmarshal(data, &accessDoc); err != nil {
		t.Fatalf("parsing access.json: %v", err)
	}
	if accessDoc["dmPolicy"] != "allowlist" {
		t.Errorf("dmPolicy = %v, want %q", accessDoc["dmPolicy"], "allowlist")
	}

	// Verify settings.json was written
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	data, err = os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("reading settings.json: %v", err)
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("parsing settings.json: %v", err)
	}
	if settings["skipDangerousModePermissionPrompt"] != true {
		t.Error("skipDangerousModePermissionPrompt not set")
	}
}

func TestInstallTelegramPluginAlreadyInstalled(t *testing.T) {
	home := t.TempDir()

	origHome := userHomeDirFn
	origStat := statFn
	origMkdir := mkdirAllFn
	origWrite := writeFileFn
	origRead := readFileFn
	t.Cleanup(func() {
		userHomeDirFn = origHome
		statFn = origStat
		mkdirAllFn = origMkdir
		writeFileFn = origWrite
		readFileFn = origRead
	})

	// Create plugin dir so it appears installed
	pluginDir := filepath.Join(home, ".claude", "plugins", "marketplaces", "claude-plugins-official", "external_plugins", "telegram")
	os.MkdirAll(pluginDir, 0755)

	userHomeDirFn = func() (string, error) { return home, nil }
	statFn = os.Stat
	mkdirAllFn = os.MkdirAll
	writeFileFn = os.WriteFile
	readFileFn = os.ReadFile

	err := installTelegramPlugin("token", "chat", "", filepath.Join(home, "ws"))
	if err != nil {
		t.Fatalf("installTelegramPlugin() error: %v", err)
	}

	// Verify .env was still written
	envPath := filepath.Join(home, ".claude", "channels", "telegram", ".env")
	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("reading .env: %v", err)
	}
	if !strings.Contains(string(data), "TELEGRAM_BOT_TOKEN=token") {
		t.Errorf(".env content = %q, missing bot token", string(data))
	}
}

// --- writeClaudeSettings ---

func TestWriteClaudeSettingsFresh(t *testing.T) {
	home := t.TempDir()

	origHome := userHomeDirFn
	origMkdir := mkdirAllFn
	origWrite := writeFileFn
	origRead := readFileFn
	t.Cleanup(func() {
		userHomeDirFn = origHome
		mkdirAllFn = origMkdir
		writeFileFn = origWrite
		readFileFn = origRead
	})

	userHomeDirFn = func() (string, error) { return home, nil }
	mkdirAllFn = os.MkdirAll
	writeFileFn = os.WriteFile
	readFileFn = os.ReadFile

	err := writeClaudeSettings("/my/workspace")
	if err != nil {
		t.Fatalf("writeClaudeSettings() error: %v", err)
	}

	settingsPath := filepath.Join(home, ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("reading settings.json: %v", err)
	}

	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("parsing settings.json: %v", err)
	}

	// Verify trustedDirectories
	trusted, ok := settings["trustedDirectories"].([]any)
	if !ok {
		t.Fatal("trustedDirectories not an array")
	}
	found := false
	for _, d := range trusted {
		if d == "/my/workspace" {
			found = true
			break
		}
	}
	if !found {
		t.Error("workspace not in trustedDirectories")
	}

	// Verify skipDangerousModePermissionPrompt
	if settings["skipDangerousModePermissionPrompt"] != true {
		t.Error("skipDangerousModePermissionPrompt not set")
	}

	// Verify enabledPlugins
	plugins, ok := settings["enabledPlugins"].(map[string]any)
	if !ok {
		t.Fatal("enabledPlugins not a map")
	}
	if plugins["telegram@claude-plugins-official"] != true {
		t.Error("telegram plugin not enabled")
	}

	// Verify schema
	if settings["$schema"] != "https://json.schemastore.org/claude-code-settings.json" {
		t.Error("schema not set")
	}
}

func TestWriteClaudeSettingsMerge(t *testing.T) {
	home := t.TempDir()

	origHome := userHomeDirFn
	origMkdir := mkdirAllFn
	origWrite := writeFileFn
	origRead := readFileFn
	t.Cleanup(func() {
		userHomeDirFn = origHome
		mkdirAllFn = origMkdir
		writeFileFn = origWrite
		readFileFn = origRead
	})

	userHomeDirFn = func() (string, error) { return home, nil }
	mkdirAllFn = os.MkdirAll
	writeFileFn = os.WriteFile
	readFileFn = os.ReadFile

	// Pre-create settings with existing data
	claudeDir := filepath.Join(home, ".claude")
	os.MkdirAll(claudeDir, 0700)
	existing := map[string]any{
		"$schema":            "https://existing-schema.json",
		"trustedDirectories": []any{"/existing/dir"},
		"customSetting":      "keep-me",
	}
	data, _ := json.MarshalIndent(existing, "", "  ")
	os.WriteFile(filepath.Join(claudeDir, "settings.json"), data, 0600)

	err := writeClaudeSettings("/new/workspace")
	if err != nil {
		t.Fatalf("writeClaudeSettings() error: %v", err)
	}

	settingsData, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	if err != nil {
		t.Fatalf("reading settings.json: %v", err)
	}
	var settings map[string]any
	if err := json.Unmarshal(settingsData, &settings); err != nil {
		t.Fatalf("parsing settings.json: %v", err)
	}

	// Existing schema should be preserved
	if settings["$schema"] != "https://existing-schema.json" {
		t.Errorf("schema = %q, want %q", settings["$schema"], "https://existing-schema.json")
	}

	// Custom setting preserved
	if settings["customSetting"] != "keep-me" {
		t.Errorf("customSetting = %q, want %q", settings["customSetting"], "keep-me")
	}

	// Both workspace dirs present
	trusted, _ := settings["trustedDirectories"].([]any)
	if len(trusted) != 2 {
		t.Errorf("trustedDirectories length = %d, want 2", len(trusted))
	}
}

func TestWriteClaudeSettingsDeduplicates(t *testing.T) {
	home := t.TempDir()

	origHome := userHomeDirFn
	origMkdir := mkdirAllFn
	origWrite := writeFileFn
	origRead := readFileFn
	t.Cleanup(func() {
		userHomeDirFn = origHome
		mkdirAllFn = origMkdir
		writeFileFn = origWrite
		readFileFn = origRead
	})

	userHomeDirFn = func() (string, error) { return home, nil }
	mkdirAllFn = os.MkdirAll
	writeFileFn = os.WriteFile
	readFileFn = os.ReadFile

	// Pre-create settings with the same workspace already trusted
	claudeDir := filepath.Join(home, ".claude")
	os.MkdirAll(claudeDir, 0700)
	existing := map[string]any{
		"trustedDirectories": []any{"/my/workspace"},
	}
	data, _ := json.MarshalIndent(existing, "", "  ")
	os.WriteFile(filepath.Join(claudeDir, "settings.json"), data, 0600)

	err := writeClaudeSettings("/my/workspace")
	if err != nil {
		t.Fatalf("writeClaudeSettings() error: %v", err)
	}

	settingsData, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	if err != nil {
		t.Fatalf("reading settings.json: %v", err)
	}
	var settings map[string]any
	if err := json.Unmarshal(settingsData, &settings); err != nil {
		t.Fatalf("parsing settings.json: %v", err)
	}

	trusted, _ := settings["trustedDirectories"].([]any)
	if len(trusted) != 1 {
		t.Errorf("trustedDirectories length = %d, want 1 (should deduplicate)", len(trusted))
	}
}

// --- Run ---

func TestRunWithOpenClaw(t *testing.T) {
	origFindOC := findOpenClawFn
	origNewReader := newReaderFn
	origMigrate := migrateInteractiveFn
	t.Cleanup(func() {
		findOpenClawFn = origFindOC
		newReaderFn = origNewReader
		migrateInteractiveFn = origMigrate
	})

	findOpenClawFn = func() string { return "/home/user/.openclaw" }
	newReaderFn = func() *bufio.Reader {
		return readerFrom("1\n") // choose migrate
	}
	migrateCalled := false
	migrateInteractiveFn = func(reader *bufio.Reader) error {
		migrateCalled = true
		return nil
	}

	err := Run()
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if !migrateCalled {
		t.Error("expected migrate to be called")
	}
}

func TestRunWithOpenClawChooseFresh(t *testing.T) {
	origFindOC := findOpenClawFn
	origNewReader := newReaderFn
	origHome := userHomeDirFn
	origMigrate := migrateInteractiveFn
	t.Cleanup(func() {
		findOpenClawFn = origFindOC
		newReaderFn = origNewReader
		userHomeDirFn = origHome
		migrateInteractiveFn = origMigrate
	})

	findOpenClawFn = func() string { return "/home/user/.openclaw" }
	// Choose "2" for fresh setup, but RunInteractive will need prereqs
	// So mock checkClaude to fail to short-circuit
	mockAllPrereqs(t)
	checkClaudeFn = func() prereq.ClaudeResult { return prereq.ClaudeResult{OK: false} }

	home := t.TempDir()
	userHomeDirFn = func() (string, error) { return home, nil }

	newReaderFn = func() *bufio.Reader {
		return readerFrom("2\n") // choose fresh setup
	}

	err := Run()
	if err == nil {
		t.Fatal("expected error from failed prereq check")
	}
	if !strings.Contains(err.Error(), "claude CLI not found") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRunNoOpenClaw(t *testing.T) {
	origFindOC := findOpenClawFn
	origNewReader := newReaderFn
	origHome := userHomeDirFn
	t.Cleanup(func() {
		findOpenClawFn = origFindOC
		newReaderFn = origNewReader
		userHomeDirFn = origHome
	})

	findOpenClawFn = func() string { return "" }
	// RunInteractive will be called, but prereqs will fail
	mockAllPrereqs(t)
	checkClaudeFn = func() prereq.ClaudeResult { return prereq.ClaudeResult{OK: false} }

	home := t.TempDir()
	userHomeDirFn = func() (string, error) { return home, nil }

	newReaderFn = func() *bufio.Reader {
		return readerFrom("") // won't be read much since prereqs fail
	}

	err := Run()
	if err == nil {
		t.Fatal("expected error from failed prereq check")
	}
}

// --- RunInteractive ---

func TestRunInteractiveHappyPath(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, ".leo")

	origHome := userHomeDirFn
	origFindWs := findExistingWorkspacesFn
	origDaemonStatus := daemonStatusFn
	origSendMsg := sendMessageFn
	origPoll := pollChatIDFn
	origLookPath := lookPathFn
	origExecCmd := execCommandFn
	t.Cleanup(func() {
		userHomeDirFn = origHome
		findExistingWorkspacesFn = origFindWs
		daemonStatusFn = origDaemonStatus
		sendMessageFn = origSendMsg
		pollChatIDFn = origPoll
		lookPathFn = origLookPath
		execCommandFn = origExecCmd
	})

	mockAllPrereqs(t)

	userHomeDirFn = func() (string, error) { return home, nil }
	// Prevent real claude lookups and plugin install attempts
	lookPathFn = func(file string) (string, error) { return "", fmt.Errorf("not found") }
	execCommandFn = exec.Command
	findExistingWorkspacesFn = func() []string { return nil }
	daemonStatusFn = func(name string) (string, error) { return "not installed", nil }
	sendMessageFn = func(botToken, chatID, text string, topicID int) error { return nil }
	pollChatIDFn = func(token string, timeout time.Duration) (string, error) {
		return "auto-chat-id", nil
	}

	// Input flow:
	// 1. Agent name (default "assistant")
	// 2. Workspace directory (default ~/.leo)
	// 3. Agent template choice ("1")
	// 4. User profile: name, role, about, prefs, timezone
	// 5. Telegram: token, (poll for chat), group
	// 6. Heartbeat: "n"
	// 7. Confirm summary: "y"
	// 8. Voice transcription: "n"
	// 9. Install daemon: "n"
	// 10. Send test message: "n"
	input := strings.Join([]string{
		"",            // agent name -> default "assistant"
		workspace,     // workspace
		"1",           // template choice
		"TestUser",    // name
		"Developer",   // role
		"About me",    // about
		"Direct",      // prefs
		"UTC",         // timezone
		"test-bot-tk", // telegram bot token
		"",            // group ID (skip)
		"n",           // heartbeat
		"y",           // confirm summary
		"n",           // voice transcription
		"n",           // install daemon
		"n",           // send test message
	}, "\n") + "\n"

	reader := readerFrom(input)
	err := RunInteractive(reader)
	if err != nil {
		t.Fatalf("RunInteractive() error: %v", err)
	}

	// Verify config was written
	cfgPath := filepath.Join(workspace, "leo.yaml")
	if _, err := os.Stat(cfgPath); err != nil {
		t.Error("leo.yaml not created")
	}

	cfg, err := config.LoadFromWorkspace(workspace)
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}
	if cfg.Agent.Name != "assistant" {
		t.Errorf("agent name = %q, want %q", cfg.Agent.Name, "assistant")
	}
	if cfg.Telegram.BotToken != "test-bot-tk" {
		t.Errorf("bot token = %q, want %q", cfg.Telegram.BotToken, "test-bot-tk")
	}
}

func TestRunInteractiveHomeDirError(t *testing.T) {
	origHome := userHomeDirFn
	t.Cleanup(func() { userHomeDirFn = origHome })

	userHomeDirFn = func() (string, error) { return "", fmt.Errorf("no home") }

	reader := readerFrom("")
	err := RunInteractive(reader)
	if err == nil {
		t.Fatal("expected error when home dir fails")
	}
	if !strings.Contains(err.Error(), "determining home directory") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRunInteractivePrereqFails(t *testing.T) {
	home := t.TempDir()
	origHome := userHomeDirFn
	t.Cleanup(func() { userHomeDirFn = origHome })

	userHomeDirFn = func() (string, error) { return home, nil }
	mockAllPrereqs(t)
	checkClaudeFn = func() prereq.ClaudeResult { return prereq.ClaudeResult{OK: false} }

	reader := readerFrom("")
	err := RunInteractive(reader)
	if err == nil {
		t.Fatal("expected error when prereqs fail")
	}
}

// --- chooseAgentTemplate ---

func TestChooseAgentTemplateValidChoice(t *testing.T) {
	reader := readerFrom("1\n")
	content := chooseAgentTemplate(reader, "test-agent", "TestUser", "/tmp/ws")

	if content == "" {
		t.Error("expected non-empty template content for choice 1")
	}
}

func TestChooseAgentTemplateInvalidChoice(t *testing.T) {
	// Invalid choice defaults to 1
	reader := readerFrom("999\n")
	content := chooseAgentTemplate(reader, "test-agent", "", "/tmp/ws")

	if content == "" {
		t.Error("expected non-empty template content for invalid choice (should default to 1)")
	}
}

func TestChooseAgentTemplateCustomChoice(t *testing.T) {
	// Choose custom (last option = len(templates)+1)
	numTemplates := len(templates.AgentTemplates())
	reader := readerFrom(fmt.Sprintf("%d\n", numTemplates+1))
	content := chooseAgentTemplate(reader, "test-agent", "", "/tmp/ws")

	if content != "" {
		t.Errorf("expected empty content for custom choice, got %q", content)
	}
}
