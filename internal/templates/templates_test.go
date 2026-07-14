package templates

import (
	"strings"
	"testing"
)

func TestSkillFiles(t *testing.T) {
	skills := SkillFiles()

	if len(skills) != 6 {
		t.Fatalf("SkillFiles() returned %d files, want 6", len(skills))
	}

	want := map[string]bool{
		"managing-tasks.md":        true,
		"debugging-logs.md":        true,
		"daemon-management.md":     true,
		"config-reference.md":      true,
		"workspace-maintenance.md": true,
		"agent-management.md":      true,
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

func TestSkillCatalog(t *testing.T) {
	catalog, err := SkillCatalog()
	if err != nil {
		t.Fatalf("SkillCatalog() error: %v", err)
	}

	if len(catalog) != len(SkillFiles()) {
		t.Fatalf("SkillCatalog() returned %d entries, want %d", len(catalog), len(SkillFiles()))
	}

	var managingTasks *SkillMeta
	for i := range catalog {
		meta := catalog[i]

		if meta.Name == "" {
			t.Errorf("entry has empty Name: %+v", meta)
		}
		if strings.HasSuffix(meta.Name, ".md") {
			t.Errorf("Name %q should not include .md suffix", meta.Name)
		}
		if meta.Title == "" {
			t.Errorf("entry %q has empty Title", meta.Name)
		}
		if meta.Summary == "" {
			t.Errorf("entry %q has empty Summary", meta.Name)
		}
		if strings.Contains(meta.Summary, "\n") {
			t.Errorf("entry %q Summary should be a single line, got %q", meta.Name, meta.Summary)
		}

		if meta.Name == "managing-tasks" {
			m := meta
			managingTasks = &m
		}
	}

	if managingTasks == nil {
		t.Fatal("expected a managing-tasks entry")
	}
	if managingTasks.Title != "Managing Tasks" {
		t.Errorf("managing-tasks Title = %q, want %q", managingTasks.Title, "Managing Tasks")
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
