package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/spf13/cobra"
)

func newProcessCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "process",
		Short: "Manage supervised processes",
	}

	cmd.AddCommand(newProcessListCmd())
	return cmd
}

func newProcessListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured processes and their runtime status",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			// Show config-defined processes
			if len(cfg.Processes) == 0 {
				info.Println("No processes configured.")
				return nil
			}

			// Try to get runtime state from daemon
			var states map[string]daemon.ProcessStateInfo
			if daemon.IsRunning(cfg.HomePath) {
				resp, err := daemon.Send(cfg.HomePath, "GET", "/process/list", nil)
				if err == nil && resp.OK {
					json.Unmarshal(resp.Data, &states)
				}
			}

			for name, proc := range cfg.Processes {
				status := "disabled"
				if proc.Enabled {
					status = "enabled"
				}

				ws := cfg.ProcessWorkspace(proc)
				model := cfg.ProcessModel(proc)
				channels := "-"
				if len(proc.Channels) > 0 {
					channels = strings.Join(proc.Channels, ", ")
				}

				// Override with runtime status if available
				runtime := ""
				if state, ok := states[name]; ok {
					runtime = fmt.Sprintf("  [%s", state.Status)
					if state.Restarts > 0 {
						runtime += fmt.Sprintf(", %d restart(s)", state.Restarts)
					}
					runtime += "]"
				}

				fmt.Printf("  %-20s %-8s %-8s%s\n", name, model, status, runtime)
				fmt.Printf("    workspace: %s\n", ws)
				if channels != "-" {
					fmt.Printf("    channels:  %s\n", channels)
				}
			}
			return nil
		},
	}
}
