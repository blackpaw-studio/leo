package cron

import (
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

func TestBuildBlock(t *testing.T) {
	cfg := &config.Config{
		Agent: config.AgentConfig{
			Name:      "rocket",
			Workspace: "/home/user/rocket",
		},
		Tasks: map[string]config.TaskConfig{
			"heartbeat": {
				Schedule: "0,30 7-22 * * *",
				Enabled:  true,
			},
			"news": {
				Schedule: "0 7 * * *",
				Enabled:  true,
			},
			"disabled": {
				Schedule: "* * * * *",
				Enabled:  false,
			},
		},
	}

	block := buildBlock(cfg, "/usr/local/bin/leo")

	if !strings.Contains(block, "# === LEO:rocket — DO NOT EDIT ===") {
		t.Error("missing start marker")
	}
	if !strings.Contains(block, "# === END LEO:rocket ===") {
		t.Error("missing end marker")
	}
	if !strings.Contains(block, "heartbeat") {
		t.Error("missing heartbeat task")
	}
	if !strings.Contains(block, "news") {
		t.Error("missing news task")
	}
	if strings.Contains(block, "disabled") {
		t.Error("should not contain disabled task")
	}
	if !strings.Contains(block, "/usr/local/bin/leo run") {
		t.Error("missing leo run command")
	}
}

func TestRemoveBlock(t *testing.T) {
	crontab := `# other stuff
0 * * * * /some/other/job
# === LEO:rocket — DO NOT EDIT ===
# leo:rocket:heartbeat
0,30 7-22 * * * /usr/local/bin/leo run heartbeat
# === END LEO:rocket ===
# more stuff
`

	result := removeBlock(crontab, "rocket")

	if strings.Contains(result, "LEO:rocket") {
		t.Error("block was not removed")
	}
	if !strings.Contains(result, "other stuff") {
		t.Error("other content was removed")
	}
	if !strings.Contains(result, "more stuff") {
		t.Error("content after block was removed")
	}
}

func TestRemoveBlockPreservesOtherAgents(t *testing.T) {
	crontab := `# === LEO:agent1 — DO NOT EDIT ===
# leo:agent1:task1
* * * * * /usr/local/bin/leo run task1
# === END LEO:agent1 ===
# === LEO:agent2 — DO NOT EDIT ===
# leo:agent2:task2
* * * * * /usr/local/bin/leo run task2
# === END LEO:agent2 ===
`

	result := removeBlock(crontab, "agent1")

	if strings.Contains(result, "LEO:agent1") {
		t.Error("agent1 block should be removed")
	}
	if !strings.Contains(result, "LEO:agent2") {
		t.Error("agent2 block should be preserved")
	}
}

func TestExtractBlock(t *testing.T) {
	crontab := `# other stuff
# === LEO:rocket — DO NOT EDIT ===
# leo:rocket:heartbeat
0,30 7-22 * * * /usr/local/bin/leo run heartbeat
# === END LEO:rocket ===
`

	block := extractBlock(crontab, "rocket")

	if !strings.Contains(block, "heartbeat") {
		t.Error("block should contain heartbeat")
	}
	if strings.Contains(block, "other stuff") {
		t.Error("block should not contain other content")
	}
}

func TestExtractBlockMissing(t *testing.T) {
	block := extractBlock("no leo content here", "rocket")
	if block != "" {
		t.Errorf("extractBlock() = %q, want empty", block)
	}
}

func TestBuildBlockEmptyLeoPath(t *testing.T) {
	cfg := &config.Config{
		Agent: config.AgentConfig{Name: "test", Workspace: "/home/user/test"},
		Tasks: map[string]config.TaskConfig{
			"heartbeat": {Schedule: "* * * * *", Enabled: true},
		},
	}

	block := buildBlock(cfg, "")
	if !strings.Contains(block, "leo run") {
		t.Error("should fallback to 'leo' when path is empty")
	}
}

func TestBuildBlockNoEnabledTasks(t *testing.T) {
	cfg := &config.Config{
		Agent: config.AgentConfig{Name: "test"},
		Tasks: map[string]config.TaskConfig{
			"disabled": {Schedule: "* * * * *", Enabled: false},
		},
	}

	block := buildBlock(cfg, "/usr/local/bin/leo")
	if !strings.Contains(block, "# === LEO:test") {
		t.Error("should still have start marker")
	}
	if !strings.Contains(block, "# === END LEO:test") {
		t.Error("should still have end marker")
	}
	if strings.Contains(block, "leo run") {
		t.Error("should not contain job lines")
	}
}

func TestInstall(t *testing.T) {
	originalRead := readCrontab
	originalWrite := writeCrontab
	defer func() {
		readCrontab = originalRead
		writeCrontab = originalWrite
	}()

	readCrontab = func() (string, error) {
		return "# existing content\n0 * * * * /some/job\n", nil
	}

	var written string
	writeCrontab = func(content string) error {
		written = content
		return nil
	}

	cfg := &config.Config{
		Agent: config.AgentConfig{Name: "rocket", Workspace: "/home/user/rocket"},
		Tasks: map[string]config.TaskConfig{
			"heartbeat": {Schedule: "0,30 7-22 * * *", Enabled: true},
		},
	}

	err := Install(cfg, "/usr/local/bin/leo")
	if err != nil {
		t.Fatalf("Install() error: %v", err)
	}

	if !strings.Contains(written, "existing content") {
		t.Error("should preserve existing content")
	}
	if !strings.Contains(written, "# === LEO:rocket") {
		t.Error("should contain leo block")
	}
	if !strings.Contains(written, "heartbeat") {
		t.Error("should contain heartbeat job")
	}
}

func TestRemoveViaAPI(t *testing.T) {
	originalRead := readCrontab
	originalWrite := writeCrontab
	defer func() {
		readCrontab = originalRead
		writeCrontab = originalWrite
	}()

	readCrontab = func() (string, error) {
		return `# other job
# === LEO:rocket — DO NOT EDIT ===
# leo:rocket:heartbeat
0,30 7-22 * * * /usr/local/bin/leo run heartbeat
# === END LEO:rocket ===
`, nil
	}

	var written string
	writeCrontab = func(content string) error {
		written = content
		return nil
	}

	cfg := &config.Config{
		Agent: config.AgentConfig{Name: "rocket"},
	}

	err := Remove(cfg)
	if err != nil {
		t.Fatalf("Remove() error: %v", err)
	}

	if strings.Contains(written, "LEO:rocket") {
		t.Error("should remove leo block")
	}
	if !strings.Contains(written, "other job") {
		t.Error("should preserve other content")
	}
}

func TestListViaAPI(t *testing.T) {
	originalRead := readCrontab
	defer func() { readCrontab = originalRead }()

	readCrontab = func() (string, error) {
		return `# === LEO:rocket — DO NOT EDIT ===
# leo:rocket:heartbeat
0,30 7-22 * * * /usr/local/bin/leo run heartbeat
# === END LEO:rocket ===
`, nil
	}

	cfg := &config.Config{
		Agent: config.AgentConfig{Name: "rocket"},
	}

	result, err := List(cfg)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}

	if !strings.Contains(result, "heartbeat") {
		t.Error("List should contain heartbeat")
	}
}
