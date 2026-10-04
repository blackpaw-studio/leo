package service

import (
	"context"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/observe"
	"github.com/blackpaw-studio/leo/internal/outbox"
)

// holdSettles keeps sv's settle goroutines from applying any ack until the
// test ends, as a scheduler that has not run them yet would.
func holdSettles(t *testing.T, sv *Supervisor) {
	t.Helper()
	release := make(chan struct{})
	sv.mail.mu.Lock()
	sv.mail.beforeSettle = func() { <-release }
	sv.mail.mu.Unlock()
	t.Cleanup(func() { close(release) })
}

// agentMessages are the agent-to-agent message announcements among events.
func agentMessages(events []observe.Event) []observe.AgentMessagePayload {
	var out []observe.AgentMessagePayload
	for _, ev := range events {
		if p, ok := ev.Payload.(*observe.AgentMessagePayload); ok && ev.Type == observe.EventAgentMessage {
			out = append(out, *p)
		}
	}
	return out
}

// A message acked just before its launch ended was delivered, even if its
// settle has not yet cleared it from the outbox when the next launch opens:
// that launch applies the ack itself rather than carry the message again.
func TestAMessageAckedAsItsLaunchEndsIsNotCarried(t *testing.T) {
	tmuxPath, logPath := statefulTmux(t, "")
	home := t.TempDir()
	f := startBridged(t, tmuxPath, "2.1.289", time.Minute, claudeSpec(t, "alpha"), withHome(home), withBackoff(time.Millisecond), durable)
	waitForNewSessions(t, logPath, 1)
	s, launch := connectMod(t, f.hub, tmuxPath, "alpha")
	holdSettles(t, f.sv)
	queueMessage(t, f, "alpha", "hello", "beta")
	ackCmd(t, f.hub, "alpha", launch, nextCmd(t, s), true)
	endSession(t, tmuxPath)

	waitForNewSessions(t, logPath, 2)
	s2, _ := connectMod(t, f.hub, tmuxPath, "alpha")
	if handed := drainCmds(s2); len(handed) != 0 {
		t.Fatalf("the relaunch was handed %+v, which its predecessor took", handed)
	}
	if got := mailOf(t, home, "alpha"); len(got) != 0 {
		t.Fatalf("the outbox kept %+v", got)
	}
}

// Likewise a legacy launch's paste skips what the bridge took.
func TestAPasteSkipsAMessageTheBridgeTook(t *testing.T) {
	pastes := recordPastes(t)
	tmuxPath, logPath := statefulTmux(t, "")
	home := t.TempDir()
	f := startBridged(t, tmuxPath, "2.1.289", time.Minute, claudeSpec(t, "alpha"), withHome(home), durable)
	waitForNewSessions(t, logPath, 1)
	s, launch := connectMod(t, f.hub, tmuxPath, "alpha")
	holdSettles(t, f.sv)
	queueMessage(t, f, "alpha", "hello", "beta")
	ackCmd(t, f.hub, "alpha", launch, nextCmd(t, s), true)

	f.sv.mu.RLock()
	id := f.sv.identities["alpha"]
	f.sv.mu.RUnlock()
	f.sv.pasteMail(context.Background(), id, tmuxPath, nil)()

	if got := pastes(); len(got) != 0 {
		t.Fatalf("pasted %q, which the bridge took", got)
	}
	if got := mailOf(t, home, "alpha"); len(got) != 0 {
		t.Fatalf("the outbox kept %+v", got)
	}
}

// Nor is a sender told a deleted agent never took a message it did take.
func TestADeletionTellsNoOneOfAMessageTaken(t *testing.T) {
	tmuxPath, logPath := statefulTmux(t, "")
	home := t.TempDir()
	agentRecord(t, home, agentstore.Record{Name: "beta"})
	f := startBridged(t, tmuxPath, "2.1.289", time.Minute, claudeSpec(t, "alpha"), withHome(home), durable)
	waitForNewSessions(t, logPath, 1)
	s, launch := connectMod(t, f.hub, tmuxPath, "alpha")
	holdSettles(t, f.sv)
	queueMessage(t, f, "alpha", "hello", "beta")
	ackCmd(t, f.hub, "alpha", launch, nextCmd(t, s), true)

	f.sv.DropAgentMail("alpha")

	if got := mailOf(t, home, "beta"); len(got) != 0 {
		t.Fatalf("beta was told %+v", got)
	}
}

// A carried message another agent sent is announced when its claude takes
// it, as one taken in the launch it was sent to is (by the web handler,
// whose request was answered "queued" long before; so that one is not
// announced here again). One from a human or a task is not: the outbox
// cannot tell a human's message from a task prompt.
func TestACarriedMessageIsAnnouncedWhenTaken(t *testing.T) {
	tmuxPath, logPath := statefulTmux(t, "")
	home := t.TempDir()
	seedMail(t, home, "alpha",
		outbox.Entry{ID: "c-1", Text: "from beta", From: "beta"},
		outbox.Entry{ID: "c-2", Text: "from a human"})
	f := startBridged(t, tmuxPath, "2.1.289", time.Minute, claudeSpec(t, "alpha"), withHome(home), durable)
	pub := &recordingPublisher{}
	f.sv.SetPublisher(pub)
	waitForNewSessions(t, logPath, 1)
	s, launch := connectMod(t, f.hub, tmuxPath, "alpha")
	ackCmd(t, f.hub, "alpha", launch, nextCmd(t, s), true)
	ackCmd(t, f.hub, "alpha", launch, nextCmd(t, s), true)
	queueMessage(t, f, "alpha", "from gamma, this launch", "gamma")
	ackCmd(t, f.hub, "alpha", launch, nextCmd(t, s), true)
	waitFor(t, "the messages to leave the outbox", func() bool { return len(mailOf(t, home, "alpha")) == 0 })

	waitFor(t, "the announcement", func() bool { return len(agentMessages(pub.Events())) > 0 })
	time.Sleep(20 * time.Millisecond)
	if got := agentMessages(pub.Events()); len(got) != 1 || got[0].From != "beta" || got[0].To != "alpha" {
		t.Fatalf("announced %+v, want beta → alpha once", got)
	}
}
