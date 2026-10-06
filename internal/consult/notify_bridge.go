package consult

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/blackpaw-studio/leo/internal/bridge"
	"github.com/blackpaw-studio/leo/internal/outbox"
)

// keyedNotificationDelivery is a NotificationDelivery that also wants the
// notification's key: the sweep calls DeliverNotification in place of
// Deliver.
type keyedNotificationDelivery interface {
	DeliverNotification(ctx context.Context, rec Record, key, line string) error
}

// deliverNotificationLine hands line to delivery, with its key when the
// delivery takes one.
func deliverNotificationLine(ctx context.Context, delivery NotificationDelivery, rec Record, key, line string) error {
	if keyed, ok := delivery.(keyedNotificationDelivery); ok {
		return keyed.DeliverNotification(ctx, rec, key, line)
	}
	return delivery.Deliver(ctx, rec, line)
}

// NotificationCommandID is the bridge command id of dispatch id's
// notification key: notify-<id>-<turn> for a turn's (<id>#<turn>), and
// notify-<id>-run for the whole run's. Every delivery of one notification
// carries it, so the outbox, the hub and the mod each take a repeat for
// the one they have.
func NotificationCommandID(id, key string) string {
	switch {
	case key == id:
		return "notify-" + id + "-run"
	case strings.HasPrefix(key, id+"#"):
		return "notify-" + id + "-" + strings.TrimPrefix(key, id+"#")
	default:
		return "notify-" + id + "-" + strings.NewReplacer("#", "-", " ", "-").Replace(key)
	}
}

// BridgeNotificationDelivery sends a completion notification to a caller
// agent whose leo bridge is connected as a bridge deliver, queued in its
// durable outbox; every other caller goes through Fallback (the peer inbox
// for claude, tmux for the other harnesses).
type BridgeNotificationDelivery struct {
	// Route resolves rec's caller to the agent and live bridge generation
	// its notifications go to; ok is false when it has none of its own.
	Route func(ctx context.Context, rec Record) (agent string, target bridge.Target, ok bool)
	// Queue queues cmd for agent's generation target, durably when the
	// daemon keeps outboxes.
	Queue    func(agent string, target bridge.Target, cmd bridge.Command) error
	Fallback NotificationDelivery
}

// NewBridgeNotificationDelivery routes through router. A caller counts as
// bridged only while its mod is connected and rec's caller pane is the
// agent's own primary pane (primaryPane): a dispatch inherits its
// orchestrator's process name, so the name alone could hand a nested
// dispatch's notification to the orchestrator.
func NewBridgeNotificationDelivery(router *bridge.Router, primaryPane func(ctx context.Context, agent string) (string, error), fallback NotificationDelivery) *BridgeNotificationDelivery {
	return &BridgeNotificationDelivery{
		Route: func(ctx context.Context, rec Record) (string, bridge.Target, bool) {
			if rec.Caller == "" || rec.CallerPaneID == "" {
				return "", bridge.Target{}, false
			}
			target, ok := router.Route(rec.Caller)
			if !ok {
				return "", bridge.Target{}, false
			}
			pane, err := primaryPane(ctx, rec.Caller)
			if err != nil || pane != rec.CallerPaneID {
				return "", bridge.Target{}, false
			}
			return rec.Caller, target, true
		},
		Queue: func(agent string, target bridge.Target, cmd bridge.Command) error {
			_, err := router.Deliver(agent, target, cmd, "")
			return err
		},
		Fallback: fallback,
	}
}

// Ready is always true for a bridged caller: the mod submits the deliver
// once its claude is idle.
func (b *BridgeNotificationDelivery) Ready(ctx context.Context, rec Record) bool {
	if _, _, ok := b.Route(ctx, rec); ok {
		return true
	}
	return b.Fallback.Ready(ctx, rec)
}

// Deliver has no key to derive a command id from, so it always takes the
// fallback; the sweep calls DeliverNotification.
func (b *BridgeNotificationDelivery) Deliver(ctx context.Context, rec Record, line string) error {
	return b.Fallback.Deliver(ctx, rec, line)
}

// DeliverNotification queues line for a bridged caller under key's
// command id, or hands it to the fallback. Once queued it counts as
// delivered: the outbox keeps it until the mod takes it, across a relaunch
// of the caller. One already queued under that id is the same message.
func (b *BridgeNotificationDelivery) DeliverNotification(ctx context.Context, rec Record, key, line string) error {
	agent, target, ok := b.Route(ctx, rec)
	if !ok {
		return b.Fallback.Deliver(ctx, rec, line)
	}
	cmd := bridge.Deliver(line, false)
	cmd.ID = NotificationCommandID(rec.ID, key)
	err := b.Queue(agent, target, cmd)
	switch {
	case err == nil, errors.Is(err, outbox.ErrDuplicate):
		return nil
	default:
		return fmt.Errorf("%w: queueing on %s's leo bridge: %v", ErrNotificationNotSent, agent, err)
	}
}
