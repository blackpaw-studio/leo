package web

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
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

// bridgeRoute returns agent name's bridge key if its mod is connected now.
// An agent launched with the bridge whose mod is not connected is logged:
// the caller falls back to tmux.
func (s *Server) bridgeRoute(name, what string) (string, bool) {
	if key, ok := s.bridgeRouter.Route(name); ok {
		return key, true
	}
	if key, known := s.bridgeRouter.Key(name); known {
		log.Printf("web: %s for %s: leo bridge %s not connected; falling back to tmux", what, strconv.Quote(name), strconv.Quote(key))
	}
	return "", false
}

// bridgeSend sends cmd to key and waits up to wait for the mod's ack.
// accepted is false when the wait ran out first: the send carries on in the
// background, onAck (may be nil) runs if and when the ack arrives, and a
// later failure is logged.
func (s *Server) bridgeSend(key, what string, cmd bridge.Command, wait time.Duration, onAck func()) (accepted bool, err error) {
	hub := s.bridgeRouter.Hub
	result := make(chan error, 1)
	var abandoned atomic.Bool
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), bridgeBackgroundWait)
		defer cancel()
		err := hub.Send(ctx, key, cmd)
		if err == nil && onAck != nil {
			onAck()
		}
		if err != nil && abandoned.Load() {
			log.Printf("web: %s via leo bridge %s failed after it was queued: %s", what, strconv.Quote(key), strconv.Quote(err.Error()))
		}
		result <- err
	}()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case err := <-result:
		return err == nil, err
	case <-timer.C:
		abandoned.Store(true)
		return false, nil
	}
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

// deliverAgentMessageOverBridge sends a message to agent name's live bridge
// and writes the response: 200 once accepted, 202 while still queued
// behind a running turn, 500 on rejection or a lost bridge — never a tmux
// retry, which could deliver it twice.
func (s *Server) deliverAgentMessageOverBridge(w http.ResponseWriter, key, name, from, text string) {
	accepted, err := s.bridgeSend(key, "message to "+name, agentMessageCommand(from, text), bridgeMessageWait, func() {
		s.publishAgentMessage(from, name)
	})
	switch {
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: fmt.Sprintf("delivering message: %v", err)})
	case !accepted:
		writeJSON(w, http.StatusAccepted, apiResponse{OK: true, Data: map[string]bool{"queued": true}})
	default:
		writeJSON(w, http.StatusOK, apiResponse{OK: true})
	}
}
