package tmux

import (
	"context"
	"fmt"
	"os/exec"
)

// execCommand is the seam tests replace.
var execCommand = exec.CommandContext

// sessionBufferName derives a tmux buffer name unique to the target session
// so concurrent injection calls into different sessions never share the same
// staging buffer. Two pump goroutines (one per session) can therefore run
// set-buffer / paste-buffer in any interleaving without clobbering each
// other.
func sessionBufferName(session string) string {
	return "leo-" + session
}

// InjectPrompt sends body to the claude running in `session` as a single
// submission. Uses set-buffer + paste-buffer (-d deletes after paste) to
// avoid character-by-character races; multi-line bodies preserved; Enter
// submits.
func InjectPrompt(ctx context.Context, tmuxPath, session, body string) error {
	buf := sessionBufferName(session)
	setArgs := Args("set-buffer", "-b", buf, "--", body)
	pasteArgs := Args("paste-buffer", "-b", buf, "-t", session, "-d")
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
