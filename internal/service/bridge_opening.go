package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/bridge"
	"github.com/blackpaw-studio/leo/internal/harness"
)

// conversationArg returns the session args select: --session-id's value
// (fresh: a spawn or reset starting a new conversation) or --resume's.
func conversationArg(args []string) (id string, fresh bool) {
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "--session-id":
			return args[i+1], true
		case "--resume":
			return args[i+1], false
		}
	}
	return "", false
}

// openingDelivery tracks one supervise loop's claude opening brief across
// its launches. Over the bridge, the opening counts as delivered only once
// the mod acks it; the ack is recorded on the agent so a restored or adopted
// agent never gets it twice. A legacy launch carries it on argv every time,
// as it always has, except once a paste delivered an oversized one.
type openingDelivery struct {
	briefPath    string
	conversation string
	// record writes the agent's acked opening id ("" clears it); nil when
	// the process has no agent record.
	record func(id string)
	acked  atomic.Bool
	pasted atomic.Bool
}

// newOpeningDelivery sets up spec's opening. A launch that starts a fresh
// conversation has not run its opening yet, so any ack its record holds is
// an earlier conversation's and is cleared; a resumed one has run it if its
// record says the mod acked it.
func newOpeningDelivery(homePath string, spec ProcessSpec, id *procIdentity) *openingDelivery {
	conversation, fresh := conversationArg(spec.ClaudeArgs)
	if conversation == "" {
		conversation = spec.Name
	}
	o := &openingDelivery{briefPath: spec.OpeningBriefPath, conversation: conversation}
	if spec.OpeningBriefPath == "" || spec.Kind != harness.KindAgent {
		return o
	}
	o.record = func(ackedID string) {
		if err := agentstore.SetOpeningAcked(homePath, id.Name(), ackedID); err != nil {
			fmt.Fprintf(os.Stderr, "[%s] warning: recording the opening prompt's delivery: %v\n", id.Name(), err)
		}
	}
	if fresh {
		o.record("")
		return o
	}
	if recs, err := agentstore.Load(agentstore.FilePath(homePath)); err == nil && recs[spec.Name].OpeningAckedID != "" {
		o.acked.Store(true)
	}
	return o
}

// done reports whether no launch may carry the opening again.
func (o *openingDelivery) done() bool { return o.acked.Load() || o.pasted.Load() }

// pending reports whether the opening still has to be delivered.
func (o *openingDelivery) pending() bool { return o.briefPath != "" && !o.done() }

// command reads the brief and returns its deliver, ok=false when there is
// nothing to deliver over the bridge: an empty brief, or one that cannot be
// read (it then rides argv, as a legacy launch's would).
func (o *openingDelivery) command(name string) (bridge.Command, bool) {
	text, err := os.ReadFile(o.briefPath)
	switch {
	case err != nil:
		fmt.Fprintf(os.Stderr, "[%s] warning: reading the opening brief for the bridge: %v; it rides argv\n", name, err)
		return bridge.Command{}, false
	case len(text) == 0:
		return bridge.Command{}, false
	}
	return bridge.Opening(o.conversation, string(text)), true
}

// markAcked records that the mod acked the opening under id.
func (o *openingDelivery) markAcked(id string) {
	if o.acked.Swap(true) || o.record == nil {
		return
	}
	o.record(id)
}

// settle takes a settled ticket's outcome: an ack marks the opening
// delivered. Called once a launch is over, after its generation was
// forgotten, so the ticket has settled one way or the other.
func (o *openingDelivery) settle(t *bridge.Ticket) {
	if t == nil {
		return
	}
	select {
	case <-t.Done():
		if t.Err() == nil {
			o.markAcked(t.ID)
		}
	default:
	}
}

// queueOpening queues bl's opening on the bridge ahead of the launch, so
// nothing sent to the agent can overtake it, and returns bl carrying it.
// ok is false when the hub refused it; bl's generation is then forgotten
// and the caller treats the launch as legacy.
func (s *Supervisor) queueOpening(id *procIdentity, bl bridgeLaunch, opening *openingDelivery) (bridgeLaunch, bool) {
	cmd, has := opening.command(id.Name())
	if !has {
		return bl, true
	}
	w := s.bridgeWiring()
	ticket, err := w.hub.EnqueueTo(bl.target, cmd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[%s] queueing the opening prompt on the bridge: %v; launching without the bridge\n", id.Name(), err)
		w.hub.ForgetGen(bl.target)
		id.setLegacy()
		return bridgeLaunch{}, false
	}
	bl.opening, bl.ticket = cmd.Text, ticket
	return bl, true
}

// trackOpeningAck waits for the mod's verdict on bl's opening. An ack marks
// it delivered. A refusal means it never ran: the launch is abandoned for a
// legacy relaunch that carries it on argv. ctx ends with the launch.
func (s *Supervisor) trackOpeningAck(ctx context.Context, id *procIdentity, bl bridgeLaunch, opening *openingDelivery, tmuxPath string, fellBack *atomic.Bool) {
	err := bl.ticket.Wait(ctx)
	if ctx.Err() != nil {
		return // the launch is over; its end settles the ticket
	}
	switch {
	case err == nil:
		opening.markAcked(bl.ticket.ID)
	case errors.Is(err, bridge.ErrRejected):
		fmt.Fprintf(os.Stderr, "[%s] the leo bridge refused the opening prompt (%v); relaunching without it\n", id.Name(), err)
		s.fallBackFromBridge(id, bl.target, tmuxPath, fellBack, true)
	}
}
