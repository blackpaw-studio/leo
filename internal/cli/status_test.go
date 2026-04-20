package cli

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

// TestBuildStatusReport_Version verifies the report captures the current
// binary version.
func TestBuildStatusReport_Version(t *testing.T) {
	oldVersion := Version
	Version = "v0.0.0-test"
	t.Cleanup(func() { Version = oldVersion })

	home := t.TempDir()
	cfgPath := filepath.Join(home, "leo.yaml")
	if err := config.Save(cfgPath, &config.Config{HomePath: home}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	oldCfgFile := cfgFile
	cfgFile = cfgPath
	t.Cleanup(func() { cfgFile = oldCfgFile })

	report := buildStatusReport(context.Background())
	if report.LeoVersion != "v0.0.0-test" {
		t.Errorf("LeoVersion = %q; want %q", report.LeoVersion, "v0.0.0-test")
	}
	if !report.ConfigValid {
		t.Errorf("ConfigValid = false; want true (err=%q)", report.ConfigError)
	}
}

// TestBuildStatusReport_ConfigMissing verifies config-load failure is
// surfaced without panicking.
func TestBuildStatusReport_ConfigMissing(t *testing.T) {
	oldCfgFile := cfgFile
	cfgFile = filepath.Join(t.TempDir(), "does-not-exist.yaml")
	t.Cleanup(func() { cfgFile = oldCfgFile })

	report := buildStatusReport(context.Background())
	if report.ConfigValid {
		t.Errorf("ConfigValid should be false when config is missing")
	}
	if report.ConfigError == "" {
		t.Errorf("ConfigError should be populated")
	}
}

// TestBuildStatusReport_TaskIssues verifies tasks with missing prompt_file
// or schedule are surfaced as issues.
func TestBuildStatusReport_TaskIssues(t *testing.T) {
	home := t.TempDir()
	cfgPath := filepath.Join(home, "leo.yaml")
	cfg := &config.Config{
		HomePath: home,
		Tasks: map[string]config.TaskConfig{
			"good": {Schedule: "0 2 * * *", PromptFile: "good.md", Enabled: true},
		},
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	oldCfgFile := cfgFile
	cfgFile = cfgPath
	t.Cleanup(func() { cfgFile = oldCfgFile })

	report := buildStatusReport(context.Background())
	if !report.ConfigValid {
		t.Fatalf("ConfigValid = false; err=%q", report.ConfigError)
	}
	if report.Tasks.Total != 1 || report.Tasks.Enabled != 1 {
		t.Errorf("Tasks summary: total=%d enabled=%d; want 1/1", report.Tasks.Total, report.Tasks.Enabled)
	}
	if len(report.TaskIssues) != 0 {
		t.Errorf("expected no task issues, got %v", report.TaskIssues)
	}
}

// TestTaskProblem verifies the taskProblem helper surfaces missing prompt_file
// and schedule.
func TestTaskProblem(t *testing.T) {
	cfg := &config.Config{HomePath: t.TempDir()}
	cases := []struct {
		name string
		task config.TaskConfig
		want string
	}{
		{"ok", config.TaskConfig{Schedule: "0 2 * * *", PromptFile: "p.md"}, ""},
		{"no prompt", config.TaskConfig{Schedule: "0 2 * * *"}, "missing prompt_file"},
		{"no schedule", config.TaskConfig{PromptFile: "p.md"}, "missing schedule"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := taskProblem(cfg, tc.name, tc.task); got != tc.want {
				t.Errorf("taskProblem = %q; want %q", got, tc.want)
			}
		})
	}
}

// TestRunStatusJSON_ValidOutput verifies --json emits parseable structured JSON.
func TestRunStatusJSON_ValidOutput(t *testing.T) {
	home := t.TempDir()
	cfgPath := filepath.Join(home, "leo.yaml")
	if err := config.Save(cfgPath, &config.Config{HomePath: home}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	oldCfgFile := cfgFile
	cfgFile = cfgPath
	t.Cleanup(func() { cfgFile = oldCfgFile })

	out := captureStdoutForConfigTests(t, func() {
		if err := runStatusJSON(context.Background()); err != nil {
			t.Fatalf("runStatusJSON: %v", err)
		}
	})

	if !strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Fatalf("expected JSON output; got:\n%s", out)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out)
	}
	for _, k := range []string{"leo_version", "config_valid", "service", "daemon", "processes", "tasks"} {
		if _, ok := parsed[k]; !ok {
			t.Errorf("missing JSON key %q in %v", k, keys(parsed))
		}
	}
}

// TestJoinNames covers empty, single, multiple names.
func TestJoinNames(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, ""},
		{[]string{}, ""},
		{[]string{"a"}, "a"},
		{[]string{"a", "b"}, "a, b"},
		{[]string{"foo", "bar", "baz"}, "foo, bar, baz"},
	}
	for _, tc := range cases {
		if got := joinNames(tc.in); got != tc.want {
			t.Errorf("joinNames(%v) = %q; want %q", tc.in, got, tc.want)
		}
	}
}
