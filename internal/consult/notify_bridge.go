package consult

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/blackpaw-studio/leo/internal/bridge"
	"github.com/blackpaw-studio/leo/internal/outbox"
)

// NotificationTransportBridge marks a notification claimed for a caller's
// leo bridge: it is queued under its deterministic outbox id, so a claim a
// restart interrupted can be queued again without delivering twice.
const NotificationTransportBridge = "bridge"

// transportNotificationDelivery is a NotificationDelivery that picks a
// transport per caller ("" for the legacy one, or
// NotificationTransportBridge) before the claim, and sends over the one the
// claim recorded.
type transportNotificationDelivery interface {
	NotificationTransport(ctx context.Context, rec Record) string
	DeliverVia(ctx context.Context, rec Record, transport, key, line string) error
}

// notificationTransport is the transport delivery picks for rec; "" for a
// delivery with one transport only.
func notificationTransport(ctx context.Context, delivery NotificationDelivery, rec Record) string {
	if t, ok := delivery.(transportNotificationDelivery); ok {
		return t.NotificationTransport(ctx, rec)
	}
	return ""
}

// deliverNotificationLine sends line over transport, with its key, when
// the delivery picks transports; else as Deliver does.
func deliverNotificationLine(ctx context.Context, delivery NotificationDelivery, rec Record, transport, key, line string) error {
	if t, ok := delivery.(transportNotificationDelivery); ok {
		return t.DeliverVia(ctx, rec, transport, key, line)
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
// whose leo bridge is connected as a bridge deliver, queued in its owner's
// durable outbox; every other caller goes through Fallback (the peer inbox
// for claude, tmux for the other harnesses).
type BridgeNotificationDelivery struct {
	// Route resolves rec's caller to its live bridge generation and the
	// agent whose outbox keeps its messages ("" for a dispatch, which keeps
	// none); ok is false when the caller has no connected bridge.
	Route func(ctx context.Context, rec Record) (agent string, target bridge.Target, ok bool)
	// Queue queues cmd for target, in agent's outbox when agent is set. The
	// ticket is nil when nothing new was queued.
	Queue    func(agent string, target bridge.Target, cmd bridge.Command) (*bridge.Ticket, error)
	Fallback NotificationDelivery
	// acked is called once the caller's mod acks a notification's deliver
	// ok; a rejection, or a deliver that never settles, never calls it.
	acked func(rec Record, key string)
}

// OnAcked installs the function called, outside any dispatcher lock, when a
// notification this delivery queued is acked ok by the caller's mod.
func (b *BridgeNotificationDelivery) OnAcked(fn func(rec Record, key string)) { b.acked = fn }

// NewBridgeNotificationDelivery routes through router. A record carrying
// the caller's bridge key routes on it: to that key's live generation, and
// the outbox of the agent keyOwner says holds the key now (a renamed
// caller's included), or the bridge alone for a calling dispatch. A legacy
// record without a key counts as bridged only while its caller agent's mod
// is connected and its caller pane is that agent's own primary pane
// (primaryPane): a dispatch inherits its orchestrator's process name, so
// the name alone could hand a nested dispatch's notification to the
// orchestrator.
func NewBridgeNotificationDelivery(router *bridge.Router, primaryPane func(ctx context.Context, agent string) (string, error), keyOwner func(key string) (agent string, ok bool), fallback NotificationDelivery) *BridgeNotificationDelivery {
	return &BridgeNotificationDelivery{
		Route: func(ctx context.Context, rec Record) (string, bridge.Target, bool) {
			if rec.CallerBridgeKey != "" {
				return routeByBridgeKey(router, keyOwner, rec.CallerBridgeKey)
			}
			return routeByCallerPane(ctx, router, primaryPane, rec)
		},
		Queue: func(agent string, target bridge.Target, cmd bridge.Command) (*bridge.Ticket, error) {
			if agent == "" {
				return router.Hub.EnqueueTo(target, cmd)
			}
			return router.Deliver(agent, target, cmd, "")
		},
		Fallback: fallback,
	}
}

func routeByBridgeKey(router *bridge.Router, keyOwner func(string) (string, bool), key string) (string, bridge.Target, bool) {
	if router == nil || router.Hub == nil {
		return "", bridge.Target{}, false
	}
	target, ok := router.Hub.Live(key)
	if !ok {
		return "", bridge.Target{}, false
	}
	if _, isDispatch := DispatchIDFromBridgeKey(key); isDispatch {
		return "", target, true
	}
	agent, ok := keyOwner(key)
	if !ok {
		return "", bridge.Target{}, false
	}
	return agent, target, true
}

func routeByCallerPane(ctx context.Context, router *bridge.Router, primaryPane func(context.Context, string) (string, error), rec Record) (string, bridge.Target, bool) {
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
// fallback; the sweep calls DeliverVia.
func (b *BridgeNotificationDelivery) Deliver(ctx context.Context, rec Record, line string) error {
	return b.Fallback.Deliver(ctx, rec, line)
}

// NotificationTransport picks the bridge for a caller whose bridge is
// connected now, else the fallback.
func (b *BridgeNotificationDelivery) NotificationTransport(ctx context.Context, rec Record) string {
	if _, _, ok := b.Route(ctx, rec); ok {
		return NotificationTransportBridge
	}
	return ""
}

// DeliverVia sends line over the transport its claim recorded. Over the
// bridge it is queued under key's command id and counts as delivered once
// queued: the outbox keeps it until the mod takes it, across a relaunch of
// the caller, and one already queued under that id is the same message. A
// bridge claim never falls back: the deliver may be queued already, so a
// caller whose bridge is gone gets ErrNotificationNotSent until it is back.
func (b *BridgeNotificationDelivery) DeliverVia(ctx context.Context, rec Record, transport, key, line string) error {
	if transport != NotificationTransportBridge {
		return b.Fallback.Deliver(ctx, rec, line)
	}
	agent, target, ok := b.Route(ctx, rec)
	if !ok {
		return fmt.Errorf("%w: the caller's leo bridge is not connected", ErrNotificationNotSent)
	}
	cmd := bridge.Deliver(line, false)
	cmd.ID = NotificationCommandID(rec.ID, key)
	ticket, err := b.Queue(agent, target, cmd)
	switch {
	case err == nil:
		b.watchAck(ticket, rec, key)
		return nil
	case errors.Is(err, outbox.ErrDuplicate):
		return nil
	default:
		return fmt.Errorf("%w: queueing on the caller's leo bridge: %v", ErrNotificationNotSent, err)
	}
}

// watchAck calls acked once ticket settles ok. The goroutine ends when the
// ticket settles: acked, rejected, forgotten with its generation, or dropped
// by the hub closing.
func (b *BridgeNotificationDelivery) watchAck(ticket *bridge.Ticket, rec Record, key string) {
	if ticket == nil || b.acked == nil {
		return
	}
	acked := b.acked
	go func() {
		<-ticket.Done()
		if ticket.Err() == nil {
			acked(rec, key)
		}
	}()
}
