package cli

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/cron"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/service"
	"github.com/spf13/cobra"
)

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show overall leo status",
		Long:  "Show service status, daemon state, processes, tasks, and next scheduled run.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus()
		},
	}
}

func runStatus() error {
	cfg, err := loadConfig()
	if err != nil {
		warn.Printf("Config: %v\n", err)
		return nil
	}
	success.Println("Config: valid")
	info.Printf("Home:    %s\n", cfg.HomePath)

	// Service status
	svcStatus, _ := service.Status(cfg.HomePath)
	if svcStatus == "running" {
		success.Printf("Service: %s\n", svcStatus)
	} else {
		warn.Printf("Service: %s\n", svcStatus)
	}

	// Daemon status
	daemonStatus, _ := service.DaemonStatus()
	if daemonStatus == "running" {
		success.Printf("Daemon:  %s\n", daemonStatus)
	} else {
		info.Printf("Daemon:  %s\n", daemonStatus)
	}

	// Web UI
	if cfg.Web.Enabled {
		bind := cfg.WebBind()
		if config.IsLoopbackBind(bind) {
			info.Printf("Web UI:  http://%s:%d\n", bind, cfg.WebPort())
		} else {
			warn.Printf("Web UI:  http://%s:%d (non-loopback bind; no built-in auth)\n", bind, cfg.WebPort())
		}
	}

	// Processes
	enabledProcs := 0
	for _, p := range cfg.Processes {
		if p.Enabled {
			enabledProcs++
		}
	}
	info.Printf("Processes: %d/%d enabled\n", enabledProcs, len(cfg.Processes))

	// Show per-process status from daemon if available
	if daemon.IsRunning(cfg.HomePath) {
		resp, err := daemon.Send(cfg.HomePath, "GET", "/process/list", nil)
		if err == nil && resp.OK {
			var states map[string]daemon.ProcessStateInfo
			if json.Unmarshal(resp.Data, &states) == nil && len(states) > 0 {
				for name, state := range states {
					uptime := ""
					if !state.StartedAt.IsZero() {
						uptime = fmt.Sprintf(" uptime %s", time.Since(state.StartedAt).Round(time.Second))
					}
					restarts := ""
					if state.Restarts > 0 {
						restarts = fmt.Sprintf(" %d restart(s)", state.Restarts)
					}
					switch state.Status {
					case "running":
						success.Printf("  %-20s %s%s%s\n", name, state.Status, uptime, restarts)
					case "restarting":
						warn.Printf("  %-20s %s%s%s\n", name, state.Status, uptime, restarts)
					default:
						info.Printf("  %-20s %s%s%s\n", name, state.Status, uptime, restarts)
					}
				}
			}
		}
	}

	// Tasks
	enabledTasks := 0
	for _, t := range cfg.Tasks {
		if t.Enabled {
			enabledTasks++
		}
	}
	info.Printf("Tasks:   %d/%d enabled\n", enabledTasks, len(cfg.Tasks))

	if n := len(cfg.Templates); n > 0 {
		info.Printf("Templates: %d\n", n)
	}

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
					info.Printf("Next:    %s (%s)\n", earliestName, earliest.Format(time.Kitchen))
				}
			}
		}
	}

	return nil
}
