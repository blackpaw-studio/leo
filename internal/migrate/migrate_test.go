package migrate

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/service"
)

func newBufioReader(r io.Reader) *bufio.Reader {
	return bufio.NewReader(r)
}

func TestDetectAgentNameHeading(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "IDENTITY.md"), []byte("# MyAgent\nSome description"), 0644)

	name := detectAgentName(dir)
	if name != "myagent" {
		t.Errorf("detectAgentName() = %q, want %q", name, "myagent")
	}
}

func TestDetectAgentNameField(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "IDENTITY.md"), []byte("name: MyAgent\nSome description"), 0644)

	name := detectAgentName(dir)
	if name != "myagent" {
		t.Errorf("detectAgentName() = %q, want %q", name, "myagent")
	}
}

func TestDetectAgentNameMissing(t *testing.T) {
	dir := t.TempDir()

	name := detectAgentName(dir)
	if name != "" {
		t.Errorf("detectAgentName() = %q, want empty", name)
	}
}

func TestStripOpenClawContent(t *testing.T) {
	input := `# Agent
Some content here.
## OpenClaw Gateway
This should be removed.
# Next Section
This should remain.
`
	result := stripOpenClawContent(input)

	if strings.Contains(result, "OpenClaw") {
		t.Error("result should not contain OpenClaw content")
	}
	if strings.Contains(result, "This should be removed") {
		t.Error("result should not contain stripped content")
	}
	if !strings.Contains(result, "Some content here") {
		t.Error("result should contain content before openclaw section")
	}
	if !strings.Contains(result, "Next Section") {
		t.Error("result should contain content after openclaw section")
	}
	if !strings.Contains(result, "This should remain") {
		t.Error("result should contain content under next section")
	}
}

func TestStripOpenClawContentHeartbeatPolling(t *testing.T) {
	input := "# Monitoring\nHeartbeat Polling is active.\n# Status\nAll good."
	result := stripOpenClawContent(input)

	if strings.Contains(result, "Heartbeat Polling") {
		t.Error("should remove heartbeat polling line")
	}
	if !strings.Contains(result, "All good") {
		t.Error("should keep content after")
	}
}

func TestSanitizeTaskName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"lowercase", "Hello", "hello"},
		{"spaces to hyphens", "my task", "my-task"},
		{"underscores to hyphens", "my_task", "my-task"},
		{"combined", "My Cool_Task", "my-cool-task"},
		{"already clean", "heartbeat", "heartbeat"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeTaskName(tt.in)
			if got != tt.want {
				t.Errorf("sanitizeTaskName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestMergeAgentFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "SOUL.md"), []byte("# Soul\nI am helpful."), 0644)
	os.WriteFile(filepath.Join(dir, "IDENTITY.md"), []byte("# MyAgent\nI am MyAgent."), 0644)

	result := mergeAgentFiles(dir, "myagent", "/home/user/myagent")

	if !strings.Contains(result, "name: myagent") {
		t.Error("should contain agent name in frontmatter")
	}
	if !strings.Contains(result, "I am helpful") {
		t.Error("should contain SOUL.md content")
	}
	if !strings.Contains(result, "I am MyAgent") {
		t.Error("should contain IDENTITY.md content")
	}
	if !strings.Contains(result, "/home/user/myagent") {
		t.Error("should contain workspace path")
	}
}

func TestCopyWorkspaceFiles(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	// Create source files
	os.WriteFile(filepath.Join(srcDir, "USER.md"), []byte("user info"), 0644)
	os.WriteFile(filepath.Join(srcDir, "HEARTBEAT.md"), []byte("heartbeat"), 0644)

	memDir := filepath.Join(srcDir, "memory")
	os.MkdirAll(memDir, 0755)
	os.WriteFile(filepath.Join(memDir, "note.md"), []byte("a note"), 0644)

	count := copyWorkspaceFiles(srcDir, dstDir, srcDir)
	if count < 2 {
		t.Errorf("copyWorkspaceFiles() = %d, want at least 2", count)
	}

	// Verify direct copies
	if data, err := os.ReadFile(filepath.Join(dstDir, "USER.md")); err != nil {
		t.Error("USER.md not copied")
	} else if string(data) != "user info" {
		t.Errorf("USER.md content = %q", string(data))
	}

	// Verify directory copy
	if data, err := os.ReadFile(filepath.Join(dstDir, "daily", "note.md")); err != nil {
		t.Error("memory/note.md not copied to daily/")
	} else if string(data) != "a note" {
		t.Errorf("note.md content = %q", string(data))
	}
}

func TestRewritePaths(t *testing.T) {
	dir := t.TempDir()
	content := "Path is /old/workspace/foo and /old/workspace/bar"
	os.WriteFile(filepath.Join(dir, "test.md"), []byte(content), 0644)

	count := rewritePaths(dir, "/old/workspace", "/new/workspace")
	if count != 1 {
		t.Errorf("rewritePaths() = %d, want 1", count)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "test.md"))
	result := string(data)

	if strings.Contains(result, "/old/workspace") {
		t.Error("old path should be replaced")
	}
	if !strings.Contains(result, "/new/workspace/foo") {
		t.Error("new path should be present")
	}
}

func TestRewritePathsNoMatch(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.md"), []byte("no paths here"), 0644)

	count := rewritePaths(dir, "/old/workspace", "/new/workspace")
	if count != 0 {
		t.Errorf("rewritePaths() = %d, want 0", count)
	}
}

func TestParseCronJobs(t *testing.T) {
	dir := t.TempDir()
	cronDir := filepath.Join(dir, "cron")
	os.MkdirAll(cronDir, 0755)

	workspace := t.TempDir()
	os.MkdirAll(filepath.Join(workspace, "reports"), 0755)

	jobs := openClawJobsFile{
		Version: 1,
		Jobs: []openClawJob{
			{
				Name:     "heartbeat",
				Schedule: openClawSchedule{Kind: "cron", Expr: "0,30 7-22 * * *", Tz: "America/New_York"},
				Payload:  openClawPayload{Kind: "agentTurn", Message: "Check inbox"},
				Enabled:  true,
			},
			{
				Name:     "gateway-health",
				Schedule: openClawSchedule{Kind: "cron", Expr: "*/5 * * * *"},
				Enabled:  true,
			},
			{
				Name:     "openclaw-status",
				Schedule: openClawSchedule{Kind: "cron", Expr: "0 * * * *"},
				Enabled:  true,
			},
		},
	}

	data, _ := json.Marshal(jobs)
	os.WriteFile(filepath.Join(cronDir, "jobs.json"), data, 0644)

	cfg := &config.Config{
		Agent: config.AgentConfig{
			Name:      "test",
			Workspace: workspace,
		},
		Tasks: make(map[string]config.TaskConfig),
	}

	parseCronJobs(dir, cfg)

	if len(cfg.Tasks) != 1 {
		t.Fatalf("expected 1 task (gateway/openclaw skipped), got %d", len(cfg.Tasks))
	}

	task, ok := cfg.Tasks["heartbeat"]
	if !ok {
		t.Fatal("heartbeat task not found")
	}
	if task.Schedule != "0,30 7-22 * * *" {
		t.Errorf("schedule = %q", task.Schedule)
	}
	if task.Timezone != "America/New_York" {
		t.Errorf("timezone = %q", task.Timezone)
	}
	if task.PromptFile == "" {
		t.Error("expected prompt file to be written")
	}
}

func TestParseCronJobsNoFile(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{Tasks: make(map[string]config.TaskConfig)}

	// Should not panic or error
	parseCronJobs(dir, cfg)

	if len(cfg.Tasks) != 0 {
		t.Errorf("expected 0 tasks, got %d", len(cfg.Tasks))
	}
}

func TestDetectAgentNameBoldField(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "IDENTITY.md"), []byte("- **Name:** Susie (also responds to Sue)"), 0644)

	name := detectAgentName(dir)
	if name != "susie" {
		t.Errorf("detectAgentName() = %q, want %q", name, "susie")
	}
}

func TestCopyFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")

	os.WriteFile(src, []byte("hello"), 0644)

	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile() error: %v", err)
	}

	data, _ := os.ReadFile(dst)
	if string(data) != "hello" {
		t.Errorf("dst content = %q, want %q", string(data), "hello")
	}
}

func TestCopyFileMissingSrc(t *testing.T) {
	err := copyFile("/nonexistent", "/tmp/dst")
	if err == nil {
		t.Error("expected error for missing source")
	}
}

func TestParseCronJobsWithDeliveryTopic(t *testing.T) {
	dir := t.TempDir()
	cronDir := filepath.Join(dir, "cron")
	os.MkdirAll(cronDir, 0755)

	workspace := t.TempDir()
	os.MkdirAll(filepath.Join(workspace, "reports"), 0755)

	jobs := openClawJobsFile{
		Version: 1,
		Jobs: []openClawJob{
			{
				Name:     "daily-report",
				Schedule: openClawSchedule{Kind: "cron", Expr: "0 7 * * *"},
				Payload:  openClawPayload{Kind: "agentTurn", Message: "Run report"},
				Delivery: openClawDelivery{To: "telegram:topic:news"},
				Enabled:  true,
			},
		},
	}

	data, _ := json.Marshal(jobs)
	os.WriteFile(filepath.Join(cronDir, "jobs.json"), data, 0644)

	cfg := &config.Config{
		Agent: config.AgentConfig{Workspace: workspace},
		Tasks: make(map[string]config.TaskConfig),
	}

	parseCronJobs(dir, cfg)

	task, ok := cfg.Tasks["daily-report"]
	if !ok {
		t.Fatal("daily-report task not found")
	}
	// Topic name migration is no longer supported (Leo uses numeric topic_id).
	// Just verify the task was created.
	if task.PromptFile == "" {
		t.Error("task should have a prompt file")
	}
}

func TestConfigureTelegramFromOpenClawJSON(t *testing.T) {
	dir := t.TempDir()

	ocConfig := map[string]any{
		"channels": map[string]any{
			"telegram": map[string]any{
				"botToken": "test-token-12345678",
				"groups": map[string]any{
					"-100999": map[string]any{"requireMention": false},
				},
			},
		},
	}
	data, _ := json.Marshal(ocConfig)
	os.WriteFile(filepath.Join(dir, "openclaw.json"), data, 0644)

	cfg := &config.Config{}
	reader := strings.NewReader("\n\n")

	configureTelegram(newBufioReader(reader), dir, cfg)

	if cfg.Telegram.BotToken != "test-token-12345678" {
		t.Errorf("BotToken = %q, want %q", cfg.Telegram.BotToken, "test-token-12345678")
	}
	if cfg.Telegram.GroupID != "-100999" {
		t.Errorf("GroupID = %q, want %q", cfg.Telegram.GroupID, "-100999")
	}
}

func TestConfigureTelegramAllowFrom(t *testing.T) {
	dir := t.TempDir()
	credDir := filepath.Join(dir, "credentials")
	os.MkdirAll(credDir, 0755)

	allow := map[string]any{
		"allowFrom": []any{"11111"},
	}
	data, _ := json.Marshal(allow)
	os.WriteFile(filepath.Join(credDir, "telegram-default-allowFrom.json"), data, 0644)

	cfg := &config.Config{}
	reader := strings.NewReader("mytoken\n\n")

	configureTelegram(newBufioReader(reader), dir, cfg)

	if cfg.Telegram.ChatID != "11111" {
		t.Errorf("ChatID = %q, want %q", cfg.Telegram.ChatID, "11111")
	}
}

func TestCopyDir(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	// Create test files in src
	os.WriteFile(filepath.Join(src, "file1.txt"), []byte("content1"), 0644)
	os.WriteFile(filepath.Join(src, "file2.txt"), []byte("content2"), 0644)

	count := 0
	copyDir(src, filepath.Join(dst, "copied"), &count)
	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}

	// Verify files exist in dst
	data, err := os.ReadFile(filepath.Join(dst, "copied", "file1.txt"))
	if err != nil {
		t.Fatalf("reading file1.txt: %v", err)
	}
	if string(data) != "content1" {
		t.Error("file1 content mismatch")
	}
}

func TestCopyDirEmpty(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	count := 0
	copyDir(src, filepath.Join(dst, "empty"), &count)
	if count != 0 {
		t.Errorf("count = %d, want 0", count)
	}
}

func TestCopyDirNonexistent(t *testing.T) {
	dst := t.TempDir()
	count := 0
	// Should not panic for nonexistent source
	copyDir("/nonexistent/path", filepath.Join(dst, "out"), &count)
	if count != 0 {
		t.Errorf("count = %d, want 0", count)
	}
}

func TestCopyDirSkipsSubdirs(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	os.WriteFile(filepath.Join(src, "file.txt"), []byte("content"), 0644)
	os.MkdirAll(filepath.Join(src, "subdir"), 0755)
	os.WriteFile(filepath.Join(src, "subdir", "nested.txt"), []byte("nested"), 0644)

	count := 0
	copyDir(src, filepath.Join(dst, "copied"), &count)
	// Should only copy the top-level file, not the subdir
	if count != 1 {
		t.Errorf("count = %d, want 1 (subdirs skipped)", count)
	}
}

func TestFindOpenClawNotFound(t *testing.T) {
	// FindOpenClaw looks for ~/.openclaw -- if it doesn't exist, should return ""
	result := FindOpenClaw()
	// We can't guarantee whether ~/.openclaw exists, but we can test it doesn't panic
	_ = result
}

func TestRunInteractiveNoOCPath(t *testing.T) {
	origFindOC := findOpenClawFn
	t.Cleanup(func() { findOpenClawFn = origFindOC })
	findOpenClawFn = func() string { return "" }

	// Provide empty OC path -> should error
	reader := strings.NewReader("\n")
	err := RunInteractive(newBufioReader(reader))
	if err == nil {
		t.Error("expected error for empty OC path")
	}
	if !strings.Contains(err.Error(), "no OpenClaw installation found") {
		t.Errorf("error = %q, want 'no OpenClaw installation found'", err.Error())
	}
}

func TestRunInteractiveFullMigration(t *testing.T) {
	// Set up a fake OpenClaw workspace
	ocDir := t.TempDir()
	ocWorkspace := filepath.Join(ocDir, "workspace")
	os.MkdirAll(ocWorkspace, 0755)
	os.WriteFile(filepath.Join(ocWorkspace, "IDENTITY.md"), []byte("# TestBot\nI am a test bot."), 0644)
	os.WriteFile(filepath.Join(ocWorkspace, "USER.md"), []byte("Test user"), 0644)

	// Set up cron jobs
	cronDir := filepath.Join(ocDir, "cron")
	os.MkdirAll(cronDir, 0755)
	jobs := openClawJobsFile{
		Version: 1,
		Jobs: []openClawJob{
			{
				Name:     "heartbeat",
				Schedule: openClawSchedule{Kind: "cron", Expr: "0 * * * *", Tz: "America/New_York"},
				Payload:  openClawPayload{Kind: "agentTurn", Message: "Check in"},
				Enabled:  true,
			},
		},
	}
	jobsData, _ := json.Marshal(jobs)
	os.WriteFile(filepath.Join(cronDir, "jobs.json"), jobsData, 0644)

	// Set up openclaw.json with telegram config
	ocConfig := map[string]any{
		"channels": map[string]any{
			"telegram": map[string]any{
				"botToken": "test-token-12345678",
				"groups":   map[string]any{"-100999": map[string]any{}},
			},
		},
	}
	ocConfigData, _ := json.Marshal(ocConfig)
	os.WriteFile(filepath.Join(ocDir, "openclaw.json"), ocConfigData, 0644)

	// Set up credentials for chat_id
	credDir := filepath.Join(ocDir, "credentials")
	os.MkdirAll(credDir, 0755)
	cred := map[string]any{"chat_id": "12345"}
	credData, _ := json.Marshal(cred)
	os.WriteFile(filepath.Join(credDir, "telegram-bot.json"), credData, 0644)

	newWorkspace := filepath.Join(t.TempDir(), "workspace")

	// Mock seams
	origFindOC := findOpenClawFn
	origDaemonIsRunning := daemonIsRunningFn
	origDaemonSend := daemonSendFn
	origInstallDaemon := installDaemonFn
	origDaemonStatus := daemonStatusFn
	origSendMessage := sendMessageFn
	origEnvCapture := envCaptureFn
	origOsExecutable := osExecutableFn
	t.Cleanup(func() {
		findOpenClawFn = origFindOC
		daemonIsRunningFn = origDaemonIsRunning
		daemonSendFn = origDaemonSend
		installDaemonFn = origInstallDaemon
		daemonStatusFn = origDaemonStatus
		sendMessageFn = origSendMessage
		envCaptureFn = origEnvCapture
		osExecutableFn = origOsExecutable
	})

	findOpenClawFn = func() string { return "" }
	daemonIsRunningFn = func(string) bool { return false }
	daemonSendFn = func(string, string, string, any) (*daemon.Response, error) {
		return &daemon.Response{OK: true}, nil
	}
	installDaemonFn = func(sc service.ServiceConfig) error { return nil }
	daemonStatusFn = func(string) (string, error) { return "running", nil }
	sendMessageFn = func(string, string, string, int) error { return nil }
	envCaptureFn = func() map[string]string { return map[string]string{} }
	osExecutableFn = func() (string, error) { return "/usr/bin/leo", nil }

	// Input sequence:
	// 1. OpenClaw workspace path: provide ocDir
	// 2. Agent name: accept default (empty line)
	// 3. New workspace directory: provide newWorkspace
	// 4. configureTelegram: token/chat_id found, accept defaults (empty lines)
	// 5. Install chat daemon: "y"
	// 6. Send test Telegram message: "n"
	input := fmt.Sprintf("%s\n\n%s\n\n\ny\nn\n", ocDir, newWorkspace)
	reader := newBufioReader(strings.NewReader(input))

	err := RunInteractive(reader)
	if err != nil {
		t.Fatalf("RunInteractive() error: %v", err)
	}

	// Verify config was written
	cfgPath := filepath.Join(newWorkspace, "leo.yaml")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}
	if cfg.Agent.Name != "testbot" {
		t.Errorf("agent name = %q, want %q", cfg.Agent.Name, "testbot")
	}
	if len(cfg.Tasks) != 1 {
		t.Errorf("tasks count = %d, want 1", len(cfg.Tasks))
	}
}

func TestConfigureTelegram(t *testing.T) {
	dir := t.TempDir()
	credDir := filepath.Join(dir, "credentials")
	os.MkdirAll(credDir, 0755)

	cred := map[string]any{
		"chat_id":  "123456",
		"group_id": "-100999",
		"topics": map[string]any{
			"alerts": float64(1),
			"news":   float64(3),
		},
	}
	data, _ := json.Marshal(cred)
	os.WriteFile(filepath.Join(credDir, "telegram-bot.json"), data, 0644)

	cfg := &config.Config{}

	// Use a reader that provides the bot token when prompted
	reader := strings.NewReader("test-token\n")

	configureTelegram(
		newBufioReader(reader),
		dir,
		cfg,
	)

	if cfg.Telegram.ChatID != "123456" {
		t.Errorf("ChatID = %q, want %q", cfg.Telegram.ChatID, "123456")
	}
	if cfg.Telegram.GroupID != "-100999" {
		t.Errorf("GroupID = %q, want %q", cfg.Telegram.GroupID, "-100999")
	}
	// Topics are no longer migrated (Leo uses numeric topic_id on tasks).
	// Just verify chat_id and group_id were migrated correctly.
}
