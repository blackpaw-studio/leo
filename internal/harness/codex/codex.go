// Package codex adapts leo's harness-neutral LaunchSpec to the OpenAI Codex
// CLI. Scheduled tasks run one-shot (codex exec); supervised processes,
// ephemeral agents, and persistent sessions all drive turn-per-invocation via
// TurnDriver (no resident process). Headless exec always runs with approval
// policy "never" (upstream removed the flag), so the only permission knob is
// the sandbox.
package codex

import (
	"fmt"
	"strings"

	"github.com/blackpaw-studio/leo/internal/harness"
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

// SupportsKind: scheduled tasks, supervised processes, ephemeral agents, and
// persistent sessions — all driven turn-per-invocation via TurnDriver.
func (Codex) SupportsKind(k harness.Kind) bool {
	return k == harness.KindTask || k == harness.KindProcess || k == harness.KindAgent || k == harness.KindSession
}

// Driver: TurnDriver drives processes, ephemeral agents, and persistent
// sessions turn-per-invocation (no resident process).
func (Codex) Driver() harness.SessionDriver { return TurnDriver{} }

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
	args = append(args, opts.LeoMCP.configArgs()...)

	if spec.Kind == harness.KindProcess || spec.Kind == harness.KindAgent || spec.Kind == harness.KindSession {
		// Turn prefix only: TurnDriver appends ["resume", id] and the
		// per-message prompt on each Inject/Start.
		return args, nil
	}

	args = append(args, c.SessionArgs(spec.Session)...)
	return append(args, spec.Prompt), nil
}
