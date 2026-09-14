package tmux

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"
)

const (
	injectCommandTimeout = 5 * time.Second
	injectWaitDelay      = 100 * time.Millisecond
	// injectable so confirmation failure paths can be tested without waiting.
)

var injectConfirmAttempts = 15

var injectBufferNonce atomic.Uint64

// CommandFunc starts a tmux command. It makes strict injection usable by
// callers that own their command-execution seam.
type CommandFunc func(context.Context, string, ...string) *exec.Cmd

var (
	ErrComposerBusy    = errors.New("composer busy")
	ErrComposerUnknown = errors.New("composer unknown")
	ErrPasteFailed     = errors.New("paste failed")
)

// InjectInto pastes only into a visibly empty composer. It intentionally
// never probes with keys: interactive dispatch shares the composer with a
// human, so an uncertain state fails closed.
func InjectInto(ctx context.Context, tmuxPath, paneID string, classify ComposerClassifier, text string, beforeEnter func() error) error {
	return InjectIntoWith(ctx, tmuxPath, paneID, classify, text, beforeEnter, execCommand)
}

// InjectIntoWith is InjectInto with an injectable tmux command runner.
func InjectIntoWith(ctx context.Context, tmuxPath, paneID string, classify ComposerClassifier, text string, beforeEnter func() error, command CommandFunc) error {
	capture := func() (string, error) {
		cctx, cancel := context.WithTimeout(ctx, injectCommandTimeout)
		defer cancel()
		c := command(cctx, tmuxPath, Args("capture-pane", "-p", "-t", paneID)...)
		c.WaitDelay = injectWaitDelay
		out, err := c.Output()
		return string(out), err
	}
	before, err := capture()
	if err != nil {
		return fmt.Errorf("capture composer: %w", err)
	}
	switch classify(before) {
	case ComposerBusy:
		return ErrComposerBusy
	case ComposerEmpty:
	default:
		return ErrComposerUnknown
	}
	buffer := fmt.Sprintf("leo-dispatch-%s-%d", strings.TrimPrefix(paneID, "%"), injectBufferNonce.Add(1))
	if err := runInjectCommand(ctx, tmuxPath, command, Args("set-buffer", "-b", buffer, "--", text)...); err != nil {
		return fmt.Errorf("set paste buffer: %w", err)
	}
	bufferDeleted := false
	defer func() {
		if !bufferDeleted {
			// paste-buffer -d normally deletes this, but a failed paste leaves it
			// behind. Cleanup is bounded by runInjectCommand and best-effort.
			_ = runInjectCommand(ctx, tmuxPath, command, Args("delete-buffer", "-b", buffer)...)
		}
	}()
	if err := runInjectCommand(ctx, tmuxPath, command, Args("paste-buffer", "-b", buffer, "-d", "-t", paneID)...); err != nil {
		return fmt.Errorf("paste buffer: %w", err)
	}
	bufferDeleted = true
	needle := submitConfirmNeedle(text)
	if needle == "" {
		return ErrPasteFailed
	}
	confirmed := false
	for i := 0; i < injectConfirmAttempts; i++ {
		after, err := capture()
		if err == nil && after != before && composerPasteConfirmed(after, needle) {
			confirmed = true
			break
		}
		if i+1 < injectConfirmAttempts {
			wait := time.NewTimer(submitConfirmPoll)
			select {
			case <-wait.C:
			case <-ctx.Done():
				wait.Stop()
				return ctx.Err()
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
	if !confirmed {
		return ErrPasteFailed
	}
	if beforeEnter != nil {
		if err := beforeEnter(); err != nil {
			return err
		}
	}
	if err := runInjectCommand(ctx, tmuxPath, command, Args("send-keys", "-t", paneID, "Enter")...); err != nil {
		return fmt.Errorf("submit paste: %w", err)
	}
	return nil
}

// composerPasteConfirmed only trusts confirmation inside the active composer.
// History can contain an earlier copy of the message, especially after a
// scrollback capture, and must not cause an Enter into an empty composer.
func composerPasteConfirmed(capture, needle string) bool {
	lines := strings.Split(capture, "\n")
	if _, composerLine, bottom, ok := claudeComposerBox(lines); ok {
		return composerLinesContainPaste(lines[composerLine:bottom], needle)
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(strings.TrimLeft(lines[i], " \t"), "›") {
			return composerLinesContainPaste(lines[i:], needle)
		}
	}
	return false
}

func composerLinesContainPaste(lines []string, needle string) bool {
	for i, line := range lines {
		content := strings.TrimSpace(line)
		if i == 0 {
			content = strings.TrimSpace(strings.TrimPrefix(strings.TrimLeft(line, " \t"), "❯"))
			content = strings.TrimSpace(strings.TrimPrefix(content, "›"))
		}
		if strings.Contains(content, "[Pasted text") || strings.HasPrefix(content, needle) {
			return true
		}
	}
	return false
}

func runInjectCommand(ctx context.Context, tmuxPath string, command CommandFunc, args ...string) error {
	cctx, cancel := context.WithTimeout(ctx, injectCommandTimeout)
	defer cancel()
	cmd := command(cctx, tmuxPath, args...)
	cmd.WaitDelay = injectWaitDelay
	return cmd.Run()
}
