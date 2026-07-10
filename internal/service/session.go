package service

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/harness"
	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
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
// session via the claude adapter's KindSession handling.
func buildSessionClaudeArgs(spec SessionSpec) []string {
	ls := harness.LaunchSpec{
		Kind:      harness.KindSession,
		Name:      spec.Name,
		Model:     spec.Model,
		Workspace: spec.Workdir,
		AddDirs:   spec.AddDirs,
		Channels:  spec.Channels,
		Options: claudeharness.Options{
			PermissionMode:     spec.PermissionMode,
			AgentFile:          spec.Agent,
			AllowedTools:       spec.AllowedTools,
			DisallowedTools:    spec.DisallowedTools,
			AppendSystemPrompt: spec.AppendPrompt,
		},
	}
	if spec.ResumeID != "" {
		ls.Session = harness.SessionState{Mode: harness.SessionResume, ID: spec.ResumeID}
	}
	args, err := claudeharness.Claude{}.Args(ls)
	if err != nil {
		// Unreachable with a well-formed spec; never launch flagless silently.
		fmt.Fprintf(os.Stderr, "warning: session %q: building claude args: %v\n", spec.Name, err)
		return nil
	}
	return args
}

// claudeSessionOptions decodes a session-scoped harness_options map. A
// config that passed Validate() cannot fail here; on the defensive path we
// warn and skip the session rather than boot claude with dropped flags.
func claudeSessionOptions(opts map[string]any) (claudeharness.Options, error) {
	decoded, err := claudeharness.Claude{}.DecodeOptions(opts)
	if err != nil {
		return claudeharness.Options{}, err
	}
	o, ok := decoded.(claudeharness.Options)
	if !ok {
		return claudeharness.Options{}, fmt.Errorf("unexpected options type %T", decoded)
	}
	return o, nil
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
		env := make(map[string]string, len(sc.Env))
		for k, v := range sc.Env {
			env[k] = v
		}
		o, err := claudeSessionOptions(cfg.SessionHarnessOptions(sc))
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: session %q: decoding harness options: %v (skipping)\n", name, err)
			continue
		}
		out = append(out, SessionSpec{
			Name:            name,
			Workdir:         workspaceOr(sc.Workspace),
			Model:           cfg.SessionModel(sc),
			Agent:           o.AgentFile,
			PermissionMode:  o.PermissionMode,
			AllowedTools:    o.AllowedTools,
			DisallowedTools: o.DisallowedTools,
			AppendPrompt:    o.AppendSystemPrompt,
			AddDirs:         sc.AddDirs,
			Channels:        sc.Channels,
			Env:             env,
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
		// Implicit sessions read the task's OWN harness_options without the
		// defaults cascade (preserved quirk): decode the raw map rather than
		// cfg.TaskHarnessOptions, which merges in defaults.harness_options.
		o, err := claudeSessionOptions(task.HarnessOptions)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: implicit session %q: decoding harness options: %v (skipping)\n", name, err)
			continue
		}
		out = append(out, SessionSpec{
			Name:            name,
			Workdir:         workspaceOr(task.Workspace),
			Model:           task.Model,
			PermissionMode:  o.PermissionMode,
			AllowedTools:    o.AllowedTools,
			DisallowedTools: o.DisallowedTools,
			AppendPrompt:    o.AppendSystemPrompt,
			Channels:        task.Channels,
			// Note: TaskConfig has no Agent field; stays zero.
			Env: nil,
		})
	}
	return out, nil
}
