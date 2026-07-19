package tmux

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// devChannelPromptMarker is a substring unique to the claude
// "Loading development channels" confirmation screen. When visible the
// default-selected option is "I am using this for local development", so a
// single Enter accepts the dev-channel load.
const devChannelPromptMarker = "I am using this for local development"

// devChannelDefaultTimeout is the total time AcceptDevChannelPrompt will wait
// for the prompt to appear before giving up.
const devChannelDefaultTimeout = 30 * time.Second

// devChannelPollInterval controls how often the pane is captured while
// waiting for the prompt to appear.
const devChannelPollInterval = 200 * time.Millisecond

// AcceptDevChannelPrompt polls a tmux session's visible pane for the claude
// "--dangerously-load-development-channels" confirmation, then sends Enter to
// accept. It returns nil on successful accept, an error if the prompt never
// appeared within the timeout, or ctx.Err() if the context is cancelled.
//
// The default-highlighted option is the accept path, so a single Enter is
// sufficient. Callers typically run this in a goroutine right after launching
// a tmux session that carries the --dangerously-load-development-channels flag.
func AcceptDevChannelPrompt(ctx context.Context, tmuxPath, sessionName string) error {
	return acceptDevChannelPrompt(ctx, tmuxPath, sessionName, devChannelDefaultTimeout, devChannelPollInterval)
}

// acceptDevChannelPrompt is the testable inner form with injectable timings.
func acceptDevChannelPrompt(ctx context.Context, tmuxPath, sessionName string, timeout, poll time.Duration) error {
	if tmuxPath == "" {
		return fmt.Errorf("tmux path is empty")
	}
	if sessionName == "" {
		return fmt.Errorf("session name is empty")
	}

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("dev-channel prompt never appeared in session %q within %s", sessionName, timeout)
		}

		target, err := ResolvePane(ctx, tmuxPath, sessionName)
		if err != nil {
			// Best-effort: fall back to the active-pane target rather than
			// erroring louder than before ResolvePane existed.
			target = PaneTarget(sessionName)
		}

		pane, err := execCommand(ctx, tmuxPath, Args("capture-pane", "-p", "-t", target)...).Output()
		if err != nil {
			// Session may not exist yet (race with new-session) or was killed;
			// keep polling until the deadline.
			continue
		}

		if !strings.Contains(string(pane), devChannelPromptMarker) {
			continue
		}

		if err := execCommand(ctx, tmuxPath, Args("send-keys", "-t", target, "Enter")...).Run(); err != nil {
			return fmt.Errorf("send-keys Enter to session %q: %w", sessionName, err)
		}
		return nil
	}
}
