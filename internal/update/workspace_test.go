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

	written, err := RefreshWorkspace(dir)
	if err != nil {
		t.Fatalf("RefreshWorkspace() error: %v", err)
	}

	// Should write CLAUDE.md + 5 skills + HEARTBEAT.md = 7 files
	expectedCount := 1 + len(templates.SkillFiles()) + 1
	if len(written) != expectedCount {
		t.Errorf("wrote %d files, want %d", len(written), expectedCount)
	}

	// Verify CLAUDE.md content
	data, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if err != nil {
		t.Fatal("CLAUDE.md not created")
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

	written, err := RefreshWorkspace(dir)
	if err != nil {
		t.Fatalf("RefreshWorkspace() error: %v", err)
	}

	claudePath := filepath.Join(dir, "CLAUDE.md")
	skillPath := filepath.Join(dir, "skills", firstSkill)
	if !contains(written, claudePath) {
		t.Errorf("written list missing %s: %v", claudePath, written)
	}
	if !contains(written, skillPath) {
		t.Errorf("written list missing %s: %v", skillPath, written)
	}

	// CLAUDE.md should be overwritten
	data, _ := os.ReadFile(claudePath)
	if string(data) == "old content" {
		t.Error("CLAUDE.md was not overwritten")
	}

	// Skill should be overwritten
	data, _ = os.ReadFile(skillPath)
	if string(data) == "old skill" {
		t.Error("skill file was not overwritten")
	}
}

func TestRefreshWorkspaceNoopWhenCurrent(t *testing.T) {
	dir := t.TempDir()

	first, err := RefreshWorkspace(dir)
	if err != nil {
		t.Fatalf("first RefreshWorkspace() error: %v", err)
	}
	if len(first) == 0 {
		t.Fatal("first call wrote nothing; expected initial population")
	}

	// Snapshot mtimes of every managed file.
	type fileStat struct {
		path  string
		mtime int64
	}
	statPaths := []string{filepath.Join(dir, "CLAUDE.md")}
	for _, skill := range templates.SkillFiles() {
		statPaths = append(statPaths, filepath.Join(dir, "skills", skill))
	}
	snapshots := make([]fileStat, 0, len(statPaths))
	for _, p := range statPaths {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		snapshots = append(snapshots, fileStat{path: p, mtime: info.ModTime().UnixNano()})
	}

	// Second call: everything is current, nothing should be rewritten.
	second, err := RefreshWorkspace(dir)
	if err != nil {
		t.Fatalf("second RefreshWorkspace() error: %v", err)
	}
	if len(second) != 0 {
		t.Errorf("second call rewrote files unexpectedly: %v", second)
	}

	for _, snap := range snapshots {
		info, err := os.Stat(snap.path)
		if err != nil {
			t.Fatalf("stat %s: %v", snap.path, err)
		}
		if got := info.ModTime().UnixNano(); got != snap.mtime {
			t.Errorf("%s mtime changed: before=%d after=%d", snap.path, snap.mtime, got)
		}
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func TestRefreshWorkspaceSkipsExistingHeartbeat(t *testing.T) {
	dir := t.TempDir()

	// Pre-create HEARTBEAT.md with custom content
	os.WriteFile(filepath.Join(dir, "HEARTBEAT.md"), []byte("custom heartbeat"), 0644)

	written, err := RefreshWorkspace(dir)
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
