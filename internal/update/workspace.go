package update

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/blackpaw-studio/leo/internal/templates"
)

// RefreshWorkspace re-renders workspace template files and writes any that
// differ from the embedded source. CLAUDE.md and skills/*.md are binary-owned
// reference material: they're rewritten whenever the on-disk bytes don't
// match the embedded bytes, and left alone otherwise. HEARTBEAT.md is only
// written if missing (may be user-customized).
//
// Safe to call unconditionally on every service startup — the content diff
// keeps no-op runs cheap and avoids mtime churn.
//
// Returns the list of files that were (re)written.
func RefreshWorkspace(workspace string) ([]string, error) {
	var written []string

	if err := os.MkdirAll(workspace, 0750); err != nil {
		return written, fmt.Errorf("creating workspace directory: %w", err)
	}

	claudeContent, err := templates.RenderClaudeWorkspace(templates.AgentData{
		Workspace: workspace,
	})
	if err != nil {
		return written, fmt.Errorf("rendering CLAUDE.md: %w", err)
	}
	claudePath := filepath.Join(workspace, "CLAUDE.md")
	wrote, err := writeIfChanged(claudePath, []byte(claudeContent), 0644)
	if err != nil {
		return written, fmt.Errorf("writing CLAUDE.md: %w", err)
	}
	if wrote {
		written = append(written, claudePath)
	}

	skillsDir := filepath.Join(workspace, "skills")
	if err := os.MkdirAll(skillsDir, 0750); err != nil {
		return written, fmt.Errorf("creating skills directory: %w", err)
	}
	for _, skillName := range templates.SkillFiles() {
		content, err := templates.ReadSkill(skillName)
		if err != nil {
			return written, fmt.Errorf("reading skill %s: %w", skillName, err)
		}
		skillPath := filepath.Join(skillsDir, skillName)
		wrote, err := writeIfChanged(skillPath, []byte(content), 0644)
		if err != nil {
			return written, fmt.Errorf("writing skill %s: %w", skillName, err)
		}
		if wrote {
			written = append(written, skillPath)
		}
	}

	// HEARTBEAT.md stays user-customizable: only created if missing.
	heartbeatPath := filepath.Join(workspace, "HEARTBEAT.md")
	if _, err := os.Stat(heartbeatPath); os.IsNotExist(err) {
		heartbeatContent, err := templates.RenderHeartbeat()
		if err != nil {
			return written, fmt.Errorf("rendering heartbeat: %w", err)
		}
		if err := os.WriteFile(heartbeatPath, []byte(heartbeatContent), 0644); err != nil {
			return written, fmt.Errorf("writing HEARTBEAT.md: %w", err)
		}
		written = append(written, heartbeatPath)
	}

	return written, nil
}

// writeIfChanged writes data to path only if the existing file's contents
// differ (or the file doesn't exist / can't be read). Returns true iff a
// write occurred.
//
// Writes are atomic: data is written to a temp file in the same directory
// and renamed into place. This prevents readers (e.g. a claude subprocess
// reading CLAUDE.md) from ever seeing a partially-written file when two
// task runners start simultaneously.
func writeIfChanged(path string, data []byte, perm os.FileMode) (bool, error) {
	existing, err := os.ReadFile(path)
	if err == nil && bytes.Equal(existing, data) {
		return false, nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".leo-tmp-*")
	if err != nil {
		return false, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once renamed
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return false, err
	}
	return true, nil
}
