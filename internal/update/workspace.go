package update

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/blackpaw-studio/leo/internal/templates"
)

// RefreshWorkspace re-renders and writes template files in the workspace.
// CLAUDE.md and skills/*.md are always overwritten (generated reference material).
// HEARTBEAT.md is only written if missing (may be user-customized).
// Returns a list of files that were written.
func RefreshWorkspace(agentName, workspace string) ([]string, error) {
	var written []string

	// Always overwrite CLAUDE.md
	claudeContent, err := templates.RenderClaudeWorkspace(templates.AgentData{
		Name:      agentName,
		Workspace: workspace,
	})
	if err != nil {
		return written, fmt.Errorf("rendering CLAUDE.md: %w", err)
	}
	claudePath := filepath.Join(workspace, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte(claudeContent), 0644); err != nil {
		return written, fmt.Errorf("writing CLAUDE.md: %w", err)
	}
	written = append(written, claudePath)

	// Always overwrite skill files
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
		if err := os.WriteFile(skillPath, []byte(content), 0644); err != nil {
			return written, fmt.Errorf("writing skill %s: %w", skillName, err)
		}
		written = append(written, skillPath)
	}

	// Write HEARTBEAT.md only if missing
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
