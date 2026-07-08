package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/hooks"
	"github.com/blackpaw-studio/leo/internal/session"
)

// SessionSpec is the runtime descriptor for one supervised persistent claude
// session. Materialized at daemon start from config.Sessions plus any
// implicit sessions derived from runtime: persistent tasks without `session:`.
type SessionSpec struct {
	Name            string
	Workdir         string
	Model           string
	Agent           string
	PermissionMode  string
	AllowedTools    []string
	DisallowedTools []string
	AppendPrompt    string
	AddDirs         []string
	Channels        []string
	Env             map[string]string
	ResumeID        string
}

// SessionTmuxName is the tmux session name for a persistent session
// (Topology A/B). The single source of truth for this convention; the daemon's
// prompt injector resolves logical session names to this via the runner.
func SessionTmuxName(name string) string { return "leo-session-" + name }

// buildSessionClaudeArgs assembles the claude CLI args for a persistent
// session. Mirrors buildProcessArgs but for SessionSpec.
func buildSessionClaudeArgs(spec SessionSpec) []string {
	var a []string
	if spec.Model != "" {
		a = append(a, "--model", spec.Model)
	}
	if spec.ResumeID != "" {
		a = append(a, "--resume", spec.ResumeID)
	}
	if spec.PermissionMode != "" {
		a = append(a, "--permission-mode", spec.PermissionMode)
	}
	for _, ch := range spec.Channels {
		a = append(a, "--channels", ch)
	}
	if spec.Agent != "" {
		a = append(a, "--agent", spec.Agent)
	}
	a = append(a, "--add-dir", spec.Workdir)
	for _, d := range spec.AddDirs {
		a = append(a, "--add-dir", d)
	}
	if len(spec.AllowedTools) > 0 {
		a = append(a, "--allowed-tools", strings.Join(spec.AllowedTools, ","))
	}
	if len(spec.DisallowedTools) > 0 {
		a = append(a, "--disallowed-tools", strings.Join(spec.DisallowedTools, ","))
	}
	if spec.AppendPrompt != "" {
		a = append(a, "--append-system-prompt", spec.AppendPrompt)
	}
	return a
}

// SuperviseSession launches the restart-loop for one session in its own
// goroutine. Caller is responsible for ctx lifecycle.
func SuperviseSession(ctx context.Context, tmuxPath, claudePath string, spec SessionSpec, homePath string, onSessionEnd func(int)) error {
	if err := hooks.EnsureLeoStopHook(spec.Workdir); err != nil {
		return fmt.Errorf("ensure stop hook: %w", err)
	}
	// buildShell assembles the env exports + claude command. resume=false drops
	// --resume for poisoned-session recovery. LEO_HOME tells the in-session
	// Stop hook which daemon to report completion to.
	buildShell := func(resume bool) string {
		s := spec
		if !resume {
			s.ResumeID = ""
		}
		shellCmd := shellQuote(claudePath)
		for _, a := range buildSessionClaudeArgs(s) {
			shellCmd += " " + shellQuote(a)
		}
		envExports := fmt.Sprintf("export LEO_SESSION_NAME=%s; export LEO_CHANNELS=%s; export LEO_HOME=%s;",
			shellQuote(spec.Name), shellQuote(strings.Join(spec.Channels, ",")), shellQuote(homePath))
		for k, v := range spec.Env {
			envExports += fmt.Sprintf(" export %s=%s;", k, shellQuote(v))
		}
		return envExports + " exec " + shellCmd
	}
	loop := LoopSpec{
		Name:        spec.Name,
		SessionName: SessionTmuxName(spec.Name),
		Workdir:     spec.Workdir,
		ShellCmd:    buildShell,
		OnQuickExit: func() {
			// Stale --resume: clear the stored id so the next boot starts fresh.
			_ = session.NewStore(homePath).Delete("session:" + spec.Name)
		},
		OnSessionEnd: onSessionEnd,
	}
	go runSuperviseLoop(ctx, tmuxPath, loop)
	return nil
}

// SessionSpecsFromConfig builds the SessionSpec list from a config. Includes:
// - all entries in cfg.Sessions (Topology B)
// - implicit sessions from runtime: persistent tasks without a `session:` field (Topology A)
// Excludes `session: process:<name>` tasks (Topology C — supervised by process loop).
func SessionSpecsFromConfig(cfg *config.Config) ([]SessionSpec, error) {
	out := []SessionSpec{}
	// workspaceOr falls back to the default workspace so a session never
	// boots tmux with an empty `-c` (which would land in an unintended dir).
	workspaceOr := func(ws string) string {
		if ws != "" {
			return ws
		}
		return cfg.DefaultWorkspace()
	}
	// explicit sessions
	for name, sc := range cfg.Sessions {
		out = append(out, SessionSpec{
			Name:            name,
			Workdir:         workspaceOr(sc.Workspace),
			Model:           cfg.SessionModel(sc),
			Agent:           sc.Agent,
			PermissionMode:  sc.PermissionMode,
			AllowedTools:    sc.AllowedTools,
			DisallowedTools: sc.DisallowedTools,
			AppendPrompt:    sc.AppendSystemPrompt,
			AddDirs:         sc.AddDirs,
			Channels:        sc.Channels,
			Env:             sc.Env,
		})
	}
	// implicit sessions from persistent tasks without `session:`
	seen := map[string]bool{}
	for _, s := range out {
		seen[s.Name] = true
	}
	for name, task := range cfg.Tasks {
		if task.Runtime != "persistent" {
			continue
		}
		if task.Session != "" {
			// shared (B) or process: (C) — handled elsewhere
			continue
		}
		if seen[name] {
			return nil, fmt.Errorf("session name conflict: implicit %q collides with sessions.%s", name, name)
		}
		model := task.Model
		if model == "" {
			model = cfg.ProviderDefaultModel(cfg.TaskProvider(task))
		}
		out = append(out, SessionSpec{
			Name:            name,
			Workdir:         workspaceOr(task.Workspace),
			Model:           model,
			PermissionMode:  task.PermissionMode,
			AllowedTools:    task.AllowedTools,
			DisallowedTools: task.DisallowedTools,
			AppendPrompt:    task.AppendSystemPrompt,
			Channels:        task.Channels,
			// Note: TaskConfig has no Agent or Env fields; those stay zero.
		})
	}
	return out, nil
}
