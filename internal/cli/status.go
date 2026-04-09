package cli

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/blackpaw-studio/leo/internal/cron"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/service"
	"github.com/spf13/cobra"
)

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show overall leo status",
		Long:  "Show service status, daemon state, enabled tasks, and next scheduled run.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				// Config is invalid, but still show what we can
				warn.Printf("Config: %v\n", err)
				return nil
			}
			success.Println("Config: valid")

			// Service status
			svcStatus, _ := service.Status(cfg.HomePath)
			fmt.Printf("Service: %s\n", svcStatus)

			// Daemon status
			daemonStatus, _ := service.DaemonStatus()
			fmt.Printf("Daemon:  %s\n", daemonStatus)

			// Processes
			enabledProcs := 0
			for _, p := range cfg.Processes {
				if p.Enabled {
					enabledProcs++
				}
			}
			fmt.Printf("Processes: %d/%d enabled\n", enabledProcs, len(cfg.Processes))

			// Tasks
			enabledTasks := 0
			for _, t := range cfg.Tasks {
				if t.Enabled {
					enabledTasks++
				}
			}
			fmt.Printf("Tasks:   %d/%d enabled\n", enabledTasks, len(cfg.Tasks))

			// Next scheduled run (from daemon if available)
			if daemon.IsRunning(cfg.HomePath) {
				resp, err := daemon.Send(cfg.HomePath, "GET", "/cron/list", nil)
				if err == nil && resp.OK {
					var entries []cron.EntryInfo
					if err := json.Unmarshal(resp.Data, &entries); err == nil && len(entries) > 0 {
						var earliest time.Time
						var earliestName string
						for _, e := range entries {
							if earliest.IsZero() || e.Next.Before(earliest) {
								earliest = e.Next
								earliestName = e.Name
							}
						}
						if !earliest.IsZero() {
							fmt.Printf("Next:    %s (%s)\n", earliestName, earliest.Format(time.Kitchen))
						}
					}
				}
			}

			return nil
		},
	}
}
