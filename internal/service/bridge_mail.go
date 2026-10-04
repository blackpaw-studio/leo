package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/bridge"
	"github.com/blackpaw-studio/leo/internal/outbox"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

// pasteMessage delivers a message to a claude without the bridge: a
// readiness-probed tmux paste into its session. A package var so tests can
// observe it.
var pasteMessage = tmux.InjectPrompt

// noticeDeliverTimeout bounds a notice's paste into a running agent that
// has no bridge (a cold claude can take a minute to accept input).
const noticeDeliverTimeout = 3 * time.Minute

// noticePreviewRunes is how much of an undelivered message a notice quotes.
const noticePreviewRunes = 200

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
type agentMail struct {
	mu    sync.Mutex
	store *outbox.Store
}

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
	go s.settleMail(id, ticket, false)
	return ticket, nil
}

// settleMail takes ticket's message out of id's outbox once the mod acks
// it, ok or refused. One forgotten with its launch, or closed with the
// daemon, stays for the next launch. A refusal nobody waits for (a
// carried message) is logged.
func (s *Supervisor) settleMail(id *procIdentity, ticket *bridge.Ticket, carried bool) {
	<-ticket.Done()
	err := ticket.Err()
	if err != nil && !errors.Is(err, bridge.ErrRejected) {
		return
	}
	if err != nil && carried {
		fmt.Fprintf(os.Stderr, "[%s] the leo bridge refused carried message %s: %v\n", id.Name(), ticket.ID, err)
	}
	s.mail.mu.Lock()
	defer s.mail.mu.Unlock()
	if store := s.mail.store; store != nil {
		s.forgetMailLocked(store, id.Name(), ticket.ID)
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
// its opening, before anything can be routed to it. One the hub refuses
// stays, with all after it, for a later launch.
func (s *Supervisor) carryMail(hub *bridge.Hub, id *procIdentity, target bridge.Target) int {
	s.mail.mu.Lock()
	defer s.mail.mu.Unlock()
	store := s.mail.store
	if store == nil {
		return 0
	}
	name := id.Name()
	entries, err := store.List(name)
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
		go s.settleMail(id, ticket, true)
		carried++
	}
	if carried > 0 {
		fmt.Fprintf(os.Stderr, "[%s] carried %d undelivered message(s) over to this launch\n", name, carried)
	}
	return carried
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
		s.mail.mu.Unlock()
		if store == nil {
			return
		}
		entries, err := store.List(id.Name())
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

// DropAgentMail forgets what agent name never took, for it was deleted.
// Every agent that sent some is told, where it can be reached: at once if
// it is running, else in its own outbox for its next launch. Messages from
// a human or a task are only logged.
func (s *Supervisor) DropAgentMail(name string) {
	s.mail.mu.Lock()
	store := s.mail.store
	var (
		dropped []outbox.Entry
		err     error
	)
	if store != nil {
		dropped, err = store.Drop(name)
	}
	s.mail.mu.Unlock()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[%s] warning: dropping undelivered messages: %v\n", name, err)
		return
	}
	if len(dropped) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "[%s] dropped %d undelivered message(s): the agent was deleted\n", name, len(dropped))
	for _, n := range undeliveredNotices(name, dropped) {
		s.notifyAgent(n.to, n.text)
	}
}

type notice struct{ to, text string }

// undeliveredNotices words, per sending agent in the order they first
// sent, what it is told about its messages to deleted agent name.
func undeliveredNotices(name string, dropped []outbox.Entry) []notice {
	var (
		order []string
		bySrc = map[string][]outbox.Entry{}
	)
	for _, e := range dropped {
		if e.From == "" {
			continue
		}
		if _, seen := bySrc[e.From]; !seen {
			order = append(order, e.From)
		}
		bySrc[e.From] = append(bySrc[e.From], e)
	}
	notices := make([]notice, 0, len(order))
	for _, from := range order {
		sent := bySrc[from]
		text := fmt.Sprintf("leo: %d message(s) you sent to agent %s were never delivered: %s was deleted before it took them. The first began: %q",
			len(sent), name, name, preview(sent[0].Text))
		notices = append(notices, notice{to: from, text: text})
	}
	return notices
}

func preview(text string) string {
	if utf8.RuneCountInString(text) <= noticePreviewRunes {
		return text
	}
	return string([]rune(text)[:noticePreviewRunes]) + "…"
}

// notifyAgent tells agent name text where it can be reached: over its live
// bridge (durably), by paste into a running agent without one, or, for a
// stopped agent, in its outbox for its next launch. An agent leo does not
// know is only logged.
func (s *Supervisor) notifyAgent(name, text string) {
	cmd := bridge.Deliver(text, false)
	if router := s.BridgeRouter(); router != nil {
		if target, ok := router.Route(name); ok {
			if _, err := s.QueueDeliver(name, target, cmd, ""); err == nil {
				return
			}
		}
	}
	s.mu.RLock()
	id, live := s.identities[name]
	parent := s.ctx
	s.mu.RUnlock()
	if live {
		if parent == nil {
			parent = context.Background()
		}
		go func() {
			ctx, cancel := context.WithTimeout(parent, noticeDeliverTimeout)
			defer cancel()
			if err := pasteMessage(ctx, s.tmuxPath, id.SessionName(), text); err != nil {
				fmt.Fprintf(os.Stderr, "[%s] could not deliver a leo notice (%v): %s\n", name, err, text)
			}
		}()
		return
	}
	if !s.agentRecorded(name) {
		fmt.Fprintf(os.Stderr, "[%s] no such agent to tell: %s\n", name, text)
		return
	}
	s.mail.mu.Lock()
	defer s.mail.mu.Unlock()
	if s.mail.store == nil {
		return
	}
	entry := outbox.Entry{ID: bridge.NewCommandID(), Text: text, QueuedAt: time.Now().UTC()}
	if err := s.mail.store.Append(name, entry); err != nil {
		fmt.Fprintf(os.Stderr, "[%s] could not keep a leo notice for its next launch (%v): %s\n", name, err, text)
	}
}

// agentRecorded reports whether name has an agent record.
func (s *Supervisor) agentRecorded(name string) bool {
	recs, err := agentstore.Load(agentstore.FilePath(s.homePath))
	if err != nil {
		return false
	}
	_, ok := recs[name]
	return ok
}
