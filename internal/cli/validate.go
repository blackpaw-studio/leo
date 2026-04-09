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

			// 3. Check default workspace
			defaultWS := cfg.DefaultWorkspace()
			if _, err := os.Stat(defaultWS); err != nil {
				warn.Printf("Default workspace: %s does not exist\n", defaultWS)
				issues++
			} else {
				success.Printf("Default workspace: %s\n", defaultWS)
			}

			// 4. Check process workspaces
			for name, proc := range cfg.Processes {
				ws := cfg.ProcessWorkspace(proc)
				if _, err := os.Stat(ws); err != nil {
					warn.Printf("Process %q workspace: %s does not exist\n", name, ws)
					issues++
				}
			}

			// 5. Check prompt files for enabled tasks
			for name, task := range cfg.Tasks {
				if !task.Enabled {
					continue
				}
				ws := cfg.TaskWorkspace(task)
				promptPath := filepath.Join(ws, task.PromptFile)
				if _, err := os.Stat(promptPath); err != nil {
					warn.Printf("Task %q: prompt file %s not found\n", name, promptPath)
					issues++
				}
			}

			// 6. Check MCP configs for processes
			for name, proc := range cfg.Processes {
				mcpPath := cfg.ProcessMCPConfigPath(proc)
				if _, err := os.Stat(mcpPath); err == nil {
					data, readErr := os.ReadFile(mcpPath)
					if readErr != nil {
						warn.Printf("Process %q MCP config: %s unreadable\n", name, mcpPath)
						issues++
					} else {
						var parsed map[string]json.RawMessage
						if json.Unmarshal(data, &parsed) != nil {
							warn.Printf("Process %q MCP config: %s is not valid JSON\n", name, mcpPath)
							issues++
						}
					}
				}
			}

			// 7. Check daemon health
			if daemon.IsRunning(cfg.HomePath) {
				resp, err := daemon.Send(cfg.HomePath, "GET", "/health", nil)
				switch {
				case err != nil:
					warn.Println("Daemon: socket exists but not responding")
					issues++
				case resp.OK:
					success.Println("Daemon: healthy")
				default:
					warn.Printf("Daemon: unhealthy (%s)\n", resp.Error)
					issues++
				}
			} else {
				info.Println("Daemon: not running")
			}

			// 8. Check service status
			svcStatus, _ := service.Status(cfg.HomePath)
			if svcStatus == "stopped" {
				info.Println("Service: stopped")
			} else {
				success.Printf("Service: %s\n", svcStatus)
			}

			// 9. Check service log
			logPath := service.LogPathFor(cfg.HomePath)
			if fi, err := os.Stat(logPath); err == nil {
				success.Printf("Service log: %s (%.0f KB)\n", logPath, float64(fi.Size())/1024)
			} else {
				info.Println("Service log: not present (service hasn't run yet)")
			}

			// 10. Summary
			fmt.Println()
			if issues == 0 {
				success.Println("All checks passed.")
				return nil
			}
			return fmt.Errorf("%d issue(s) found — run 'leo validate' after fixing to verify", issues)
		},
	}
}
