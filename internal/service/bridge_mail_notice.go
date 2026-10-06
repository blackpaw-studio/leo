package service

import (
	"context"
	"fmt"
	"os"
	"time"
	"unicode/utf8"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/bridge"
	"github.com/blackpaw-studio/leo/internal/outbox"
)

// noticeDeliverTimeout bounds a notice's paste into a running agent that
// has no bridge (a cold claude can take a minute to accept input).
const noticeDeliverTimeout = 3 * time.Minute

// noticePreviewRunes is how much of an undelivered message a notice quotes.
const noticePreviewRunes = 200

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
	dropped = s.untakenLocked(name, dropped)
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

// untakenLocked is dropped (agent name's former outbox) less what its
// claude took: those whose ack its settle goroutine has yet to apply.
func (s *Supervisor) untakenLocked(name string, dropped []outbox.Entry) []outbox.Entry {
	var untaken []outbox.Entry
	for _, e := range dropped {
		if got, _ := s.applyAckLocked(name, e.ID); got != settledTaken {
			untaken = append(untaken, e)
		}
	}
	return untaken
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
