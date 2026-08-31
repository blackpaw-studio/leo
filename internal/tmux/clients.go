package tmux

import "context"

// HasAttachedClient reports whether sessionName currently has at least one
// tmux client attached. The startup-dialog auto-dismisser uses this to stay
// out of an attended session's way: dismissal exists only to unblock
// UNATTENDED sessions, and a human who has attached and opened an
// interactive menu (e.g. Claude's /mcp picker, which renders the same
// confirm/cancel footer as a blocking startup dialog) must be left to
// dismiss it themselves.
//
// Errors (no such session, tmux not reachable, etc.) report false — fail
// open to dismissal, preserving the existing unattended behavior rather than
// silently going quiet on a session we can't inspect.
func HasAttachedClient(ctx context.Context, tmuxPath, sessionName string) bool {
	out, err := execCommand(ctx, tmuxPath, Args("list-clients", "-t", Target(sessionName))...).Output()
	if err != nil {
		return false
	}
	return len(out) > 0
}
