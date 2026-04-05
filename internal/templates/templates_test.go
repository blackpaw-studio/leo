package templates

import (
	"strings"
	"testing"
)

func TestAgentTemplates(t *testing.T) {
	templates := AgentTemplates()

	if len(templates) != 3 {
		t.Fatalf("AgentTemplates() returned %d templates, want 3", len(templates))
	}

	want := map[string]bool{
		"chief-of-staff": true,
		"dev-assistant":  true,
		"skeleton":       true,
	}

	for _, name := range templates {
		if !want[name] {
			t.Errorf("unexpected template name: %q", name)
		}
	}
}

func TestRenderAgent(t *testing.T) {
	data := AgentData{
		Name:      "rocket",
		UserName:  "Evan",
		Workspace: "/home/user/rocket",
	}

	for _, name := range AgentTemplates() {
		t.Run(name, func(t *testing.T) {
			result, err := RenderAgent(name, data)
			if err != nil {
				t.Fatalf("RenderAgent(%q) error: %v", name, err)
			}

			if result == "" {
				t.Error("RenderAgent returned empty string")
			}

			if strings.Contains(result, "{{") {
				t.Error("rendered output contains unresolved template directives")
			}
		})
	}
}

func TestRenderAgentInvalid(t *testing.T) {
	_, err := RenderAgent("nonexistent", AgentData{Name: "test"})
	if err == nil {
		t.Error("expected error for nonexistent template")
	}
}

func TestRenderHeartbeat(t *testing.T) {
	result, err := RenderHeartbeat()
	if err != nil {
		t.Fatalf("RenderHeartbeat() error: %v", err)
	}

	if result == "" {
		t.Error("RenderHeartbeat returned empty string")
	}
}

func TestRenderUserProfile(t *testing.T) {
	data := UserProfileData{
		UserName:    "Evan",
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
