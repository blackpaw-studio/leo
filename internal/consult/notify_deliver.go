package consult

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/blackpaw-studio/leo/internal/peerinbox"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

type NotificationCommand func(context.Context, string, ...string) *exec.Cmd

// TmuxNotificationDelivery delivers to the immutable caller pane captured at
// acceptance. Its readiness check is passive and includes the session guard.
type TmuxNotificationDelivery struct {
	TmuxPath       string
	Command        NotificationCommand
	ResolveSocket  func(context.Context, peerinbox.ExecFunc, string) (string, error)
	DeliverSocket  func(context.Context, string, string) error
	CommandTimeout time.Duration
}

func NewTmuxNotificationDelivery(tmuxPath string, command NotificationCommand) *TmuxNotificationDelivery {
	if command == nil {
		command = func(ctx context.Context, name string, args ...string) *exec.Cmd {
			return exec.CommandContext(ctx, name, args...)
		}
	}
	return &TmuxNotificationDelivery{TmuxPath: tmuxPath, Command: command, ResolveSocket: peerinbox.ResolveSocket, DeliverSocket: peerinbox.Deliver, CommandTimeout: 5 * time.Second}
}

func (d *TmuxNotificationDelivery) commandContext(parent context.Context) (context.Context, context.CancelFunc) {
	timeout := d.CommandTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return context.WithTimeout(parent, timeout)
}

func (d *TmuxNotificationDelivery) paneCurrent(ctx context.Context, rec Record) bool {
	ctx, cancel := d.commandContext(ctx)
	defer cancel()
	out, err := d.Command(ctx, d.TmuxPath, tmux.Args("display-message", "-p", "-t", rec.CallerPaneID, "#{session_id}")...).Output()
	return err == nil && strings.TrimSpace(string(out)) == rec.CallerSessionID && rec.CallerSessionID != ""
}

func notificationClassifier(harness string) tmux.ComposerClassifier {
	switch harness {
	case "codex":
		return tmux.CodexComposerClassifier
	case "opencode":
		return tmux.OpenCodeComposerClassifier
	default:
		return nil
	}
}

func (d *TmuxNotificationDelivery) Ready(ctx context.Context, rec Record) bool {
	if !d.paneCurrent(ctx, rec) {
		return false
	}
	classify := notificationClassifier(rec.CallerHarness)
	if classify == nil {
		return rec.CallerHarness == "claude"
	}
	commandCtx, cancel := d.commandContext(ctx)
	defer cancel()
	out, err := d.Command(commandCtx, d.TmuxPath, tmux.Args("capture-pane", "-p", "-t", rec.CallerPaneID)...).Output()
	return err == nil && classify(string(out)) == tmux.ComposerEmpty
}

func (d *TmuxNotificationDelivery) Deliver(ctx context.Context, rec Record, line string) error {
	if !d.paneCurrent(ctx, rec) {
		return fmt.Errorf("%w: stale caller pane", ErrNotificationNotSent)
	}
	if rec.CallerHarness == "claude" {
		socket, err := d.ResolveSocket(ctx, peerinbox.ExecFunc(d.Command), rec.CallerPaneID)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrNotificationNotSent, err)
		}
		if err := d.DeliverSocket(ctx, socket, line); err != nil {
			return fmt.Errorf("%w: %v", ErrNotificationAmbiguous, err)
		}
		return nil
	}
	classify := notificationClassifier(rec.CallerHarness)
	if classify == nil {
		return fmt.Errorf("%w: unsupported caller harness %q", ErrNotificationNotSent, rec.CallerHarness)
	}
	err := tmux.InjectIntoWith(ctx, d.TmuxPath, rec.CallerPaneID, classify, line, nil, tmux.CommandFunc(d.Command))
	if err != nil {
		if errors.Is(err, tmux.ErrComposerBusy) || errors.Is(err, tmux.ErrComposerUnknown) {
			return fmt.Errorf("%w: %v", ErrNotificationNotSent, err)
		}
		return fmt.Errorf("%w: %v", ErrNotificationAmbiguous, err)
	}
	return nil
}
