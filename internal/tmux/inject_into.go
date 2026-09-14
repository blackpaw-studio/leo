package tmux

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrComposerBusy    = errors.New("composer busy")
	ErrComposerUnknown = errors.New("composer unknown")
	ErrPasteFailed     = errors.New("paste failed")
)

// InjectInto pastes only into a visibly empty composer. It intentionally
// never probes with keys: interactive dispatch shares the composer with a
// human, so an uncertain state fails closed.
func InjectInto(ctx context.Context, tmuxPath, paneID string, classify ComposerClassifier, text string, beforeEnter func()) error {
	capture := func() (string, error) {
		out, err := execCommand(ctx, tmuxPath, Args("capture-pane", "-p", "-t", paneID)...).Output()
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
	if err := execCommand(ctx, tmuxPath, Args("set-buffer", "--", text)...).Run(); err != nil {
		return fmt.Errorf("set paste buffer: %w", err)
	}
	if err := execCommand(ctx, tmuxPath, Args("paste-buffer", "-d", "-t", paneID)...).Run(); err != nil {
		return fmt.Errorf("paste buffer: %w", err)
	}
	needle := strings.TrimSpace(text)
	if needle == "" {
		return ErrPasteFailed
	}
	confirmed := false
	for i := 0; i < submitConfirmAttempts; i++ {
		after, err := capture()
		if err == nil && strings.Contains(after, needle) {
			confirmed = true
			break
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
		beforeEnter()
	}
	if err := execCommand(ctx, tmuxPath, Args("send-keys", "-t", paneID, "Enter")...).Run(); err != nil {
		return fmt.Errorf("submit paste: %w", err)
	}
	return nil
}
