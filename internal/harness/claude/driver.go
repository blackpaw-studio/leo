package claude

import (
	"context"
	"regexp"
	"strings"

	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

// injectPromptFn is the seam driver tests replace; production uses
// tmux.InjectPrompt (readiness-probed paste + Enter).
var injectPromptFn = tmux.InjectPrompt

// locateTmuxFn is the seam driver tests replace; production uses tmux.Locate.
// Injectable so Inject/Attach can be unit-tested on a machine (or CI runner)
// with no tmux on PATH — otherwise the real Locate errors before delegation.
var locateTmuxFn = tmux.Locate

// SetInjectPromptForTest swaps the InjectPrompt seam and returns a restore
// func. Exported for _test files in this package only by convention.
func SetInjectPromptForTest(fn func(ctx context.Context, tmuxPath, session, body string) error) func() {
	prev := injectPromptFn
	injectPromptFn = fn
	return func() { injectPromptFn = prev }
}

// SetLocateTmuxForTest swaps the tmux.Locate seam and returns a restore func,
// so Inject/Attach tests don't depend on tmux being installed on the runner.
func SetLocateTmuxForTest(fn func() (string, error)) func() {
	prev := locateTmuxFn
	locateTmuxFn = fn
	return func() { locateTmuxFn = prev }
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
	tmuxPath, err := locateTmuxFn()
	if err != nil {
		return nil, err
	}
	return nil, injectPromptFn(ctx, tmuxPath, h.TmuxSession, msg)
}

// Attach returns the plain tmux attach argv. The CLI's claude path keeps its
// richer behavior (display-popup nesting, -CC control mode) — this spec is
// the harness-neutral fallback shape.
func (TmuxTUIDriver) Attach(h harness.SessionHandle) (harness.AttachSpec, error) {
	tmuxPath, err := locateTmuxFn()
	if err != nil {
		return harness.AttachSpec{}, err
	}
	argv := append([]string{tmuxPath}, tmux.Args("attach", "-t", tmux.Target(h.TmuxSession))...)
	return harness.AttachSpec{Argv: argv}, nil
}

// dialogDenyPattern marks dialogs that make a consequential decision — never
// auto-answered, always left for a human. Word-boundaried, case-insensitive.
var dialogDenyPattern = regexp.MustCompile(`(?i)\b(trust|permission|delete|overwrite)\b`)

// PaneKey decides how to clear a blocking claude startup/announcement
// dialog visible in pane. It returns the tmux key to send ("Enter" or "Escape"),
// or "" to leave the pane untouched. Pure (no I/O) so it is unit-tested directly.
//
// This runs on EVERY session poll for the session's whole lifetime, so it must
// never fire on ordinary output. The only reliable discriminator between a
// blocking interactive modal and normal conversational text (which routinely
// contains numbered lists) is the modal's confirm/cancel footer — normal output
// never renders one. A numbered menu alone is NOT sufficient.
//
// Order matters:
//  1. "Resume from summary" is a known prompt we ACCEPT (Enter).
//  2. Otherwise, act only when genuine dialog chrome (both confirm AND cancel
//     footer) is present; anything else is left untouched.
//  3. A modal mentioning a consequential decision (trust/permission/delete/
//     overwrite) is left for a human — never auto-answered.
//  4. Any other modal is an announcement/opt-in we DECLINE with Escape so the
//     agent's behavior stays stable.
func (TmuxTUIDriver) PaneKey(pane string) string {
	if strings.Contains(pane, "Resume from summary") && strings.Contains(pane, "Enter to confirm") {
		return "Enter"
	}
	if !hasDialogChrome(pane) {
		return ""
	}
	if dialogDenyPattern.MatchString(pane) {
		return ""
	}
	return "Escape"
}

// hasDialogChrome reports whether pane shows an interactive modal's confirm AND
// cancel footer — the chrome that distinguishes a blocking dialog from ordinary
// output. Mirrors the same check in the tmux package's input classifier.
func hasDialogChrome(pane string) bool {
	return strings.Contains(pane, "Enter to confirm") && strings.Contains(pane, "Esc to cancel")
}

// RecoverQuickExit implements the --session-id → --resume → fresh
// degradation ladder for quick exits (see the supervisor's doc comment).
func (TmuxTUIDriver) RecoverQuickExit(args []string) ([]string, harness.QuickExitAction) {
	switch {
	case hasSessionIDArg(args):
		return convertSessionIDToResume(args), harness.QuickExitRetryArgs
	case hasResumeArg(args):
		return stripResumeArg(args), harness.QuickExitClearAndNoResume
	default:
		return args, harness.QuickExitClearSession
	}
}

// stripResumeArg removes --resume and its value from claude args.
func stripResumeArg(args []string) []string {
	var result []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--resume" && i+1 < len(args) {
			i++ // skip the value too
			continue
		}
		result = append(result, args[i])
	}
	return result
}

// hasResumeArg reports whether a `--resume <id>` pair is present in args.
func hasResumeArg(args []string) bool {
	for i := 0; i < len(args); i++ {
		if args[i] == "--resume" && i+1 < len(args) {
			return true
		}
	}
	return false
}

// hasSessionIDArg reports whether a `--session-id <id>` pair is present in args.
func hasSessionIDArg(args []string) bool {
	for i := 0; i < len(args); i++ {
		if args[i] == "--session-id" && i+1 < len(args) {
			return true
		}
	}
	return false
}

// convertSessionIDToResume rewrites every `--session-id <id>` pair into
// `--resume <id>`, leaving all other args untouched. Used by the quick-exit
// recovery: a freshly minted `--session-id` is rejected once its jsonl exists
// on disk, so the supervisor retries by resuming that same session. A
// `--session-id` flag with no following value, or args with none at all, are
// returned unchanged.
func convertSessionIDToResume(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "--session-id" && i+1 < len(args) {
			out = append(out, "--resume", args[i+1])
			i++ // consumed the value
			continue
		}
		out = append(out, args[i])
	}
	return out
}
