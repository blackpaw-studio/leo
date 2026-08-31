package claude

import (
	"regexp"
	"strings"

	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

// dialogDenyPattern marks dialogs that make a consequential decision — never
// auto-answered, always left for a human. Word-boundaried, case-insensitive.
var dialogDenyPattern = regexp.MustCompile(`(?i)\b(trust|permission|delete|overwrite)\b`)

// DialogKey decides how to clear a blocking claude startup/announcement
// dialog visible in pane. It returns the tmux key to send ("Enter" or "Escape"),
// or "" to leave the pane untouched. Pure (no I/O) so it is unit-tested directly.
//
// This runs on EVERY session poll for the session's whole lifetime, so it must
// never fire on ordinary output. Normal conversational text CAN and does
// contain both footer phrases — an agent discussing this very feature, code
// review output, or test logs may print "Enter to confirm" and "Esc to
// cancel" as prose. The reliable discriminator is not the phrases' mere
// presence but their shape: a blocking modal renders them as a dedicated
// footer line (hint segments like "Enter to confirm" joined by "·"), never
// embedded mid-sentence. A numbered menu alone is NOT sufficient either.
//
// Order matters:
//  1. "Resume from summary" is a known prompt we ACCEPT (Enter) when paired
//     with a genuine "Enter to confirm" footer line.
//  2. Otherwise, act only when genuine dialog chrome (both confirm AND cancel
//     rendered on one dedicated footer line) is present; anything else is
//     left untouched.
//  3. A modal mentioning a consequential decision (trust/permission/delete/
//     overwrite) is left for a human — never auto-answered.
//  4. Any other modal is an announcement/opt-in we DECLINE with Escape so the
//     agent's behavior stays stable.
func DialogKey(pane string) string {
	if strings.Contains(pane, "Resume from summary") && tmux.HasConfirmFooterLine(pane) {
		return "Enter"
	}
	if !tmux.HasDialogChrome(pane) {
		return ""
	}
	if dialogDenyPattern.MatchString(pane) {
		return ""
	}
	return "Escape"
}

// RecoverQuickExitArgs implements the --session-id → --resume → fresh
// degradation ladder for quick exits (see the supervisor's doc comment).
func RecoverQuickExitArgs(args []string) ([]string, harness.QuickExitAction) {
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
