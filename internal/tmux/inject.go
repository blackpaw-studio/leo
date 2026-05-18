package tmux

import (
	"context"
	"fmt"
	"os/exec"
)

// execCommand is the seam tests replace.
var execCommand = exec.CommandContext

// bufferName is the tmux named buffer used to stage prompt bodies before
// pasting them into a session. A single shared name is safe because the
// daemon serializes injection calls per-session via the pump goroutine;
// no two callers ever inject into the same session concurrently.
const bufferName = SocketName

// InjectPrompt sends body to the claude running in `session` as a single
// submission. Uses set-buffer + paste-buffer (-d deletes after paste) to
// avoid character-by-character races; multi-line bodies preserved; Enter
// submits.
func InjectPrompt(ctx context.Context, tmuxPath, session, body string) error {
	setArgs := Args("set-buffer", "-b", bufferName, "--", body)
	pasteArgs := Args("paste-buffer", "-b", bufferName, "-t", session, "-d")
	enterArgs := Args("send-keys", "-t", session, "Enter")
	for _, args := range [][]string{setArgs, pasteArgs, enterArgs} {
		cmd := execCommand(ctx, tmuxPath, args...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("tmux %s: %w: %s", args[2], err, string(out))
		}
	}
	return nil
}

// AbortPrompt cancels a mid-turn claude by sending Escape then Ctrl-C.
// Best-effort; records the first error but continues with both keys.
func AbortPrompt(ctx context.Context, tmuxPath, session string) error {
	keys := []string{"Escape", "C-c"}
	var firstErr error
	for _, k := range keys {
		cmd := execCommand(ctx, tmuxPath, Args("send-keys", "-t", session, k)...)
		if out, err := cmd.CombinedOutput(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("tmux send-keys %s: %w: %s", k, err, string(out))
		}
	}
	return firstErr
}
