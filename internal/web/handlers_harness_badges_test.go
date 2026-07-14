package web

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

func TestBuildTemplatesDataHarnessBadges(t *testing.T) {
	cfg := &config.Config{
		Defaults: config.DefaultsConfig{Harness: "codex"},
		Templates: map[string]config.TemplateConfig{
			"explicit": {Workspace: "/w", Harness: "claude"},
			"inherit":  {Workspace: "/w"},
		},
	}
	s := seedHarnessTestServer(t, cfg)

	data, err := s.buildTemplatesData(httptest.NewRequest("GET", "/config/templates", nil))
	if err != nil {
		t.Fatalf("buildTemplatesData: %v", err)
	}
	rows := data.(templatesPageData).Rows

	byName := make(map[string]templateRow, len(rows))
	for _, row := range rows {
		byName[row.Name] = row
	}

	if got := byName["explicit"]; got.Harness != "claude" || got.HarnessInherited {
		t.Errorf("explicit template row = %+v, want {Harness: claude, HarnessInherited: false}", got)
	}
	if got := byName["inherit"]; got.Harness != "codex" || !got.HarnessInherited {
		t.Errorf("inherit template row = %+v, want {Harness: codex, HarnessInherited: true}", got)
	}
}

// TestBuildTasksDataHarnessBadges also confirms both the Enabled and Disabled
// buckets carry the harness fields (buildTasksData splits rows into two
// slices after computing them).
func TestBuildTasksDataHarnessBadges(t *testing.T) {
	cfg := &config.Config{
		Defaults: config.DefaultsConfig{Harness: "codex"},
		Tasks: map[string]config.TaskConfig{
			"explicit": {Schedule: "@daily", PromptFile: "p.md", Harness: "claude", Enabled: true},
			"inherit":  {Schedule: "@daily", PromptFile: "p.md", Enabled: false},
		},
	}
	s := seedHarnessTestServer(t, cfg)

	data, err := s.buildTasksData(httptest.NewRequest("GET", "/tasks", nil))
	if err != nil {
		t.Fatalf("buildTasksData: %v", err)
	}
	tpd := data.(tasksPageData)

	if len(tpd.Enabled) != 1 || tpd.Enabled[0].Name != "explicit" {
		t.Fatalf("expected 1 enabled row 'explicit', got %+v", tpd.Enabled)
	}
	if got := tpd.Enabled[0]; got.Harness != "claude" || got.HarnessInherited {
		t.Errorf("explicit task row = %+v, want {Harness: claude, HarnessInherited: false}", got)
	}

	if len(tpd.Disabled) != 1 || tpd.Disabled[0].Name != "inherit" {
		t.Fatalf("expected 1 disabled row 'inherit', got %+v", tpd.Disabled)
	}
	if got := tpd.Disabled[0]; got.Harness != "codex" || !got.HarnessInherited {
		t.Errorf("inherit task row = %+v, want {Harness: codex, HarnessInherited: true}", got)
	}
}

// TestPageConfigTemplatesRendersHarnessColumn is a render-level smoke test:
// the templates table shows a harness column.
func TestPageConfigTemplatesRendersHarnessColumn(t *testing.T) {
	cfg := &config.Config{
		Defaults:  config.DefaultsConfig{Harness: "codex"},
		Templates: map[string]config.TemplateConfig{"a": {Workspace: "/w", Harness: "claude"}},
	}
	s := seedHarnessTestServer(t, cfg)

	body := getBody(t, s, "/config/templates")
	if !strings.Contains(body, "<th>harness</th>") {
		t.Error("expected templates table to show a harness column header")
	}
}

// TestPageConfigTemplatesEmptyStateColspan confirms the empty-state row's
// colspan matches the templates table's column count.
func TestPageConfigTemplatesEmptyStateColspan(t *testing.T) {
	s := seedHarnessTestServer(t, &config.Config{})

	body := getBody(t, s, "/config/templates")
	if !strings.Contains(body, `colspan="6"`) {
		t.Errorf("expected empty-state colspan bumped to 6: %s", body)
	}
}

// TestPageTasksRendersHarnessColumn confirms the harness column header
// appears in both the Enabled and Disabled tables.
func TestPageTasksRendersHarnessColumn(t *testing.T) {
	cfg := &config.Config{
		Defaults: config.DefaultsConfig{Harness: "codex"},
		Tasks: map[string]config.TaskConfig{
			"on":  {Schedule: "@daily", PromptFile: "p.md", Enabled: true, Harness: "claude"},
			"off": {Schedule: "@daily", PromptFile: "p.md", Enabled: false},
		},
	}
	s := seedHarnessTestServer(t, cfg)

	body := getBody(t, s, "/tasks")
	if n := strings.Count(body, "<th>harness</th>"); n != 2 {
		t.Errorf("expected 2 harness column headers (enabled + disabled tables), got %d", n)
	}
}
