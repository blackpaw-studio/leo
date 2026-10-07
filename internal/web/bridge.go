package web

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
	"github.com/blackpaw-studio/leo/internal/harness/claude/bridgemod"
	"github.com/blackpaw-studio/leo/internal/leotools"
)

// BridgeOptions wires the claude mod bridge into the web server: Router
// (whose Hub is the daemon's bridge hub) routes agent names to their live
// bridges, Launcher plans bridged dispatch launches. The zero value leaves
// every agent and dispatch on tmux.
type BridgeOptions struct {
	Router   *bridge.Router
	Launcher *bridgemod.Launcher
}

func (o BridgeOptions) hub() *bridge.Hub {
	if o.Router == nil {
		return nil
	}
	return o.Router.Hub
}

// dispatchBridge says which dispatches take their turn state from the
// bridge rather than the claude shell hooks (consult.TmuxInteractiveRuntime).
type dispatchBridge interface {
	BridgeOwnsReports(id string) bool
}

var (
	// bridgeMessageWait is how long a message to a bridged agent waits for
	// the mod's ack before the send is answered 202 (queued, not yet
	// accepted). A busy agent acks only once its turn ends; the deliver
	// stays queued until then and is announced on acceptance.
	bridgeMessageWait = 10 * time.Second
	// bridgeInterruptTimeout bounds an interrupt's ack; the mod interrupts
	// at once, even mid-turn.
	bridgeInterruptTimeout = 10 * time.Second
	// bridgeControlWait is how long compact and clear wait before being
	// answered 202. They run only once the agent's turn ends, and an agent
	// asks for them mid-turn on itself, so waiting longer would deadlock
	// it; the wait just lets an immediate failure surface.
	bridgeControlWait = time.Second
)

// bridgeBackgroundWait bounds how long a send answered 202 keeps waiting
// for its ack, only to announce or log the outcome. A deliver stays queued
// past it; compact and clear are dropped (see bridge.Hub.Send).
const bridgeBackgroundWait = time.Hour

// bridgeSendTimer starts bridgeSend's wait. A test seam.
var bridgeSendTimer = func(d time.Duration) (<-chan time.Time, func() bool) {
	t := time.NewTimer(d)
	return t.C, t.Stop
}

// bridgeRoute returns the live generation of agent name's bridge if its mod
// is connected now. An agent launched with the bridge whose mod is not
// connected is logged: the caller falls back to tmux.
func (s *Server) bridgeRoute(name, what string) (bridge.Target, bool) {
	if target, ok := s.bridgeRouter.Route(name); ok {
		return target, true
	}
	if key, known := s.bridgeRouter.Key(name); known {
		log.Printf("web: %s for %s: leo bridge %s not connected; falling back to tmux", what, strconv.Quote(name), strconv.Quote(key))
	}
	return bridge.Target{}, false
}

// bridgeSendOutcome is how a bridgeSend settled, and whether its caller
// stopped waiting first; one lock orders the two.
type bridgeSendOutcome struct {
	mu        sync.Mutex
	settled   bool
	err       error
	isKept    bool
	abandoned bool
}

// bridgeSend sends cmd to target and waits up to wait for the mod's ack
// (see bridgeAwait).
func (s *Server) bridgeSend(target bridge.Target, what string, cmd bridge.Command, wait time.Duration, onAck func()) (accepted bool, err error) {
	hub := s.bridgeRouter.Hub
	send := func(ctx context.Context) error { return hub.SendTo(ctx, target, cmd) }
	return s.bridgeAwait(target, what, send, nil, wait, onAck)
}

// bridgeAwait runs send, which waits for a command's ack on target, and
// waits up to wait for it. accepted is false when the wait ran out first:
// the send carries on in the background, onAck (may be nil) runs if and
// when the ack arrives, and a later failure is logged. A send that settled
// by the time the wait ran out is answered with its outcome, except one
// isKept (may be nil) says outlives its launch in the agent's outbox: that
// is answered as still queued.
func (s *Server) bridgeAwait(target bridge.Target, what string, send func(context.Context) error, isKept func(error) bool, wait time.Duration, onAck func()) (accepted bool, err error) {
	out := &bridgeSendOutcome{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		ctx, cancel := context.WithTimeout(context.Background(), bridgeBackgroundWait)
		defer cancel()
		err := send(ctx)
		kept := err != nil && isKept != nil && isKept(err)
		out.mu.Lock()
		out.settled, out.err, out.isKept = true, err, kept
		abandoned := out.abandoned
		out.mu.Unlock()
		if err == nil && onAck != nil {
			onAck()
		}
		switch {
		case kept:
			log.Printf("web: %s via leo bridge %s: its launch ended before the mod took it; it waits in the agent's outbox for the next one", strconv.Quote(what), strconv.Quote(target.Key))
		case err != nil && abandoned:
			log.Printf("web: %s via leo bridge %s failed after it was queued: %s", strconv.Quote(what), strconv.Quote(target.Key), strconv.Quote(err.Error()))
		}
	}()
	timeout, stop := bridgeSendTimer(wait)
	defer stop()
	select {
	case <-done:
	case <-timeout:
	}
	out.mu.Lock()
	defer out.mu.Unlock()
	if !out.settled {
		out.abandoned = true
		return false, nil
	}
	if out.isKept {
		return false, nil
	}
	return out.err == nil, out.err
}

// keptForNextLaunch reports whether err, how a durable deliver settled,
// left it in its agent's outbox for the next launch: its generation ended
// first, or the daemon's hub closed.
func keptForNextLaunch(err error) bool {
	return errors.Is(err, bridge.ErrForgotten) || errors.Is(err, bridge.ErrClosed)
}

// agentMessageCommand is the deliver a message to an agent becomes: from
// another agent, a non-user message framed with the sender (replacing the
// "[message from x]" wire prefix leotools.MessagePrefixFormat put there);
// from a human (no sender), the user's own prompt, verbatim.
func agentMessageCommand(from, text string) bridge.Command {
	if from == "" {
		return bridge.Deliver(text, true)
	}
	body := strings.TrimPrefix(text, fmt.Sprintf(leotools.MessagePrefixFormat, from, ""))
	return bridge.Deliver(bridge.Framed("agent "+from, body), false)
}

// deliverAgentMessageOverBridge sends a message to agent name's live bridge:
// 200 once accepted, 202 while still queued behind a running turn or
// (durably) for the agent's next launch, 500 on rejection, a full outbox or
// a lost bridge — never a tmux retry, which could deliver it twice.
func (s *Server) deliverAgentMessageOverBridge(target bridge.Target, name, from, text string) controlOutcome {
	ticket, err := s.bridgeRouter.Deliver(name, target, agentMessageCommand(from, text), from)
	if err != nil {
		return controlFailed(http.StatusInternalServerError, transportBridge, "delivering message: %v", err)
	}
	hub := s.bridgeRouter.Hub
	send := func(ctx context.Context) error { return hub.Await(ctx, ticket) }
	var isKept func(error) bool
	if s.bridgeRouter.IsDurable() {
		isKept = keptForNextLaunch
	}
	accepted, err := s.bridgeAwait(target, "message to "+name, send, isKept, bridgeMessageWait, func() {
		s.publishAgentMessage(from, name)
	})
	switch {
	case err != nil:
		return controlFailed(http.StatusInternalServerError, transportBridge, "delivering message: %v", err)
	case !accepted:
		return controlOutcome{status: http.StatusAccepted, transport: transportBridge, bridgeQueued: true}
	default:
		return controlOK(transportBridge)
	}
}
