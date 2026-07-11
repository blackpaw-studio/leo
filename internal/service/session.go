package service

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/harness"
	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
	codexharness "github.com/blackpaw-studio/leo/internal/harness/codex"
	opencodeharness "github.com/blackpaw-studio/leo/internal/harness/opencode"
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
	// Harness is the resolved harness adapter name ("claude", "codex", or
	// "opencode"). SessionSpecsFromConfig always resolves this via
	// cfg.SessionHarness/cfg.TaskHarness (which default to "claude"), so it
	// is never empty for a spec built there — SuperviseSession still treats
	// "" as claude defensively for any other constructor.
	Harness string
	// HarnessOptions is the RAW (undecoded) harness_options map for
	// non-claude sessions. Claude sessions decode straight into the
	// claude-specific fields above at SessionSpecsFromConfig time; non-claude
	// harness_options are decoded later, at injector-wiring time, via the
	// adapter's own DecodeOptions — this field just carries the raw map that
	// far.
	HarnessOptions map[string]any
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

// sessionLeoMCPEnv gates whether a non-claude session's LeoMCP bridge should
// be wired in, mirroring cli.processLeoMCPEnv's web+token gate exactly
// (cfg.Web.Enabled && webToken != ""). Sessions have no equivalent of a
// config-defined process name, so the value exported under LEO_PROCESS_NAME
// — the only env var internal/mcp/server.go actually reads — is
// "session:"+name, keeping it distinct from every config-defined process
// name in the same daemon.
func sessionLeoMCPEnv(cfg *config.Config, sessionName, webToken string) (map[string]string, bool) {
	if cfg == nil || !cfg.Web.Enabled || webToken == "" {
		return nil, false
	}
	return map[string]string{
		"LEO_PROCESS_NAME": "session:" + sessionName,
		"LEO_WEB_PORT":     strconv.Itoa(cfg.WebPort()),
		"LEO_API_TOKEN":    webToken,
	}, true
}

// SuperviseSession launches the restart-loop for one session in its own
// goroutine, dispatching on spec.Harness. Caller is responsible for ctx
// lifecycle. cfg/webToken gate the non-claude LeoMCP bridge (see
// sessionLeoMCPEnv) — claude never consults them (its own bridge, if any,
// rides in through a different path today).
//
//   - claude (or ""): byte-identical to the original tmux-hosted claude
//     restart loop below (Stop hook, --resume recovery).
//   - codex: TurnDriver has no resident process — turns spawn per message via
//     the daemon's harness-aware injector (wired from the same []SessionSpec
//     in service.defaultSupervisedExec). Nothing to supervise here.
//   - opencode: a resident `opencode serve` restart loop, structurally
//     parallel to the claude one but without the Stop hook (opencode has no
//     equivalent) and with QuickExitNone semantics — a serve crash is not a
//     poisoned conversation, so the stored session id and args survive it.
func SuperviseSession(ctx context.Context, tmuxPath, claudePath string, spec SessionSpec, homePath string, cfg *config.Config, webToken string, onSessionEnd func(int)) error {
	switch spec.Harness {
	case "", "claude":
		return superviseClaudeSession(ctx, tmuxPath, claudePath, spec, homePath, onSessionEnd)
	case "codex":
		return nil
	case "opencode":
		return superviseOpencodeSession(ctx, tmuxPath, spec, homePath, cfg, webToken, onSessionEnd)
	default:
		return fmt.Errorf("session %q: unsupported harness %q", spec.Name, spec.Harness)
	}
}

// buildOpencodeSessionLaunch resolves everything superviseOpencodeSession
// needs to launch a resident `opencode serve` for spec: harness_options are
// decoded here (spec.HarnessOptions is the RAW map — see SessionSpec's doc
// comment) so a config-declared `permission` map actually reaches the
// server, matching what SessionSpecsFromConfig does for claude's own fields
// at spec-build time. The LeoMCP bridge is wired in exactly like
// cli.resolveProcessLaunch's opencode branch, gated by sessionLeoMCPEnv.
// Split out from superviseOpencodeSession so it can be unit tested without
// spawning the restart-loop goroutine.
func buildOpencodeSessionLaunch(spec SessionSpec, homePath string, cfg *config.Config, webToken string) (args []string, env map[string]string, err error) {
	tmuxName := SessionTmuxName(spec.Name)
	state, err := opencodeharness.EnsureServerState(homePath, tmuxName, spec.Model)
	if err != nil {
		return nil, nil, fmt.Errorf("session %q: provisioning opencode server state: %w", spec.Name, err)
	}
	decoded, err := opencodeharness.Opencode{}.DecodeOptions(spec.HarnessOptions)
	if err != nil {
		return nil, nil, fmt.Errorf("session %q: decoding opencode harness options: %w", spec.Name, err)
	}
	opts, ok := decoded.(opencodeharness.Options)
	if !ok {
		return nil, nil, fmt.Errorf("session %q: opencode DecodeOptions returned %T, want opencodeharness.Options", spec.Name, decoded)
	}
	opts.ServerPort = state.Port
	opts.ServerPassword = state.Password
	if leoEnv, ok := sessionLeoMCPEnv(cfg, spec.Name, webToken); ok {
		opts.LeoMCP = &opencodeharness.LeoMCPBridge{
			Command: []string{"leo", "mcp-server"},
			Env:     leoEnv,
		}
	}
	launchSpec := harness.LaunchSpec{
		Kind:      harness.KindSession,
		Name:      spec.Name,
		Model:     spec.Model,
		Workspace: spec.Workdir,
		Options:   opts,
	}
	args, err = opencodeharness.Opencode{}.Args(launchSpec)
	if err != nil {
		return nil, nil, fmt.Errorf("session %q: building opencode serve args: %w", spec.Name, err)
	}
	env, err = opencodeharness.Opencode{}.Env(launchSpec)
	if err != nil {
		return nil, nil, fmt.Errorf("session %q: building opencode env: %w", spec.Name, err)
	}
	return args, env, nil
}

// superviseOpencodeSession launches the restart-loop for a resident
// `opencode serve` backing one persistent session. Port/password are
// provisioned once via EnsureServerState (keyed by the session's tmux name,
// stable across restarts) and rendered into the serve argv + env exports the
// same way the process/agent spawn builders do (see cli.resolveProcessLaunch)
// — buildOpencodeSessionLaunch IS that builder for the session case.
func superviseOpencodeSession(ctx context.Context, tmuxPath string, spec SessionSpec, homePath string, cfg *config.Config, webToken string, onSessionEnd func(int)) error {
	args, env, err := buildOpencodeSessionLaunch(spec, homePath, cfg, webToken)
	if err != nil {
		return err
	}

	// buildShell ignores resume (opencode serve has no --resume equivalent;
	// the same argv is exec'd on every (re)spawn) but keeps the LoopSpec's
	// func(bool) string shape.
	buildShell := func(bool) string {
		shellCmd := shellQuote(opencodeharness.Opencode{}.Binary())
		for _, a := range args {
			shellCmd += " " + shellQuote(a)
		}
		envExports := fmt.Sprintf("export LEO_SESSION_NAME=%s; export LEO_HOME=%s;",
			shellQuote(spec.Name), shellQuote(homePath))
		for k, v := range env {
			envExports += fmt.Sprintf(" export %s=%s;", k, shellQuote(v))
		}
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
		// QuickExitNone semantics: a crashed `opencode serve` is not a
		// poisoned conversation — the stored session id must survive
		// restarts, unlike claude's OnQuickExit below.
		OnQuickExit:  func() {},
		OnSessionEnd: onSessionEnd,
	}
	go runSuperviseLoop(ctx, tmuxPath, loop)
	return nil
}

// superviseClaudeSession is the original tmux-hosted claude restart loop,
// unchanged in behavior from before SuperviseSession gained non-claude
// branches.
func superviseClaudeSession(ctx context.Context, tmuxPath, claudePath string, spec SessionSpec, homePath string, onSessionEnd func(int)) error {
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

// mergeEnvMaps returns a new map with override's entries layered over base.
// Neither input is mutated. Used to merge the LeoMCP bridge's LEO_* env vars
// into a session's own Env map without aliasing either.
func mergeEnvMaps(base, override map[string]string) map[string]string {
	merged := make(map[string]string, len(base)+len(override))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range override {
		merged[k] = v
	}
	return merged
}

// copyEnvMap returns an independent copy of m so callers never alias a
// config map through a SessionSpec. Returns nil for an empty/nil input.
func copyEnvMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
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
		env := copyEnvMap(sc.Env)
		harnessName := cfg.SessionHarness(sc)
		spec := SessionSpec{
			Name:     name,
			Workdir:  workspaceOr(sc.Workspace),
			Model:    cfg.SessionModel(sc),
			AddDirs:  sc.AddDirs,
			Channels: sc.Channels,
			Env:      env,
			Harness:  harnessName,
		}
		if harnessName == "" || harnessName == "claude" {
			o, err := claudeSessionOptions(cfg.SessionHarnessOptions(sc))
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: session %q: decoding harness options: %v (skipping)\n", name, err)
				continue
			}
			spec.Agent = o.AgentFile
			spec.PermissionMode = o.PermissionMode
			spec.AllowedTools = o.AllowedTools
			spec.DisallowedTools = o.DisallowedTools
			spec.AppendPrompt = o.AppendSystemPrompt
		} else {
			// Non-claude: decoding happens later, at injector-wiring time
			// (BuildSessionDispatch), via the adapter's own DecodeOptions —
			// carry the raw map through untouched.
			spec.HarnessOptions = cfg.SessionHarnessOptions(sc)
		}
		out = append(out, spec)
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
		harnessName := cfg.TaskHarness(task)
		spec := SessionSpec{
			Name:     name,
			Workdir:  workspaceOr(task.Workspace),
			Model:    task.Model,
			Channels: task.Channels,
			// Note: TaskConfig has no Agent field; stays zero.
			Env:     copyEnvMap(task.Env),
			Harness: harnessName,
		}
		if harnessName == "" || harnessName == "claude" {
			// Implicit sessions read the task's OWN harness_options without
			// the defaults cascade (preserved quirk): decode the raw map
			// rather than cfg.TaskHarnessOptions, which merges in
			// defaults.harness_options.
			o, err := claudeSessionOptions(task.HarnessOptions)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: implicit session %q: decoding harness options: %v (skipping)\n", name, err)
				continue
			}
			spec.PermissionMode = o.PermissionMode
			spec.AllowedTools = o.AllowedTools
			spec.DisallowedTools = o.DisallowedTools
			spec.AppendPrompt = o.AppendSystemPrompt
		} else {
			// Same no-cascade quirk applies: carry the task's own raw map,
			// not cfg.TaskHarnessOptions.
			spec.HarnessOptions = task.HarnessOptions
		}
		out = append(out, spec)
	}
	return out, nil
}

// SessionDispatch pairs a resolved harness name with the SessionHandle a
// SessionDriver needs to inject into or abort a non-claude persistent
// session. Built once at daemon boot (see BuildSessionDispatch) so the
// daemon's harness-aware injector/aborter never re-resolves config per
// invocation.
type SessionDispatch struct {
	Harness string
	Handle  harness.SessionHandle
}

// BuildSessionDispatch resolves the non-claude entries of sessionSpecs into a
// tmux-session-keyed dispatch table for the daemon's harness-aware
// injector/aborter (wired in service.defaultSupervisedExec). Claude sessions
// are intentionally excluded — the injector's default tmux path handles
// them, and a miss in this map falls through to that path.
//
// codex: harness_options are decoded here (deferred from
// SessionSpecsFromConfig, see SessionSpec.HarnessOptions) and rendered into
// TurnArgs via the adapter's own Args() — TurnDriver.Inject appends the
// per-message prompt on every call. The LeoMCP bridge is wired in exactly
// like cli.resolveProcessLaunch's codex branch, gated by sessionLeoMCPEnv;
// when it fires, the same three LEO_* vars are also merged into the
// handle's Env so TurnDriver's spawned codex process actually has them to
// forward (the bridge's EnvVars field is only a name whitelist — codex
// forwards whatever is already in its own environment under those names). A
// session whose options fail to decode (defensive only; Validate() should
// have already caught this) or whose args fail to build is skipped with a
// warning: better to leave that one session undispatchable than to fail the
// whole daemon boot.
//
// opencode: ServerDriver.Inject resolves the server URL/password itself via
// LoadServerState(h.HomePath, h.TmuxSession) — no TurnArgs needed. The
// resident `opencode serve` itself (including its own LeoMCP wiring) is
// provisioned separately by superviseOpencodeSession; this only needs the
// tmux/home coordinates to build the handle.
func BuildSessionDispatch(sessionSpecs []SessionSpec, homePath string, cfg *config.Config, webToken string) map[string]SessionDispatch {
	out := map[string]SessionDispatch{}
	for _, sp := range sessionSpecs {
		if sp.Harness == "" || sp.Harness == "claude" {
			continue
		}
		tmuxName := SessionTmuxName(sp.Name)
		handle := harness.SessionHandle{
			Kind:        harness.KindSession,
			Name:        sp.Name,
			TmuxSession: tmuxName,
			Workspace:   sp.Workdir,
			HomePath:    homePath,
			Env:         sp.Env,
			IDs:         newStoreIDs(homePath, "session:"+sp.Name),
		}
		switch sp.Harness {
		case "codex":
			decoded, err := codexharness.Codex{}.DecodeOptions(sp.HarnessOptions)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: session %q: decoding codex harness options: %v (injector disabled for this session)\n", sp.Name, err)
				continue
			}
			opts, ok := decoded.(codexharness.Options)
			if !ok {
				fmt.Fprintf(os.Stderr, "warning: session %q: codex DecodeOptions returned %T, want codexharness.Options (injector disabled for this session)\n", sp.Name, decoded)
				continue
			}
			if leoEnv, ok := sessionLeoMCPEnv(cfg, sp.Name, webToken); ok {
				opts.LeoMCP = &codexharness.LeoMCPBridge{
					Command:      "leo",
					Args:         []string{"mcp-server"},
					EnvVars:      []string{"LEO_PROCESS_NAME", "LEO_WEB_PORT", "LEO_API_TOKEN"},
					ApprovalMode: "approve",
				}
				handle.Env = mergeEnvMaps(handle.Env, leoEnv)
			}
			args, err := codexharness.Codex{}.Args(harness.LaunchSpec{
				Kind:      harness.KindSession,
				Name:      sp.Name,
				Model:     sp.Model,
				Workspace: sp.Workdir,
				Options:   opts,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: session %q: building codex args: %v (injector disabled for this session)\n", sp.Name, err)
				continue
			}
			handle.TurnArgs = args
		case "opencode":
			// No TurnArgs needed; see doc comment above.
		default:
			fmt.Fprintf(os.Stderr, "warning: session %q: unsupported harness %q for dispatch (skipping)\n", sp.Name, sp.Harness)
			continue
		}
		out[tmuxName] = SessionDispatch{Harness: sp.Harness, Handle: handle}
	}
	return out
}
