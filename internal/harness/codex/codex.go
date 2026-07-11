// Package codex adapts leo's harness-neutral LaunchSpec to the OpenAI Codex
// CLI. One-shot tasks only (codex exec); session drivers land in a later
// plan. Headless exec always runs with approval policy "never" (upstream
// removed the flag), so the only permission knob is the sandbox.
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

// SupportsKind: one-shot tasks only until the TurnDriver lands (Plan 4).
func (Codex) SupportsKind(k harness.Kind) bool { return k == harness.KindTask }

// Driver: no interactive kinds yet — the TurnDriver/ServerDriver lands in Plan-4 Task 5/6.
func (Codex) Driver() harness.SessionDriver { return nil }

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
	if spec.Kind != harness.KindTask {
		return nil, fmt.Errorf("codex: %s launches are not supported yet (only scheduled tasks) — session drivers land in a later plan", spec.Kind)
	}
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
	args = append(args, c.SessionArgs(spec.Session)...)
	return append(args, spec.Prompt), nil
}
