package templates

import (
	"strings"
	"testing"
)

func TestRenderHeartbeat(t *testing.T) {
	result, err := RenderHeartbeat()
	if err != nil {
		t.Fatalf("RenderHeartbeat() error: %v", err)
	}

	if result == "" {
		t.Error("RenderHeartbeat returned empty string")
	}
}

func TestRenderClaudeWorkspace(t *testing.T) {
	data := AgentData{
		Workspace: "/home/user/myagent",
	}

	result, err := RenderClaudeWorkspace(data)
	if err != nil {
		t.Fatalf("RenderClaudeWorkspace() error: %v", err)
	}

	if result == "" {
		t.Error("RenderClaudeWorkspace returned empty string")
	}

	if strings.Contains(result, "{{") {
		t.Error("rendered output contains unresolved template directives")
	}

	if !strings.Contains(result, "/home/user/myagent") {
		t.Error("rendered output missing workspace path")
	}
}

func TestSkillFiles(t *testing.T) {
	skills := SkillFiles()

	if len(skills) != 5 {
		t.Fatalf("SkillFiles() returned %d files, want 5", len(skills))
	}

	want := map[string]bool{
		"managing-tasks.md":        true,
		"debugging-logs.md":        true,
		"daemon-management.md":     true,
		"config-reference.md":      true,
		"workspace-maintenance.md": true,
	}

	for _, name := range skills {
		if !want[name] {
			t.Errorf("unexpected skill file: %q", name)
		}
	}
}

func TestReadSkill(t *testing.T) {
	for _, name := range SkillFiles() {
		t.Run(name, func(t *testing.T) {
			content, err := ReadSkill(name)
			if err != nil {
				t.Fatalf("ReadSkill(%q) error: %v", name, err)
			}

			if content == "" {
				t.Error("ReadSkill returned empty string")
			}
		})
	}
}

func TestReadSkillInvalid(t *testing.T) {
	_, err := ReadSkill("nonexistent.md")
	if err == nil {
		t.Error("expected error for nonexistent skill")
	}
}

func TestRenderUserProfile(t *testing.T) {
	data := UserProfileData{
		UserName:    "Alice",
		Role:        "Engineer",
		About:       "Builds things",
		Preferences: "Dark mode",
		Timezone:    "America/New_York",
	}

	result, err := RenderUserProfile(data)
	if err != nil {
		t.Fatalf("RenderUserProfile() error: %v", err)
	}

	checks := []struct {
		field string
		value string
	}{
		{"UserName", data.UserName},
		{"Role", data.Role},
		{"About", data.About},
		{"Preferences", data.Preferences},
		{"Timezone", data.Timezone},
	}

	for _, check := range checks {
		if !strings.Contains(result, check.value) {
			t.Errorf("rendered profile missing %s value %q", check.field, check.value)
		}
	}
}
