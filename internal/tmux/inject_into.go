package tmux

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

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
func InjectInto(ctx context.Context, tmuxPath, paneID string, classify ComposerClassifier, text string, beforeEnter func()) error {
	return InjectIntoWith(ctx, tmuxPath, paneID, classify, text, beforeEnter, execCommand)
}

// InjectIntoWith is InjectInto with an injectable tmux command runner.
func InjectIntoWith(ctx context.Context, tmuxPath, paneID string, classify ComposerClassifier, text string, beforeEnter func(), command CommandFunc) error {
	capture := func() (string, error) {
		out, err := command(ctx, tmuxPath, Args("capture-pane", "-p", "-t", paneID)...).Output()
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
	if err := command(ctx, tmuxPath, Args("set-buffer", "--", text)...).Run(); err != nil {
		return fmt.Errorf("set paste buffer: %w", err)
	}
	if err := command(ctx, tmuxPath, Args("paste-buffer", "-d", "-t", paneID)...).Run(); err != nil {
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
	if err := command(ctx, tmuxPath, Args("send-keys", "-t", paneID, "Enter")...).Run(); err != nil {
		return fmt.Errorf("submit paste: %w", err)
	}
	return nil
}
