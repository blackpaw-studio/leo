package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/blackpaw-studio/leo/internal/prereq"
	"github.com/spf13/cobra"
)

func newValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Check config, prerequisites, and workspace health",
		RunE: func(cmd *cobra.Command, args []string) error {
			issues := 0

			// 1. Load and validate config
			cfg, err := loadConfig()
			if err != nil {
				warn.Printf("Config: %v\n", err)
				issues++
			} else {
				success.Println("Config: valid")
			}

			if cfg == nil {
				return fmt.Errorf("cannot continue without valid config")
			}

			// 2. Check prerequisites
			claude := prereq.CheckClaude()
			if claude.OK {
				v := claude.Version
				if v == "" {
					v = "installed"
				}
				success.Printf("claude CLI: %s\n", v)
			} else {
				warn.Println("claude CLI: not found")
				issues++
			}

			if prereq.CheckTmux() {
				success.Println("tmux: installed")
			} else {
				warn.Println("tmux: not found")
				issues++
			}

			if prereq.CheckBun() {
				success.Println("bun: installed")
			} else {
				warn.Println("bun: not found")
				issues++
			}

			// 3. Check workspace
			if _, err := os.Stat(cfg.Agent.Workspace); err != nil {
				warn.Printf("Workspace: %s does not exist\n", cfg.Agent.Workspace)
				issues++
			} else {
				success.Printf("Workspace: %s\n", cfg.Agent.Workspace)
			}

			// 4. Check prompt files for enabled tasks
			for name, task := range cfg.Tasks {
				if !task.Enabled {
					continue
				}
				promptPath := filepath.Join(cfg.Agent.Workspace, task.PromptFile)
				if _, err := os.Stat(promptPath); err != nil {
					warn.Printf("Task %q: prompt file %s not found\n", name, promptPath)
					issues++
				}
			}

			// 5. Summary
			fmt.Println()
			if issues == 0 {
				success.Println("All checks passed.")
				return nil
			}
			return fmt.Errorf("%d issue(s) found", issues)
		},
	}
}
