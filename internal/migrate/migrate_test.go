package migrate

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
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
	if cfg.Telegram.Topics["alerts"] != 1 {
		t.Errorf("Topics[alerts] = %d, want 1", cfg.Telegram.Topics["alerts"])
	}
	if cfg.Telegram.Topics["news"] != 3 {
		t.Errorf("Topics[news] = %d, want 3", cfg.Telegram.Topics["news"])
	}
}
