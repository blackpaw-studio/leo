// Package opencode adapts leo's harness-neutral LaunchSpec to the opencode
// CLI. One-shot tasks only (opencode run); the server-based session driver
// lands in a later plan. Permissions are config-only upstream, so they ride
// in via the OPENCODE_CONFIG_CONTENT env overlay rather than argv.
package opencode

import (
	"fmt"
	"strings"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// Opencode is the opencode adapter.
type Opencode struct{}

func init() { harness.Register(Opencode{}) }

func (Opencode) Name() string   { return "opencode" }
func (Opencode) Binary() string { return "opencode" }

// ValidateModel enforces opencode's required provider/model shape.
func (Opencode) ValidateModel(model string) error {
	if model == "" {
		return nil
	}
	provider, name, ok := strings.Cut(model, "/")
	if ok && provider != "" && name != "" {
		return nil
	}
	return fmt.Errorf("%q is not valid (must be provider/model, e.g. anthropic/claude-sonnet-4-5)", model)
}

func (Opencode) SupportsChannels() bool { return false }

// SupportsKind: one-shot tasks only until the ServerDriver lands (Plan 4).
func (Opencode) SupportsKind(k harness.Kind) bool { return k == harness.KindTask }

func (Opencode) SessionArgs(s harness.SessionState) []string {
	if s.Mode == harness.SessionResume {
		return []string{"-s", s.ID}
	}
	return nil
}

func (o Opencode) Args(spec harness.LaunchSpec) ([]string, error) {
	if spec.Kind != harness.KindTask {
		return nil, fmt.Errorf("opencode: %s launches are not supported yet (only scheduled tasks) — session drivers land in a later plan", spec.Kind)
	}
	if _, ok := spec.Options.(Options); !ok {
		return nil, fmt.Errorf("opencode: spec.Options is %T, want opencode.Options", spec.Options)
	}
	if len(spec.Channels) > 0 || len(spec.DevChannels) > 0 {
		return nil, fmt.Errorf("opencode: channel plugins are not supported; use leo's MCP tools for messaging")
	}
	if spec.Session.Mode == harness.SessionPinned {
		return nil, fmt.Errorf("opencode: cannot start a session with a pre-issued ID")
	}

	args := []string{"run", "--format", "json"}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	args = append(args, o.SessionArgs(spec.Session)...)
	return append(args, spec.Prompt), nil
}
