package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/cron"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/service"
	"github.com/spf13/cobra"
)

// badProcessStates are daemon-reported statuses we consider unhealthy.
var badProcessStates = map[string]bool{
	"stopped":    true,
	"crashed":    true,
	"exited":     true,
	"failed":     true,
	"restarting": true,
}

// StatusReport is the structured representation of `leo status`, used by --json.
type StatusReport struct {
	LeoVersion    string                  `json:"leo_version"`
	HomePath      string                  `json:"home_path"`
	ConfigValid   bool                    `json:"config_valid"`
	ConfigError   string                  `json:"config_error,omitempty"`
	Service       string                  `json:"service"`
	Daemon        string                  `json:"daemon"`
	Web           *StatusWeb              `json:"web,omitempty"`
	Processes     StatusProcessesSummary  `json:"processes"`
	ProcessStates []StatusProcessState    `json:"process_states,omitempty"`
	BadProcesses  []string                `json:"bad_processes,omitempty"`
	Tasks         StatusTasksSummary      `json:"tasks"`
	TaskIssues    []StatusTaskIssue       `json:"task_issues,omitempty"`
	Templates     int                     `json:"templates,omitempty"`
	Next          *StatusNextScheduledRun `json:"next_scheduled_run,omitempty"`
}

// StatusWeb captures the resolved web UI config for the status report.
type StatusWeb struct {
	Bind     string `json:"bind"`
	Port     int    `json:"port"`
	Loopback bool   `json:"loopback"`
	URL      string `json:"url"`
}

// StatusProcessesSummary holds process counts.
type StatusProcessesSummary struct {
	Enabled int `json:"enabled"`
	Total   int `json:"total"`
}

// StatusProcessState mirrors daemon.ProcessStateInfo but trims fields for
// the status report.
type StatusProcessState struct {
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	StartedAt time.Time `json:"started_at,omitempty"`
	Restarts  int       `json:"restarts,omitempty"`
}

// StatusTasksSummary holds task counts.
type StatusTasksSummary struct {
	Enabled int `json:"enabled"`
	Total   int `json:"total"`
}

// StatusTaskIssue describes a basic task problem surfaced from status.
type StatusTaskIssue struct {
	Name    string `json:"name"`
	Problem string `json:"problem"`
}

// StatusNextScheduledRun points at the nearest upcoming task fire time.
type StatusNextScheduledRun struct {
	Name string    `json:"name"`
	Next time.Time `json:"next"`
}

func newStatusCmd() *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show overall leo status",
		Long:  "Show service status, daemon state, processes, tasks, and next scheduled run.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if asJSON {
				return runStatusJSON()
			}
			return runStatus()
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "emit status as JSON (suitable for scripting)")

	return cmd
}

// buildStatusReport assembles the structured StatusReport used by both text
// and JSON output. Config-load failure surfaces as ConfigError with
// ConfigValid=false; the caller can still render the rest of the fields that
// don't depend on config.
func buildStatusReport() StatusReport {
	report := StatusReport{
		LeoVersion: Version,
	}

	cfg, err := loadConfig()
	if err != nil {
		report.ConfigValid = false
		report.ConfigError = err.Error()
		return report
	}
	report.ConfigValid = true
	report.HomePath = cfg.HomePath

	svcStatus, _ := service.Status(cfg.HomePath)
	report.Service = svcStatus

	daemonStatus, _ := service.DaemonStatus()
	report.Daemon = daemonStatus

	if cfg.Web.Enabled {
		bind := cfg.WebBind()
		port := cfg.WebPort()
		report.Web = &StatusWeb{
			Bind:     bind,
			Port:     port,
			Loopback: config.IsLoopbackBind(bind),
			URL:      fmt.Sprintf("http://%s:%d", bind, port),
		}
	}

	// Process counts + states.
	for _, p := range cfg.Processes {
		report.Processes.Total++
		if p.Enabled {
			report.Processes.Enabled++
		}
	}

	if daemon.IsRunning(cfg.HomePath) {
		if states, err := fetchProcessStates(cfg.HomePath); err == nil {
			names := make([]string, 0, len(states))
			for name := range states {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				state := states[name]
				report.ProcessStates = append(report.ProcessStates, StatusProcessState{
					Name:      name,
					Status:    state.Status,
					StartedAt: state.StartedAt,
					Restarts:  state.Restarts,
				})
				if badProcessStates[state.Status] {
					report.BadProcesses = append(report.BadProcesses, name)
				}
			}
		}
	}

	// Task counts + simple validation issues (missing prompt file, invalid cron).
	for name, t := range cfg.Tasks {
		report.Tasks.Total++
		if t.Enabled {
			report.Tasks.Enabled++
		}
		if issue := taskProblem(cfg, name, t); issue != "" {
			report.TaskIssues = append(report.TaskIssues, StatusTaskIssue{Name: name, Problem: issue})
		}
	}
	sort.Slice(report.TaskIssues, func(i, j int) bool { return report.TaskIssues[i].Name < report.TaskIssues[j].Name })

	report.Templates = len(cfg.Templates)

	if daemon.IsRunning(cfg.HomePath) {
		if next, name, ok := fetchNextScheduledRun(cfg.HomePath); ok {
			report.Next = &StatusNextScheduledRun{Name: name, Next: next}
		}
	}

	return report
}

// fetchProcessStates returns the current process states from the daemon.
func fetchProcessStates(homePath string) (map[string]daemon.ProcessStateInfo, error) {
	resp, err := daemon.Send(homePath, "GET", "/process/list", nil)
	if err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, fmt.Errorf("daemon returned error: %s", resp.Error)
	}
	var states map[string]daemon.ProcessStateInfo
	if err := json.Unmarshal(resp.Data, &states); err != nil {
		return nil, err
	}
	return states, nil
}

// fetchNextScheduledRun picks the nearest upcoming cron fire time.
func fetchNextScheduledRun(homePath string) (time.Time, string, bool) {
	resp, err := daemon.Send(homePath, "GET", "/cron/list", nil)
	if err != nil || !resp.OK {
		return time.Time{}, "", false
	}
	var entries []cron.EntryInfo
	if err := json.Unmarshal(resp.Data, &entries); err != nil || len(entries) == 0 {
		return time.Time{}, "", false
	}
	var earliest time.Time
	var earliestName string
	for _, e := range entries {
		if earliest.IsZero() || e.Next.Before(earliest) {
			earliest = e.Next
			earliestName = e.Name
		}
	}
	if earliest.IsZero() {
		return time.Time{}, "", false
	}
	return earliest, earliestName, true
}

// taskProblem returns a short human-readable description of a task-level
// config problem, or "" if nothing is obviously wrong. Keeps the logic
// cheap — this is called on every status render, not every validate pass.
func taskProblem(cfg *config.Config, name string, t config.TaskConfig) string {
	if t.PromptFile == "" {
		return "missing prompt_file"
	}
	if t.Schedule == "" {
		return "missing schedule"
	}
	// Don't re-run cron parsing here; Config.Validate() already rejects a
	// malformed schedule before loadConfig returns. Surfacing missing prompt
	// files is the useful runtime signal that might drift between edits.
	_ = cfg
	return ""
}

// runStatusJSON emits the structured StatusReport as JSON to stdout.
func runStatusJSON() error {
	report := buildStatusReport()
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

func runStatus() error {
	report := buildStatusReport()

	info.Printf("leo:     %s\n", report.LeoVersion)
	if !report.ConfigValid {
		warn.Printf("Config: %s\n", report.ConfigError)
		return nil
	}
	success.Println("Config:  valid")
	info.Printf("Home:    %s\n", report.HomePath)

	// Service status
	if report.Service == "running" {
		success.Printf("Service: %s\n", report.Service)
	} else {
		warn.Printf("Service: %s\n", report.Service)
	}

	// Daemon status
	if report.Daemon == "running" {
		success.Printf("Daemon:  %s\n", report.Daemon)
	} else {
		info.Printf("Daemon:  %s\n", report.Daemon)
	}

	// Web UI
	if report.Web != nil {
		if report.Web.Loopback {
			info.Printf("Web UI:  %s\n", report.Web.URL)
		} else {
			warn.Printf("Web UI:  %s (non-loopback bind; token auth required)\n", report.Web.URL)
		}
	}

	// Processes
	info.Printf("Processes: %d/%d enabled\n", report.Processes.Enabled, report.Processes.Total)
	for _, state := range report.ProcessStates {
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
			success.Printf("  %-20s %s%s%s\n", state.Name, state.Status, uptime, restarts)
		case "restarting":
			warn.Printf("  %-20s %s%s%s\n", state.Name, state.Status, uptime, restarts)
		default:
			info.Printf("  %-20s %s%s%s\n", state.Name, state.Status, uptime, restarts)
		}
	}
	if len(report.BadProcesses) > 0 {
		warn.Printf("%d process(es) in bad state: %s\n",
			len(report.BadProcesses),
			joinNames(report.BadProcesses))
	}

	// Tasks
	info.Printf("Tasks:   %d/%d enabled\n", report.Tasks.Enabled, report.Tasks.Total)
	if len(report.TaskIssues) > 0 {
		warn.Printf("%d task(s) with issues:\n", len(report.TaskIssues))
		for _, issue := range report.TaskIssues {
			warn.Printf("  %-20s %s\n", issue.Name, issue.Problem)
		}
	}

	if report.Templates > 0 {
		info.Printf("Templates: %d\n", report.Templates)
	}

	if report.Next != nil {
		info.Printf("Next:    %s (%s)\n", report.Next.Name, report.Next.Next.Format(time.Kitchen))
	}

	return nil
}

// joinNames returns a comma-separated list of names, trimmed for output.
func joinNames(names []string) string {
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}
