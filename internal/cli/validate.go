package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/prereq"
	"github.com/blackpaw-studio/leo/internal/service"
	"github.com/spf13/cobra"
)

// Severity classifies a validation finding.
type Severity string

const (
	SeverityError Severity = "ERROR"
	SeverityWarn  Severity = "WARN"
	SeverityInfo  Severity = "INFO"
)

// severityRank maps severity to a sort order: ERROR (0) first, WARN (1), INFO (2).
func severityRank(s Severity) int {
	switch s {
	case SeverityError:
		return 0
	case SeverityWarn:
		return 1
	case SeverityInfo:
		return 2
	default:
		return 3
	}
}

// Finding is a single validation result.
type Finding struct {
	Severity Severity `json:"severity"`
	Check    string   `json:"check"`
	Message  string   `json:"message"`
}

// logSizeWarnBytes is the threshold above which we flag the service log as
// large and suggest rotation. 50 MB per task spec.
const logSizeWarnBytes int64 = 50 * 1024 * 1024

func newValidateCmd() *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Check config, prerequisites, and workspace health",
		Long:  "Run diagnostic checks on config, prerequisites, daemon, and workspace. Like a doctor's checkup for your leo setup.",
		RunE: func(cmd *cobra.Command, args []string) error {
			findings, cfg := collectValidateFindings(cmd.Context())

			if asJSON {
				return emitValidateJSON(findings)
			}

			return emitValidateText(findings, cfg)
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "emit structured findings as JSON")

	return cmd
}

// collectValidateFindings runs all validation checks and returns findings plus
// the loaded config (may be nil if loading failed). Sorted by severity.
func collectValidateFindings(ctx context.Context) ([]Finding, *config.Config) {
	var findings []Finding
	add := func(sev Severity, check, msg string) {
		findings = append(findings, Finding{Severity: sev, Check: check, Message: msg})
	}

	// 1. Load and validate config
	cfg, err := loadConfig()
	if err != nil {
		add(SeverityError, "config", err.Error())
		return sortFindings(findings), nil
	}
	add(SeverityInfo, "config", "valid")

	// 2. Check prerequisites
	claude := prereq.CheckClaude()
	if claude.OK {
		v := claude.Version
		if v == "" {
			v = "installed"
		}
		add(SeverityInfo, "claude", v)
	} else {
		add(SeverityError, "claude", "claude CLI not found")
	}

	if prereq.CheckTmux() {
		add(SeverityInfo, "tmux", "installed")
	} else {
		add(SeverityWarn, "tmux", "tmux not found (required for background service)")
	}

	// 3. Default workspace
	defaultWS := cfg.DefaultWorkspace()
	if _, err := os.Stat(defaultWS); err != nil {
		add(SeverityWarn, "workspace", fmt.Sprintf("default workspace %s does not exist", defaultWS))
	} else {
		add(SeverityInfo, "workspace", defaultWS)
	}

	// 4. Process workspaces
	for name, proc := range cfg.Processes {
		ws := cfg.ProcessWorkspace(proc)
		if _, err := os.Stat(ws); err != nil {
			add(SeverityWarn, fmt.Sprintf("process:%s", name), fmt.Sprintf("workspace %s does not exist", ws))
		}
	}

	// 5. Prompt files for enabled tasks (missing prompt file is ERROR)
	for name, task := range cfg.Tasks {
		if !task.Enabled {
			continue
		}
		ws := cfg.TaskWorkspace(task)
		promptPath := filepath.Join(ws, task.PromptFile)
		if _, err := os.Stat(promptPath); err != nil {
			add(SeverityError, fmt.Sprintf("task:%s", name), fmt.Sprintf("prompt file %s not found", promptPath))
		}
	}

	// 6. MCP configs for processes
	for name, proc := range cfg.Processes {
		mcpPath := cfg.ProcessMCPConfigPath(proc)
		if _, err := os.Stat(mcpPath); err == nil {
			data, readErr := os.ReadFile(mcpPath)
			if readErr != nil {
				add(SeverityWarn, fmt.Sprintf("process:%s", name), fmt.Sprintf("MCP config %s unreadable", mcpPath))
				continue
			}
			var parsed map[string]json.RawMessage
			if json.Unmarshal(data, &parsed) != nil {
				add(SeverityError, fmt.Sprintf("process:%s", name), fmt.Sprintf("MCP config %s is not valid JSON", mcpPath))
			}
		}
	}

	// 7. Web bind exposure
	if cfg.Web.Enabled && !config.IsLoopbackBind(cfg.WebBind()) {
		add(SeverityWarn, "web", fmt.Sprintf("bind=%q exposes the dashboard beyond localhost; clients must authenticate with the api token", cfg.WebBind()))
	} else if !cfg.Web.Enabled {
		add(SeverityInfo, "web", "web UI disabled")
	}

	// 8. Daemon health. If the socket is present but unresponsive, that's ERROR —
	// someone clearly intended the daemon to be running.
	if daemon.IsRunning(cfg.HomePath) {
		resp, err := daemon.Send(ctx, cfg.HomePath, "GET", "/health", nil)
		switch {
		case err != nil:
			add(SeverityError, "daemon", "socket exists but daemon not responding")
		case resp.OK:
			add(SeverityInfo, "daemon", "healthy")
		default:
			add(SeverityError, "daemon", fmt.Sprintf("unhealthy: %s", resp.Error))
		}
	} else {
		add(SeverityInfo, "daemon", "not running")
	}

	// 9. Service status
	svcStatus, _ := service.Status(cfg.HomePath)
	add(SeverityInfo, "service", svcStatus)

	// 10. Service log — large logs are a WARN.
	logPath := service.LogPathFor(cfg.HomePath)
	if fi, err := os.Stat(logPath); err == nil {
		if fi.Size() > logSizeWarnBytes {
			add(SeverityWarn, "service-log", fmt.Sprintf("%s is %.0f MB (consider rotation)", logPath, float64(fi.Size())/(1024*1024)))
		} else {
			add(SeverityInfo, "service-log", fmt.Sprintf("%s (%.0f KB)", logPath, float64(fi.Size())/1024))
		}
	} else {
		add(SeverityInfo, "service-log", "not present (service hasn't run yet)")
	}

	return sortFindings(findings), cfg
}

// sortFindings returns findings sorted by severity (ERROR, WARN, INFO) while
// preserving original order within a severity bucket. Stable sort.
func sortFindings(findings []Finding) []Finding {
	out := make([]Finding, len(findings))
	copy(out, findings)
	sort.SliceStable(out, func(i, j int) bool {
		return severityRank(out[i].Severity) < severityRank(out[j].Severity)
	})
	return out
}

// emitValidateJSON prints findings as JSON to stdout. Returns non-nil error
// (exit non-zero) when any finding is an ERROR.
func emitValidateJSON(findings []Finding) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	payload := struct {
		Findings []Finding `json:"findings"`
		Errors   int       `json:"errors"`
		Warnings int       `json:"warnings"`
		Infos    int       `json:"infos"`
	}{
		Findings: findings,
		Errors:   countSeverity(findings, SeverityError),
		Warnings: countSeverity(findings, SeverityWarn),
		Infos:    countSeverity(findings, SeverityInfo),
	}
	if err := enc.Encode(payload); err != nil {
		return err
	}
	if payload.Errors > 0 {
		return fmt.Errorf("%d error(s) found", payload.Errors)
	}
	return nil
}

// emitValidateText prints findings as severity-prefixed lines then a tally.
// Returns non-nil error (exit non-zero) when any finding is an ERROR.
func emitValidateText(findings []Finding, _ *config.Config) error {
	for _, f := range findings {
		switch f.Severity {
		case SeverityError:
			warn.Printf("ERROR [%s] %s\n", f.Check, f.Message)
		case SeverityWarn:
			warn.Printf("WARN  [%s] %s\n", f.Check, f.Message)
		default:
			info.Printf("INFO  [%s] %s\n", f.Check, f.Message)
		}
	}

	errors := countSeverity(findings, SeverityError)
	warnings := countSeverity(findings, SeverityWarn)

	fmt.Println()
	if errors == 0 && warnings == 0 {
		success.Println("All checks passed.")
		return nil
	}

	summary := fmt.Sprintf("%s, %s", pluralize(errors, "error"), pluralize(warnings, "warning"))
	if errors > 0 {
		warn.Println(summary)
		return fmt.Errorf("%s — run 'leo validate' after fixing to verify", pluralize(errors, "error"))
	}

	info.Println(summary)
	return nil
}

func countSeverity(findings []Finding, sev Severity) int {
	n := 0
	for _, f := range findings {
		if f.Severity == sev {
			n++
		}
	}
	return n
}

func pluralize(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
