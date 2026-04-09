package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/prereq"
	"github.com/blackpaw-studio/leo/internal/service"
	"github.com/spf13/cobra"
)

func newValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Check config, prerequisites, and workspace health",
		Long:  "Run diagnostic checks on config, prerequisites, daemon, and workspace. Like a doctor's checkup for your leo setup.",
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
				warn.Println("tmux: not found (required for background service)")
				issues++
			}

			if prereq.CheckBun() {
				success.Println("bun: installed")
			} else {
				warn.Println("bun: not found (required for telegram plugin)")
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

			// 5. Check MCP config
			mcpPath := cfg.MCPConfigPath()
			if _, err := os.Stat(mcpPath); err == nil {
				data, readErr := os.ReadFile(mcpPath)
				if readErr != nil {
					warn.Printf("MCP config: %s unreadable\n", mcpPath)
					issues++
				} else {
					var parsed map[string]json.RawMessage
					if json.Unmarshal(data, &parsed) != nil {
						warn.Printf("MCP config: %s is not valid JSON\n", mcpPath)
						issues++
					} else {
						success.Printf("MCP config: %s\n", mcpPath)
					}
				}
			} else {
				info.Println("MCP config: not configured (optional)")
			}

			// 6. Check daemon health
			if daemon.IsRunning(cfg.Agent.Workspace) {
				resp, err := daemon.Send(cfg.Agent.Workspace, "GET", "/health", nil)
				if err != nil {
					warn.Println("Daemon: socket exists but not responding")
					issues++
				} else if resp.OK {
					success.Println("Daemon: healthy")
				} else {
					warn.Printf("Daemon: unhealthy (%s)\n", resp.Error)
					issues++
				}
			} else {
				info.Println("Daemon: not running")
			}

			// 7. Check service status
			svcStatus, _ := service.Status(cfg.Agent.Workspace)
			if svcStatus == "stopped" {
				info.Println("Service: stopped")
			} else {
				success.Printf("Service: %s\n", svcStatus)
			}

			// 8. Check service log
			logPath := service.LogPathFor(cfg.Agent.Workspace)
			if fi, err := os.Stat(logPath); err == nil {
				success.Printf("Service log: %s (%.0f KB)\n", logPath, float64(fi.Size())/1024)
			} else {
				info.Println("Service log: not present (service hasn't run yet)")
			}

			// 9. Summary
			fmt.Println()
			if issues == 0 {
				success.Println("All checks passed.")
				return nil
			}
			return fmt.Errorf("%d issue(s) found — run 'leo validate' after fixing to verify", issues)
		},
	}
}
