// Package opencode adapts leo's harness-neutral LaunchSpec to the opencode
// CLI. Scheduled tasks run one-shot (opencode run); supervised processes,
// ephemeral agents, and persistent sessions all drive the interactive
// opencode TUI supervised in a leo tmux session (parity with claude/codex),
// via the shared tmuxtui.Driver. Permissions are config-only upstream, so
// they ride in via the OPENCODE_CONFIG_CONTENT env overlay rather than argv.
package opencode

import (
	"fmt"
	"strings"

	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/harness/tmuxtui"
	"github.com/blackpaw-studio/leo/internal/tmux"
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

// SupportsKind: scheduled tasks plus ephemeral agents — all driven against
// the interactive opencode TUI supervised in tmux.
func (Opencode) SupportsKind(k harness.Kind) bool {
	return k == harness.KindTask || k == harness.KindAgent
}

// Driver: the shared tmuxtui driver, wired with opencode's readiness-probe
// marker, resume-argv refresher, quick-exit recovery, and post-hoc
// session-id discovery (no session exists at TUI launch — one is created on
// the first turn).
func (Opencode) Driver() harness.SessionDriver {
	return tmuxtui.New(tmuxtui.Config{
		Probe:         tmux.Profile{Marker: "┃", Classify: tmux.ProbeClassifier("┃")},
		RecoverFn:     recoverQuickExitArgs,
		RefreshArgsFn: refreshSessionArgs,
		DiscoverIDFn:  discoverSessionID,
	})
}

func (Opencode) SessionArgs(s harness.SessionState) []string {
	if s.Mode == harness.SessionResume {
		return []string{"-s", s.ID}
	}
	return nil
}

func (o Opencode) Args(spec harness.LaunchSpec) ([]string, error) {
	if _, ok := spec.Options.(Options); !ok {
		return nil, fmt.Errorf("opencode: spec.Options is %T, want opencode.Options", spec.Options)
	}
	if len(spec.Channels) > 0 || len(spec.DevChannels) > 0 {
		return nil, fmt.Errorf("opencode: channel plugins are not supported; use leo's MCP tools for messaging")
	}

	if spec.Kind == harness.KindAgent {
		// Interactive TUI argv; workspace rides in as tmux new-session's -c
		// cwd. Resume (-s) is added by RefreshSessionArgs once a session id
		// is discovered; the opening prompt is injected by the driver's
		// Start. Permissions and the leo MCP bridge ride in via the
		// OPENCODE_CONFIG_CONTENT env overlay (Env), unchanged.
		var args []string
		if spec.Model != "" {
			args = append(args, "--model", spec.Model)
		}
		return args, nil
	}

	if spec.Kind != harness.KindTask {
		// Defensive only: harness.Kind has exactly four values and the three
		// session-driver kinds are all handled above, so this can't fire for
		// any kind that exists today.
		return nil, fmt.Errorf("opencode: %s launches are not supported", spec.Kind)
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
