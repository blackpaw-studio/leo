package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/blackpaw-studio/leo/internal/config"
)

// startupWarnings returns a slice of non-fatal warnings about the current
// config. It runs the config-driven subset of `leo validate` — checks that
// should be surfaced when the service starts so misconfigurations fail early
// and visibly rather than silently at first task/process invocation.
//
// Prerequisite (claude/tmux/bun) and daemon health checks are intentionally
// omitted: this function is called before those subsystems are relevant.
func startupWarnings(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	var warnings []string

	// Default workspace directory exists.
	if ws := cfg.DefaultWorkspace(); ws != "" {
		if _, err := os.Stat(ws); err != nil {
			warnings = append(warnings, fmt.Sprintf("default workspace %s does not exist", ws))
		}
	}

	// Each enabled process has an existing workspace.
	for name, proc := range cfg.Processes {
		if !proc.Enabled {
			continue
		}
		ws := cfg.ProcessWorkspace(proc)
		if _, err := os.Stat(ws); err != nil {
			warnings = append(warnings, fmt.Sprintf("process %q: workspace %s does not exist", name, ws))
		}
	}

	// Each enabled task has an existing prompt file.
	for name, task := range cfg.Tasks {
		if !task.Enabled {
			continue
		}
		ws := cfg.TaskWorkspace(task)
		promptPath := filepath.Join(ws, task.PromptFile)
		if _, err := os.Stat(promptPath); err != nil {
			warnings = append(warnings, fmt.Sprintf("task %q: prompt file %s not found", name, promptPath))
		}
	}

	// MCP configs, if present, parse as JSON.
	for name, proc := range cfg.Processes {
		mcpPath := cfg.ProcessMCPConfigPath(proc)
		if mcpPath == "" {
			continue
		}
		data, err := os.ReadFile(mcpPath)
		if err != nil {
			if !os.IsNotExist(err) {
				warnings = append(warnings, fmt.Sprintf("process %q MCP config %s unreadable: %v", name, mcpPath, err))
			}
			continue
		}
		var parsed map[string]json.RawMessage
		if err := json.Unmarshal(data, &parsed); err != nil {
			warnings = append(warnings, fmt.Sprintf("process %q MCP config %s is not valid JSON", name, mcpPath))
		}
	}

	return warnings
}
