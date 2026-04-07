package update

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/templates"
)

func TestRefreshWorkspace(t *testing.T) {
	dir := t.TempDir()

	written, err := RefreshWorkspace("testagent", dir, TelegramInfo{})
	if err != nil {
		t.Fatalf("RefreshWorkspace() error: %v", err)
	}

	// Should write CLAUDE.md + 5 skills = 6 files (no HEARTBEAT.md since it doesn't exist yet... wait, it should write it)
	// Actually HEARTBEAT.md is written when missing, so 7 total
	expectedCount := 1 + len(templates.SkillFiles()) + 1 // CLAUDE.md + skills + HEARTBEAT.md
	if len(written) != expectedCount {
		t.Errorf("wrote %d files, want %d", len(written), expectedCount)
	}

	// Verify CLAUDE.md content
	data, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if err != nil {
		t.Fatal("CLAUDE.md not created")
	}
	if !strings.Contains(string(data), "testagent") {
		t.Error("CLAUDE.md missing agent name")
	}
	if !strings.Contains(string(data), dir) {
		t.Error("CLAUDE.md missing workspace path")
	}

	// Verify skills
	for _, skill := range templates.SkillFiles() {
		if _, err := os.Stat(filepath.Join(dir, "skills", skill)); err != nil {
			t.Errorf("skill %s not created", skill)
		}
	}

	// Verify HEARTBEAT.md
	if _, err := os.Stat(filepath.Join(dir, "HEARTBEAT.md")); err != nil {
		t.Error("HEARTBEAT.md not created")
	}
}

func TestRefreshWorkspaceOverwrites(t *testing.T) {
	dir := t.TempDir()

	// Pre-create CLAUDE.md and a skill with custom content
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("old content"), 0644)
	os.MkdirAll(filepath.Join(dir, "skills"), 0755)
	firstSkill := templates.SkillFiles()[0]
	os.WriteFile(filepath.Join(dir, "skills", firstSkill), []byte("old skill"), 0644)

	_, err := RefreshWorkspace("testagent", dir, TelegramInfo{})
	if err != nil {
		t.Fatalf("RefreshWorkspace() error: %v", err)
	}

	// CLAUDE.md should be overwritten
	data, _ := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if string(data) == "old content" {
		t.Error("CLAUDE.md was not overwritten")
	}

	// Skill should be overwritten
	data, _ = os.ReadFile(filepath.Join(dir, "skills", firstSkill))
	if string(data) == "old skill" {
		t.Error("skill file was not overwritten")
	}
}

func TestRefreshWorkspaceSkipsExistingHeartbeat(t *testing.T) {
	dir := t.TempDir()

	// Pre-create HEARTBEAT.md with custom content
	os.WriteFile(filepath.Join(dir, "HEARTBEAT.md"), []byte("custom heartbeat"), 0644)

	written, err := RefreshWorkspace("testagent", dir, TelegramInfo{})
	if err != nil {
		t.Fatalf("RefreshWorkspace() error: %v", err)
	}

	// HEARTBEAT.md should NOT be in the written list
	for _, path := range written {
		if filepath.Base(path) == "HEARTBEAT.md" {
			t.Error("HEARTBEAT.md should not have been written when it already exists")
		}
	}

	// Content should be unchanged
	data, _ := os.ReadFile(filepath.Join(dir, "HEARTBEAT.md"))
	if string(data) != "custom heartbeat" {
		t.Errorf("HEARTBEAT.md was overwritten: %q", string(data))
	}
}
