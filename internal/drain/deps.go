// Package drain lets busy agents reach a safe stopping point before a daemon
// restart and tells the same agents to resume afterwards. The daemon owns the
// drain: it holds cron, messages the busy agents, waits for them to go idle
// and persists the drain set for the next daemon to resume. The package
// depends only on the small interfaces below; the daemon, service and web
// packages provide the adapters.
package drain

import (
	"context"
	"errors"
	"time"
)

// Transport names how a probe or delivery reached an agent.
const (
	TransportBridge   = "bridge"
	TransportComposer = "composer"
)

// Probe is one agent's busy state as the drain sees it.
type Probe struct {
	Name string `json:"name"`
	// Exists is false once the agent is no longer supervised.
	Exists bool `json:"exists"`
	Busy   bool `json:"busy"`
	// Transport is TransportBridge while the agent's mod is connected and
	// TransportComposer otherwise.
	Transport string `json:"transport,omitempty"`
	// Kind is KindDispatch for a running interactive dispatch, listed so
	// the operator sees it but never drained: the restarted daemon adopts
	// it. Empty for an agent.
	Kind string `json:"kind,omitempty"`
}

// KindDispatch marks a Probe that is a running dispatch, not an agent.
const KindDispatch = "dispatch"

// Prober reports which agents are mid-turn.
type Prober interface {
	// BusyAgents returns a probe for every busy agent, and only those.
	BusyAgents(ctx context.Context) ([]Probe, error)
	// Probe reports one agent; Exists is false for an unknown name.
	Probe(ctx context.Context, name string) (Probe, error)
}

// Delivery describes how a message reached an agent.
type Delivery struct {
	Transport string
	// Queued is true when the bridge outbox accepted the message for a later
	// turn boundary instead of delivering it now.
	Queued bool
}

// ErrNotifierUnavailable means no message path exists (the web server, which
// owns agent messaging, is disabled). The drain then only waits.
var ErrNotifierUnavailable = errors.New("drain: agent messaging unavailable")

// Notifier sends messages to agents.
type Notifier interface {
	// Message sends text to name. id is the bridge command id ("" lets the
	// bridge choose one).
	Message(ctx context.Context, name, text, id string) (Delivery, error)
	// Holds reports whether name's durable outbox still holds command id.
	Holds(name, id string) (bool, error)
}

// CronGate defers cron firings while a drain is active.
type CronGate interface {
	Hold()
	Release()
	// Held returns the tasks whose firings were deferred, once each, in
	// first-fire order.
	Held() []string
}

// Store persists the drain set for the next daemon.
type Store interface {
	Write(PendingResume) error
	Remove() error
}

// Clock is the time source; it mirrors bridge.Clock.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

// SystemClock is the wall clock.
type SystemClock struct{}

func (SystemClock) Now() time.Time                         { return time.Now() }
func (SystemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
