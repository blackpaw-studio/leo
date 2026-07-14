package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/harness"
	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
	codexharness "github.com/blackpaw-studio/leo/internal/harness/codex"
	opencodeharness "github.com/blackpaw-studio/leo/internal/harness/opencode"
	"github.com/blackpaw-studio/leo/internal/hooks"
	"github.com/blackpaw-studio/leo/internal/leomcp"
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

// sessionLeoMCPEnv builds the env a non-claude session's LeoMCP bridge needs,
// mirroring cli.processLeoMCPEnv. The leo MCP server is always wired in now —
// the bridge is built regardless of whether web is enabled or a token is
// available — and self-selects local-only mode at runtime when
// LEO_WEB_PORT/LEO_API_TOKEN are empty. The bool return reports whether a
// live token was available (cfg.Web.Enabled && webToken != ""), for callers
// that still want to know; it no longer gates injection. Sessions have no
// equivalent of a config-defined process name, so the value exported under
// LEO_PROCESS_NAME — the only env var internal/mcp/server.go actually reads
// — is "session:"+name, keeping it distinct from every config-defined
// process name in the same daemon.
func sessionLeoMCPEnv(cfg *config.Config, sessionName, webToken string) (map[string]string, bool) {
	webPort := 0
	if cfg != nil {
		webPort = cfg.WebPort()
	}
	return map[string]string{
		"LEO_PROCESS_NAME": "session:" + sessionName,
		"LEO_WEB_PORT":     strconv.Itoa(webPort),
		"LEO_API_TOKEN":    webToken,
	}, cfg != nil && cfg.Web.Enabled && webToken != ""
}

// SuperviseSession launches the restart-loop for one session in its own
// goroutine, dispatching on spec.Harness. Caller is responsible for ctx
// lifecycle. cfg/webToken gate the non-claude LeoMCP bridge (see
// sessionLeoMCPEnv) — claude never consults them (its own bridge, if any,
// rides in through a different path today).
//
//   - claude (or ""): byte-identical to the original tmux-hosted claude
//     restart loop below (Stop hook, --resume recovery).
//   - codex, opencode, and every other registered harness: a resident TUI
//     supervised in a leo tmux session via superviseTUISession, driven
//     entirely through the harness's SessionDriver and its optional
//     capability interfaces (PreLauncher, SessionArgsRefresher) — no
//     per-harness branching beyond the LeoMCP bridge shape.
func SuperviseSession(ctx context.Context, tmuxPath, claudePath string, spec SessionSpec, homePath string, cfg *config.Config, webToken string, onSessionEnd func(int)) error {
	switch spec.Harness {
	case "", "claude":
		return superviseClaudeSession(ctx, tmuxPath, claudePath, spec, homePath, onSessionEnd)
	default:
		return superviseTUISession(ctx, tmuxPath, spec, homePath, cfg, webToken, onSessionEnd)
	}
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

// superviseTUISession launches the restart-loop for a persistent session on
// any TUI-driven harness (codex, opencode, and any future adapter) — a
// resident TUI supervised in a leo tmux session, structurally parallel to
// superviseClaudeSession but generic over the harness via SessionDriver and
// its optional PreLauncher/SessionArgsRefresher capabilities. The only
// per-harness branching left is the LeoMCP bridge shape: codex renders it
// into argv via `-c mcp_servers.leo.*` overrides plus a parent-env
// whitelist (the bridge's EnvVars field), so the literal LEO_* values must
// also be exported into the spawned shell's env for codex to forward them;
// opencode embeds the bridge directly into an OPENCODE_CONFIG_CONTENT env
// overlay (Opencode.Env), so no extra literal-value export is needed beyond
// what h.Env already returns. Everything else — argv, env, resume-argv
// rewriting, pre-launch hooks, dialog dismissal, quick-exit recovery — routes
// through h.Args/h.Env/drv capability assertions, with no per-harness-name
// branch beyond the bridge wiring above.
func superviseTUISession(ctx context.Context, tmuxPath string, spec SessionSpec, homePath string, cfg *config.Config, webToken string, onSessionEnd func(int)) error {
	loop, err := buildTUISessionLoop(spec, homePath, cfg, webToken, onSessionEnd)
	if err != nil {
		return err
	}
	go runSuperviseLoop(ctx, tmuxPath, loop)
	return nil
}

// buildTUISessionLoop resolves everything superviseTUISession needs to
// supervise a resident TUI for spec into a LoopSpec, without spawning the
// restart-loop goroutine — split out so it (and, by extension, the argv/env
// it produces) can be unit-tested by driving runSuperviseLoop directly under
// the caller's own goroutine, the same way session-loop tests already drive
// it for claude.
func buildTUISessionLoop(spec SessionSpec, homePath string, cfg *config.Config, webToken string, onSessionEnd func(int)) (LoopSpec, error) {
	h, err := harness.Get(spec.Harness)
	if err != nil {
		return LoopSpec{}, fmt.Errorf("session %q: %w", spec.Name, err)
	}

	decoded, err := h.DecodeOptions(spec.HarnessOptions)
	if err != nil {
		return LoopSpec{}, fmt.Errorf("session %q: decoding %s harness options: %w", spec.Name, spec.Harness, err)
	}

	// leoEnv carries literal LEO_* values that must be exported into the
	// spawned shell's env for a harness whose LeoMCP bridge only whitelists
	// parent-env var NAMES (codex). It stays empty for harnesses (opencode)
	// whose bridge embeds the values directly, since h.Env already carries
	// those through hEnv below.
	leoEnv := map[string]string{}
	switch opts := decoded.(type) {
	case codexharness.Options:
		env, _ := sessionLeoMCPEnv(cfg, spec.Name, webToken)
		opts.LeoMCP = &codexharness.LeoMCPBridge{
			Command:      "leo",
			Args:         []string{"mcp-server"},
			EnvVars:      []string{"LEO_PROCESS_NAME", "LEO_WEB_PORT", "LEO_API_TOKEN"},
			ApprovalMode: "approve",
		}
		leoEnv = env
		decoded = opts
	case opencodeharness.Options:
		env, _ := sessionLeoMCPEnv(cfg, spec.Name, webToken)
		opts.LeoMCP = &opencodeharness.LeoMCPBridge{
			Command: []string{"leo", "mcp-server"},
			Env:     env,
		}
		decoded = opts
	}

	ls := harness.LaunchSpec{
		Kind:          harness.KindSession,
		Name:          spec.Name,
		Model:         spec.Model,
		Workspace:     spec.Workdir,
		SystemContext: leomcp.LeoNudge(cfg),
		Options:       decoded,
	}
	baseArgs, err := h.Args(ls)
	if err != nil {
		return LoopSpec{}, fmt.Errorf("session %q: building %s args: %w", spec.Name, spec.Harness, err)
	}
	hEnv, err := h.Env(ls)
	if err != nil {
		return LoopSpec{}, fmt.Errorf("session %q: building %s env: %w", spec.Name, spec.Harness, err)
	}

	// binPath resolves like the process supervisor's harnessBinaryPath:
	// absolute via LookPath when the daemon's PATH can find it, bare
	// otherwise so the pane's exported PATH still gets a chance.
	binPath, lookErr := exec.LookPath(h.Binary())
	if lookErr != nil {
		binPath = h.Binary()
	}

	store := session.NewStore(homePath)
	drv := h.Driver()
	// buildShell rewrites the launch argv from the currently stored session
	// id on every (re)spawn — resume=false (poisoned-session recovery) forces
	// a fresh launch by passing storedID="" regardless of what's on disk.
	buildShell := func(resume bool) string {
		args := baseArgs
		if rf, ok := drv.(harness.SessionArgsRefresher); ok {
			storedID := ""
			if resume {
				storedID, _, _ = store.Get("session:" + spec.Name)
			}
			args = rf.RefreshSessionArgs(baseArgs, storedID)
		}
		shellCmd := shellQuote(binPath)
		for _, a := range args {
			shellCmd += " " + shellQuote(a)
		}
		envExports := fmt.Sprintf("export LEO_SESSION_NAME=%s; export LEO_HOME=%s;",
			shellQuote(spec.Name), shellQuote(homePath))
		for k, v := range hEnv {
			envExports += fmt.Sprintf(" export %s=%s;", k, shellQuote(v))
		}
		for k, v := range leoEnv {
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
		PreLaunch: func() error {
			if pl, ok := drv.(harness.PreLauncher); ok {
				return pl.PreLaunch(harness.SessionHandle{
					Kind:        harness.KindSession,
					Name:        spec.Name,
					TmuxSession: SessionTmuxName(spec.Name),
					Workspace:   spec.Workdir,
					HomePath:    homePath,
				})
			}
			return nil
		},
		OnQuickExit:  func() { _ = session.NewStore(homePath).Delete("session:" + spec.Name) },
		OnSessionEnd: onSessionEnd,
	}
	return loop, nil
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
// Every non-claude harness (codex, opencode, and any future adapter) drives
// a resident tmux-TUI: Inject pastes into the session's live tmux pane using
// only the handle's routing coordinates (TmuxSession/Workspace/HomePath/IDs)
// — no turn-prefix argv needed, since the resident TUI itself (including its
// own LeoMCP wiring) is provisioned separately by superviseTUISession. This table is
// therefore purely "route Inject/Abort to the harness driver with these
// coordinates"; it does not decode harness_options or build argv. cfg/
// webToken are unused now that no branch here wires a LeoMCP bridge, but the
// signature is kept stable for the call site in defaultSupervisedExec and any
// future non-tmux driver whose Inject would need the same gate.
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
		out[tmuxName] = SessionDispatch{Harness: sp.Harness, Handle: handle}
	}
	return out
}
