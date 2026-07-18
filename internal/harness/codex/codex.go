// Package codex adapts leo's harness-neutral LaunchSpec to the OpenAI Codex
// CLI. Scheduled tasks run one-shot (codex exec --json); supervised
// processes, ephemeral agents, and persistent sessions all drive the
// interactive codex TUI supervised in a leo tmux session (parity with
// claude), via the shared tmuxtui.Driver. Both launch shapes always run with
// approval policy "never" (headless exec has no other option; the TUI is
// pinned to it for unattended parity), so the only permission knob is the
// sandbox.
package codex

import (
	"fmt"
	"strings"

	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/harness/tmuxtui"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

// Codex is the OpenAI Codex adapter.
type Codex struct{}

func init() { harness.Register(Codex{}) }

func (Codex) Name() string   { return "codex" }
func (Codex) Binary() string { return "codex" }

// ValidateModel is a format check only: codex model names are validated
// server-side (invalid ones fail the run with a model_not_found error).
func (Codex) ValidateModel(model string) error {
	if model == "" || !strings.ContainsAny(model, " \t") {
		return nil
	}
	return fmt.Errorf("%q is not valid (must not contain whitespace)", model)
}

func (Codex) SupportsChannels() bool { return false }

// SupportsKind: scheduled tasks run one-shot; ephemeral agents and
// persistent sessions all drive the codex TUI in tmux.
func (Codex) SupportsKind(k harness.Kind) bool {
	return k == harness.KindTask || k == harness.KindAgent
}

// Driver: the shared tmuxtui driver, wired with codex's readiness-probe
// marker, trust pre-launch hook, resume-argv refresher, and quick-exit
// recovery.
func (Codex) Driver() harness.SessionDriver {
	return tmuxtui.New(tmuxtui.Config{
		Probe:         tmux.Profile{Marker: "› ", Classify: tmux.ProbeClassifier("› ")},
		RecoverFn:     recoverQuickExitArgs,
		PreLaunchFn:   ensureWorkspaceTrusted,
		RefreshArgsFn: refreshSessionArgs,
		DiscoverIDFn:  discoverSessionID,
	})
}

// Env: codex needs no adapter-injected env. Auth (CODEX_API_KEY or ambient
// login state) is the caller's/user's concern.
func (Codex) Env(harness.LaunchSpec) (map[string]string, error) { return nil, nil }

// SessionArgs renders the resume subcommand tokens. Args() places them
// between the flags and the positional prompt; codex has no flag-style
// session selection and cannot pin a fresh session ID.
func (Codex) SessionArgs(s harness.SessionState) []string {
	if s.Mode == harness.SessionResume {
		return []string{"resume", s.ID}
	}
	return nil
}

func (c Codex) Args(spec harness.LaunchSpec) ([]string, error) {
	opts, ok := spec.Options.(Options)
	if !ok {
		return nil, fmt.Errorf("codex: spec.Options is %T, want codex.Options", spec.Options)
	}
	if len(spec.Channels) > 0 || len(spec.DevChannels) > 0 {
		return nil, fmt.Errorf("codex: channel plugins are not supported; use leo's MCP tools for messaging")
	}
	if spec.Session.Mode == harness.SessionPinned {
		return nil, fmt.Errorf("codex: cannot start a session with a pre-issued ID")
	}

	if spec.Kind == harness.KindTask {
		// --skip-git-repo-check always: leo workspaces are leo-managed
		// directories, frequently not git repos, and codex refuses to run in
		// them otherwise.
		args := []string{"exec", "--json", "--skip-git-repo-check"}
		if spec.Model != "" {
			args = append(args, "--model", spec.Model)
		}
		if opts.Sandbox != "" {
			args = append(args, "--sandbox", opts.Sandbox)
		}
		args = append(args, developerInstructionsArgs(spec.SystemContext)...)
		args = append(args, sandboxWritableRootsArgs(spec.Workspace)...)
		args = append(args, opts.LeoMCP.configArgs()...)
		args = append(args, c.SessionArgs(spec.Session)...)
		return append(args, spec.Prompt), nil
	}

	// Interactive TUI argv. -a never keeps an unattended TUI from ever
	// blocking on an approval prompt (parity with headless exec, which
	// always ran approval policy "never"). Resume tokens are added by
	// the supervisor via RefreshSessionArgs once a session id is
	// discovered; the opening prompt is injected by the driver's Start.
	args := []string{"-a", "never"}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if opts.Sandbox != "" {
		args = append(args, "--sandbox", opts.Sandbox)
	}
	args = append(args, developerInstructionsArgs(spec.SystemContext)...)
	args = append(args, sandboxWritableRootsArgs(spec.Workspace)...)
	return append(args, opts.LeoMCP.configArgs()...), nil
}

// developerInstructionsArgs renders Leo's harness-neutral system-context
// addendum via codex's additive `developer_instructions` config override
// (supplements, rather than replaces, codex's built-in instructions — unlike
// model_instructions_file, which replaces them). Empty spec.SystemContext
// omits the flag entirely.
func developerInstructionsArgs(systemContext string) []string {
	if systemContext == "" {
		return nil
	}
	return []string{"-c", "developer_instructions=" + tomlString(systemContext)}
}
