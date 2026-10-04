package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
	"github.com/blackpaw-studio/leo/internal/observe"
	"github.com/blackpaw-studio/leo/internal/outbox"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

// pasteMessage delivers a message to a claude without the bridge: a
// readiness-probed tmux paste into its session. A package var so tests can
// observe it.
var pasteMessage = tmux.InjectPrompt

// agentMail is the durable side of the delivers leo queues for agents'
// claudes. Each goes into its agent's outbox before the hub, leaves it on
// the mod's ack (ok, or refused: its sender heard, or it is logged), and
// otherwise outlives the launch, and the daemon, it was queued for: the
// agent's next launch carries it over, over the bridge under its own id
// (the mod's dedup turns a repeat into a re-ack) or by paste to a legacy
// launch. mu serializes a deliver's persist-then-queue against carry-overs
// and renames, so each message is in exactly one place: queued on the live
// generation, or waiting in the outbox for the next one. Lock order:
// Supervisor.mu, then mu, then a procIdentity's.
//
// A message's ack is applied (the message leaves the outbox, or stays) once,
// by whichever comes first: its settle goroutine, or the next launch's
// carry or paste, or a deletion, finding its ticket settled. So a message
// acked just as its launch ended is never carried, pasted or reported lost
// for want of a goroutine that has yet to run.
type agentMail struct {
	mu    sync.Mutex
	store *outbox.Store
	// queued holds, by message id, each queued message's ticket on the hub
	// until its ack is applied.
	queued map[string]queuedMail
	// beforeSettle, if set, runs as a settle goroutine wakes, before it
	// applies its ack: a test seam.
	beforeSettle func()
}

// queuedMail is a message on the hub, awaiting its ack.
type queuedMail struct {
	ticket *bridge.Ticket
	// from is the agent that sent it ("" for a human or a task).
	from string
	// carried: an earlier launch's, queued again for this one; nobody
	// waits on its ticket.
	carried bool
}

// settlement is how applying a message's ack went.
type settlement int

const (
	// settledPending: the message is not queued, or not acked yet.
	settledPending settlement = iota
	// settledTaken: acked, ok or refused; it left the outbox.
	settledTaken
	// settledKept: its launch ended first; it stays for the next one.
	settledKept
)

// SetOutbox keeps agent delivers in store until their claude takes them;
// nil keeps them in the hub alone, lost with their launch.
func (s *Supervisor) SetOutbox(store *outbox.Store) {
	s.mail.mu.Lock()
	defer s.mail.mu.Unlock()
	s.mail.store = store
}

// QueueDeliver queues cmd, a deliver from agent from ("" for a human or a
// task), for agent name's live generation target: in name's outbox first,
// then on the hub. The returned ticket settles with the mod's ack; one that
// settles as forgotten or closed left the message in the outbox for name's
// next launch. A full outbox is outbox.ErrFull.
func (s *Supervisor) QueueDeliver(name string, target bridge.Target, cmd bridge.Command, from string) (*bridge.Ticket, error) {
	w := s.bridgeWiring()
	if w == nil {
		return nil, bridge.ErrClosed
	}
	s.mu.RLock()
	id, ok := s.identities[name]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("agent %s is not running", name)
	}
	return s.queueDeliver(w.hub, id, target, cmd, from)
}

// QueueDeliverForSession is QueueDeliver for the agent running in tmux
// session.
func (s *Supervisor) QueueDeliverForSession(session string, target bridge.Target, cmd bridge.Command) (*bridge.Ticket, error) {
	w := s.bridgeWiring()
	if w == nil {
		return nil, bridge.ErrClosed
	}
	s.mu.RLock()
	var id *procIdentity
	for _, candidate := range s.identities {
		if candidate.SessionName() == session {
			id = candidate
			break
		}
	}
	s.mu.RUnlock()
	if id == nil {
		return nil, fmt.Errorf("no agent runs in tmux session %s", session)
	}
	return s.queueDeliver(w.hub, id, target, cmd, "")
}

func (s *Supervisor) queueDeliver(hub *bridge.Hub, id *procIdentity, target bridge.Target, cmd bridge.Command, from string) (*bridge.Ticket, error) {
	if cmd.Op != bridge.OpDeliver {
		return nil, fmt.Errorf("%w: only a deliver is kept until delivered, not %q", bridge.ErrInvalidCommand, cmd.Op)
	}
	if cmd.ID == "" {
		cmd.ID = bridge.NewCommandID()
	}
	s.mail.mu.Lock()
	defer s.mail.mu.Unlock()
	store := s.mail.store
	if store == nil {
		return hub.EnqueueTo(target, cmd)
	}
	name := id.Name()
	entry := outbox.Entry{ID: cmd.ID, Text: cmd.Text, AsUser: cmd.AsUser, From: from, QueuedAt: time.Now().UTC()}
	if err := store.Append(name, entry); err != nil {
		return nil, fmt.Errorf("queueing a message for %s: %w", name, err)
	}
	ticket, err := hub.EnqueueTo(target, cmd)
	if err != nil {
		s.forgetMailLocked(store, name, cmd.ID)
		return nil, err
	}
	s.trackMailLocked(id, queuedMail{ticket: ticket, from: from})
	return ticket, nil
}

// trackMailLocked records q as its message's live ticket, superseding any
// earlier launch's, and starts the goroutine that applies its ack.
func (s *Supervisor) trackMailLocked(id *procIdentity, q queuedMail) {
	if s.mail.queued == nil {
		s.mail.queued = map[string]queuedMail{}
	}
	s.mail.queued[q.ticket.ID] = q
	go s.settleMail(id, q.ticket)
}

// settleMail applies ticket's ack once it settles (see applyAckLocked),
// unless a carry, paste or deletion applied it first.
func (s *Supervisor) settleMail(id *procIdentity, ticket *bridge.Ticket) {
	<-ticket.Done()
	s.mail.mu.Lock()
	hook := s.mail.beforeSettle
	s.mail.mu.Unlock()
	if hook != nil {
		hook()
	}
	s.mail.mu.Lock()
	_, announce := s.applyAckLocked(id.Name(), ticket.ID)
	s.mail.mu.Unlock()
	s.announce(announce)
}

// applyAckLocked applies the ack of agent name's message msgID if its
// ticket has settled, once: acked ok or refused, it leaves the outbox (a
// refusal nobody waits for, a carried message's, is logged); forgotten with
// its launch, or closed with the daemon, it stays for the next launch. The
// returned announcement (nil for none) is the message's, for a carried
// message another agent sent and claude took: publish it once unlocked.
func (s *Supervisor) applyAckLocked(name, msgID string) (settlement, *observe.Event) {
	q, ok := s.mail.queued[msgID]
	if !ok {
		return settledPending, nil
	}
	select {
	case <-q.ticket.Done():
	default:
		return settledPending, nil
	}
	delete(s.mail.queued, msgID)
	err := q.ticket.Err()
	if err != nil && !errors.Is(err, bridge.ErrRejected) {
		return settledKept, nil
	}
	if err != nil && q.carried {
		fmt.Fprintf(os.Stderr, "[%s] the leo bridge refused carried message %s: %v\n", name, msgID, err)
	}
	if store := s.mail.store; store != nil {
		s.forgetMailLocked(store, name, msgID)
	}
	if err != nil || !q.carried || q.from == "" {
		return settledTaken, nil
	}
	return settledTaken, &observe.Event{Type: observe.EventAgentMessage, Payload: &observe.AgentMessagePayload{From: q.from, To: name}}
}

// announce publishes ev, if any.
func (s *Supervisor) announce(ev *observe.Event) {
	if ev != nil {
		s.publish(*ev)
	}
}

func (s *Supervisor) forgetMailLocked(store *outbox.Store, name, msgID string) {
	if err := store.Remove(name, msgID); err != nil {
		fmt.Fprintf(os.Stderr, "[%s] warning: clearing delivered message %s from the outbox (a later launch may deliver it again): %v\n", name, msgID, err)
	}
}

// carryMail queues on target, the generation id's launch just opened, every
// message id's agent has waiting, in the order they were queued and under
// their own ids, and returns how many. It runs as the launch opens: behind
// its opening, before anything can be routed to it. The generation holds
// nothing else yet and its opening takes no slot, so an outbox within the
// hub's cap (see wireBridge) fits whole. One the hub refuses anyway (the
// generation already ended) stays, with all after it, for a later launch.
func (s *Supervisor) carryMail(hub *bridge.Hub, id *procIdentity, target bridge.Target) int {
	s.mail.mu.Lock()
	defer s.mail.mu.Unlock()
	store := s.mail.store
	if store == nil {
		return 0
	}
	name := id.Name()
	entries, announcements, err := s.undeliveredLocked(name)
	defer func() {
		for _, ev := range announcements {
			go s.announce(ev)
		}
	}()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[%s] warning: reading undelivered messages: %v\n", name, err)
		return 0
	}
	carried := 0
	for _, e := range entries {
		ticket, err := hub.EnqueueTo(target, bridge.Command{ID: e.ID, Op: bridge.OpDeliver, Text: e.Text, AsUser: e.AsUser})
		if err != nil {
			fmt.Fprintf(os.Stderr, "[%s] warning: carrying undelivered message %s over: %v; it and %d after it wait for the next launch\n", name, e.ID, err, len(entries)-carried-1)
			break
		}
		s.trackMailLocked(id, queuedMail{ticket: ticket, from: e.From, carried: true})
		carried++
	}
	if carried > 0 {
		fmt.Fprintf(os.Stderr, "[%s] carried %d undelivered message(s) over to this launch\n", name, carried)
	}
	return carried
}

// undeliveredLocked lists what agent name still has to be delivered: its
// outbox, less what an earlier launch's claude took (its ack applied here
// if its settle goroutine has not run yet; see agentMail), up to the first
// message still queued on a live launch, which stays, with all after it,
// in order. That cannot happen to a launch's carry or paste: every launch
// before it is over, its generation forgotten and its tickets settled. The
// announcements of what was found taken are returned for publishing once
// unlocked.
func (s *Supervisor) undeliveredLocked(name string) ([]outbox.Entry, []*observe.Event, error) {
	entries, err := s.mail.store.List(name)
	if err != nil {
		return nil, nil, err
	}
	var (
		pending       []outbox.Entry
		announcements []*observe.Event
	)
	for i, e := range entries {
		got, ev := s.applyAckLocked(name, e.ID)
		if ev != nil {
			announcements = append(announcements, ev)
		}
		switch {
		case got == settledTaken:
			continue
		case got == settledPending && s.isQueuedLocked(e.ID):
			fmt.Fprintf(os.Stderr, "[%s] warning: undelivered message %s is still queued on an earlier launch; it and %d after it wait\n", name, e.ID, len(entries)-i-1)
			return pending, announcements, nil
		}
		pending = append(pending, e)
	}
	return pending, announcements, nil
}

// isQueuedLocked reports whether message msgID has a ticket awaiting its
// ack.
func (s *Supervisor) isQueuedLocked(msgID string) bool {
	_, ok := s.mail.queued[msgID]
	return ok
}

// pasteMail delivers, without the bridge, every message id's agent has
// waiting: first runs first (a legacy launch's oversized opening, which
// they wait behind; nil for none), then each message is pasted into the
// agent's session in order, and taken out of the outbox once pasted. It
// stops when ctx (the launch) ends or a paste fails, leaving the rest for
// the next launch. The returned settle waits for it to stop, so the next
// launch never carries what this one pasted.
func (s *Supervisor) pasteMail(ctx context.Context, id *procIdentity, tmuxPath string, first func()) (settle func()) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		if first != nil {
			first()
		}
		s.mail.mu.Lock()
		store := s.mail.store
		var (
			entries       []outbox.Entry
			announcements []*observe.Event
			err           error
		)
		if store != nil {
			entries, announcements, err = s.undeliveredLocked(id.Name())
		}
		s.mail.mu.Unlock()
		for _, ev := range announcements {
			s.announce(ev)
		}
		if store == nil {
			return
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "[%s] warning: reading undelivered messages: %v\n", id.Name(), err)
			return
		}
		for i, e := range entries {
			if err := pasteMessage(ctx, tmuxPath, id.SessionName(), e.Text); err != nil {
				if ctx.Err() == nil {
					fmt.Fprintf(os.Stderr, "[%s] pasting undelivered message %s: %v; it and %d after it wait for the next launch\n", id.Name(), e.ID, err, len(entries)-i-1)
				}
				return
			}
			s.mail.mu.Lock()
			s.forgetMailLocked(store, id.Name(), e.ID)
			s.mail.mu.Unlock()
		}
		if len(entries) > 0 {
			fmt.Fprintf(os.Stderr, "[%s] pasted %d undelivered message(s) into the session\n", id.Name(), len(entries))
		}
	}()
	return func() { <-done }
}

// renameMailLocked moves oldName's waiting messages to newName. Caller
// holds s.mail.mu.
func (s *Supervisor) renameMailLocked(oldName, newName string) error {
	if s.mail.store == nil {
		return nil
	}
	return s.mail.store.Rename(oldName, newName)
}

// RenameAgentMail moves the waiting messages of an agent that is not
// running from oldName to newName: agent.Manager's rename of a stopped
// agent. A live agent's move with its rename (see RenameAgent).
func (s *Supervisor) RenameAgentMail(oldName, newName string) error {
	s.mail.mu.Lock()
	defer s.mail.mu.Unlock()
	return s.renameMailLocked(oldName, newName)
}
