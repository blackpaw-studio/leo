package claude

import (
	"context"

	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

// injectPromptFn is the seam driver tests replace; production uses
// tmux.InjectPrompt (readiness-probed paste + Enter).
var injectPromptFn = tmux.InjectPrompt

// SetInjectPromptForTest swaps the InjectPrompt seam and returns a restore
// func. Exported for _test files in this package only by convention.
func SetInjectPromptForTest(fn func(ctx context.Context, tmuxPath, session, body string) error) func() {
	prev := injectPromptFn
	injectPromptFn = fn
	return func() { injectPromptFn = prev }
}

// TmuxTUIDriver drives the interactive Claude Code TUI supervised in a leo
// tmux session. The supervisor owns the restart loop; this driver owns the
// claude-specific pane care and message delivery.
type TmuxTUIDriver struct{}

func (TmuxTUIDriver) Style() harness.DriveStyle { return harness.DriveTmux }

// Start is a no-op: the supervisor's tmux new-session already launched the
// TUI, and claude needs no post-launch arrangement beyond the pane care
// hooks the supervisor polls.
func (TmuxTUIDriver) Start(context.Context, harness.SessionHandle) error { return nil }

// Inject pastes msg into the live TUI. Delivery is asynchronous — the turn
// outcome lives in the pane / arrives via the Stop hook — so the Result is
// always nil.
func (TmuxTUIDriver) Inject(ctx context.Context, h harness.SessionHandle, msg string) (*harness.Result, error) {
	tmuxPath, err := tmux.Locate()
	if err != nil {
		return nil, err
	}
	return nil, injectPromptFn(ctx, tmuxPath, h.TmuxSession, msg)
}

// Attach returns the plain tmux attach argv. The CLI's claude path keeps its
// richer behavior (display-popup nesting, -CC control mode) — this spec is
// the harness-neutral fallback shape.
func (TmuxTUIDriver) Attach(h harness.SessionHandle) (harness.AttachSpec, error) {
	tmuxPath, err := tmux.Locate()
	if err != nil {
		return harness.AttachSpec{}, err
	}
	argv := append([]string{tmuxPath}, tmux.Args("attach", "-t", tmux.Target(h.TmuxSession))...)
	return harness.AttachSpec{Argv: argv}, nil
}
