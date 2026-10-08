package consult

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrPaneMoved means a guarded move found its pane no longer where the caller
// probed it, so tmux did nothing. The caller probes again to see where the pane
// went (and whether it is still the dispatcher's to move).
var ErrPaneMoved = errors.New("pane is no longer where it was probed")

// guardFailedSentinel is what a guarded move prints when its guard is false.
// Moves print a window id (@N) or nothing, so it cannot be mistaken for one.
const guardFailedSentinel = "leo-guard-failed"

// tmuxIDPattern matches the ids tmux gives sessions ($N), windows (@N) and
// panes (%N). They go into a format string and a command line, so nothing else
// is let through.
var tmuxIDPattern = regexp.MustCompile(`^[$@%][0-9]+$`)

// tmuxBareWord matches the tokens that need no quoting in a tmux command line.
// '$' is left out: $NAME expands from the environment there.
var tmuxBareWord = regexp.MustCompile(`^[A-Za-z0-9_@%:.=/+-]+$`)

// guardedMove runs the tmux command move on the server only while pane is still
// where from says it is, and returns what the command printed. The check and the
// move are one tmux command (if-shell -F), so nothing can happen between them:
// a separate probe followed by a move leaves a gap for the user to relocate the
// pane, or for an attach to link its window, after the probe and before the move.
//
// The condition is a format evaluated against the probed window, S:W. If that
// window is no longer in that session the target does not resolve, which is not
// an error to if-shell: the formats just expand empty, and the comparisons fail.
// The pane is checked for by listing the window's panes, not by naming it in the
// target: tmux resolves a %N in a target on its own, wherever the pane is, so
// "S:W.%N" would still hold after the pane left W. The window is targeted by id
// and session because a window linked into a watch session answers to a bare %N
// with the watch session. extra adds clauses to the guard.
//
// It returns ErrPaneMoved when the guard is false; a move that fails is a plain
// error.
func (r *TmuxInteractiveRuntime) guardedMove(ctx context.Context, pane string, from PaneLocation, extra []string, move ...string) (string, error) {
	if err := checkGuardIDs(pane, from); err != nil {
		return "", err
	}
	out, err := r.output(ctx, "if-shell", "-F", "-t", from.SessionID+":"+from.WindowID,
		guardCondition(pane, from, extra...), tmuxCommandLine(move...), "display-message -p "+guardFailedSentinel)
	if err != nil {
		return "", err
	}
	printed := strings.TrimSpace(string(out))
	if printed == guardFailedSentinel {
		return "", ErrPaneMoved
	}
	return printed, nil
}

func checkGuardIDs(pane string, from PaneLocation) error {
	for _, id := range []string{pane, from.SessionID, from.WindowID} {
		if !tmuxIDPattern.MatchString(id) {
			return fmt.Errorf("move pane %s: %q is not a tmux id (the pane was not probed)", pane, id)
		}
	}
	return nil
}

// paneRef names pane by the session and window it was probed in, for the move
// itself (the guard has just confirmed it is there).
func paneRef(pane string, from PaneLocation) string {
	return from.SessionID + ":" + from.WindowID + "." + pane
}

// guardCondition is the format that is true while pane is still one of the
// panes of the probed window in the probed session, plus the extra clauses. The
// pane list is delimited on both sides so %2 does not match %20.
func guardCondition(pane string, from PaneLocation, extra ...string) string {
	clauses := append([]string{
		"#{==:#{session_id}," + from.SessionID + "}",
		"#{==:#{window_id}," + from.WindowID + "}",
		"#{m:*|" + pane + "|*,#{P:|#{pane_id}|}}",
	}, extra...)
	cond := clauses[len(clauses)-1]
	for i := len(clauses) - 2; i >= 0; i-- {
		cond = "#{&&:" + clauses[i] + "," + cond + "}"
	}
	return cond
}

func windowPanesAre(n int) string      { return fmt.Sprintf("#{==:#{window_panes},%d}", n) }
func sessionWindowsAre(n int) string   { return fmt.Sprintf("#{==:#{session_windows},%d}", n) }
func windowUnlinked() string           { return "#{==:#{window_linked},0}" }
func quoteTmuxWord(word string) string { return "'" + strings.ReplaceAll(word, "'", `'"'"'`) + "'" }

// tmuxCommandLine renders argv as one tmux command for if-shell to run. Every
// word but the command name is quoted unless it is plainly safe, so a window
// name with a ';' or a quote cannot become a second command.
func tmuxCommandLine(argv ...string) string {
	words := make([]string, len(argv))
	for i, word := range argv {
		if i == 0 || tmuxBareWord.MatchString(word) {
			words[i] = word
			continue
		}
		words[i] = quoteTmuxWord(word)
	}
	return strings.Join(words, " ")
}
