package service

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/bridge"
	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
	"github.com/blackpaw-studio/leo/internal/outbox"
)

// mailStore is the outbox a daemon with leo home home keeps.
func mailStore(home string) *outbox.Store {
	return outbox.New(filepath.Join(home, "state", "outbox"), outbox.Options{})
}

func durable(o *bridgeTestOpts) { o.isDurable = true }

// mailOf lists name's undelivered messages as stored in home.
func mailOf(t *testing.T, home, name string) []outbox.Entry {
	t.Helper()
	entries, err := mailStore(home).List(name)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

// seedMail stores entries as name's undelivered messages in home, as a
// previous launch (or daemon) left them.
func seedMail(t *testing.T, home, name string, entries ...outbox.Entry) {
	t.Helper()
	for _, e := range entries {
		if err := mailStore(home).Append(name, e); err != nil {
			t.Fatal(err)
		}
	}
}

// recordPastes stubs pasteMessage for the test, returning what it pasted
// as "<session>|<text>".
func recordPastes(t *testing.T) func() []string {
	t.Helper()
	var (
		mu     sync.Mutex
		pasted []string
	)
	orig := pasteMessage
	pasteMessage = func(_ context.Context, _ string, session, text string) error {
		mu.Lock()
		defer mu.Unlock()
		pasted = append(pasted, session+"|"+text)
		return nil
	}
	t.Cleanup(func() { pasteMessage = orig })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), pasted...)
	}
}

// queueMessage queues text for name the way a message from agent from
// does: durably, on name's live generation.
func queueMessage(t *testing.T, f *bridgeFixture, name, text, from string) *bridge.Ticket {
	t.Helper()
	target, ok := f.sv.BridgeRouter().Route(name)
	if !ok {
		t.Fatalf("%s has no live bridge", name)
	}
	ticket, err := f.sv.QueueDeliver(name, target, bridge.Deliver(text, false), from)
	if err != nil {
		t.Fatalf("QueueDeliver: %v", err)
	}
	return ticket
}

// A queued message is in the agent's outbox until the mod acks it, and
// leaves on any ack: ok, or refused (its sender heard of the refusal).
func TestQueuedMessagesLeaveTheOutboxOnTheirAck(t *testing.T) {
	for name, ok := range map[string]bool{"acked": true, "refused": false} {
		t.Run(name, func(t *testing.T) {
			tmuxPath, logPath := statefulTmux(t, "")
			home := t.TempDir()
			f := startBridged(t, tmuxPath, "2.1.289", time.Minute, claudeSpec(t, "alpha"), withHome(home), durable)
			waitForNewSessions(t, logPath, 1)
			s, launch := connectMod(t, f.hub, tmuxPath, "alpha")
			ticket := queueMessage(t, f, "alpha", "hello", "beta")
			if got := mailOf(t, home, "alpha"); len(got) != 1 || got[0].ID != ticket.ID || got[0].From != "beta" || got[0].Text != "hello" {
				t.Fatalf("outbox before the ack: %+v", got)
			}
			ackCmd(t, f.hub, "alpha", launch, nextCmd(t, s), ok)
			waitFor(t, "the acked message to leave the outbox", func() bool { return len(mailOf(t, home, "alpha")) == 0 })
		})
	}
}

// Messages that never fit are refused to their sender, never dropped.
func TestAFullOutboxIsTheSendersError(t *testing.T) {
	tmuxPath, logPath := statefulTmux(t, "")
	home := t.TempDir()
	f := startBridged(t, tmuxPath, "2.1.289", time.Minute, claudeSpec(t, "alpha"), withHome(home))
	f.sv.SetOutbox(outbox.New(filepath.Join(home, "state", "outbox"), outbox.Options{MaxEntries: 1}))
	waitForNewSessions(t, logPath, 1)
	connectMod(t, f.hub, tmuxPath, "alpha")
	queueMessage(t, f, "alpha", "first", "")
	target, _ := f.sv.BridgeRouter().Route("alpha")
	if _, err := f.sv.QueueDeliver("alpha", target, bridge.Deliver("second", false), ""); !errors.Is(err, outbox.ErrFull) {
		t.Fatalf("err=%v, want outbox.ErrFull", err)
	}
	if got := f.hub.State("alpha").Pending; got != 1 {
		t.Fatalf("pending=%d: the refused message reached the hub", got)
	}
}

// A claude that dies with messages queued for it (it was busy) does not
// lose them: the relaunch carries them over, in order, under their ids.
func TestACrashRelaunchCarriesUndeliveredMessages(t *testing.T) {
	tmuxPath, logPath := statefulTmux(t, "")
	home := t.TempDir()
	f := startBridged(t, tmuxPath, "2.1.289", time.Minute, claudeSpec(t, "alpha"), withHome(home), withBackoff(time.Millisecond), durable)
	waitForNewSessions(t, logPath, 1)
	s, _ := connectMod(t, f.hub, tmuxPath, "alpha")
	first := queueMessage(t, f, "alpha", "first", "beta")
	second := queueMessage(t, f, "alpha", "second", "")
	nextCmd(t, s)
	endSession(t, tmuxPath)
	if err := first.Wait(context.Background()); !errors.Is(err, bridge.ErrForgotten) {
		t.Fatalf("the dead launch's message: err=%v, want ErrForgotten", err)
	}

	waitForNewSessions(t, logPath, 2)
	s2, launch2 := connectMod(t, f.hub, tmuxPath, "alpha")
	a, b := nextCmd(t, s2), nextCmd(t, s2)
	if a.ID != first.ID || a.Text != "first" || b.ID != second.ID || b.Text != "second" {
		t.Fatalf("the relaunch was handed %+v, %+v; want both messages in order under their ids", a, b)
	}
	ackCmd(t, f.hub, "alpha", launch2, a, true)
	ackCmd(t, f.hub, "alpha", launch2, b, true)
	waitFor(t, "the carried messages to leave the outbox", func() bool { return len(mailOf(t, home, "alpha")) == 0 })
}

// What an earlier launch left undelivered waits behind the opening the new
// launch still has to deliver, in the order it was queued.
func TestCarriedMessagesQueueBehindThePendingOpening(t *testing.T) {
	tmuxPath, logPath := statefulTmux(t, "")
	home := t.TempDir()
	seedMail(t, home, "alpha",
		outbox.Entry{ID: "c-1", Text: "first"},
		outbox.Entry{ID: "c-2", Text: "second", AsUser: true})
	spec := claudeSpec(t, "alpha")
	spec.OpeningBriefPath = writeBrief(t, "the opening")
	f := startBridged(t, tmuxPath, "2.1.289", time.Minute, spec, withHome(home), durable)
	waitForNewSessions(t, logPath, 1)

	s, launch := connectMod(t, f.hub, tmuxPath, "alpha")
	opening := nextCmd(t, s)
	if opening.Text != "the opening" {
		t.Fatalf("first out %+v, want the opening", opening)
	}
	if held := drainCmds(s); len(held) != 0 {
		t.Fatalf("handed %+v before the opening was acked", held)
	}
	ackCmd(t, f.hub, "alpha", launch, opening, true)
	a, b := nextCmd(t, s), nextCmd(t, s)
	if a.ID != "c-1" || a.Text != "first" || a.AsUser || b.ID != "c-2" || !b.AsUser {
		t.Fatalf("after the opening: %+v, %+v; want the carried messages in order", a, b)
	}
}

// The daemon dies with a message queued for a busy agent, already handed
// to its mod but unacked. The restarted daemon adopts the session and
// queues the message again under its id, so the mod (whose dedup outlives
// the daemon) runs it once.
func TestDaemonRestartRedeliversAQueuedMessageOnceUnderItsID(t *testing.T) {
	tmuxPath, logPath := statefulTmux(t, "")
	home := t.TempDir()
	agentRecord(t, home, agentstore.Record{Name: "alpha"})
	spec := claudeSpec(t, "alpha")
	before := startBridged(t, tmuxPath, "2.1.289", time.Minute, spec, withHome(home), durable)
	waitForNewSessions(t, logPath, 1)
	s, launch := connectMod(t, before.hub, tmuxPath, "alpha")
	queued := queueMessage(t, before, "alpha", "while you were busy", "beta")
	handed := []bridge.Command{nextCmd(t, s)}
	before.die()
	if got := mailOf(t, home, "alpha"); len(got) != 1 || got[0].ID != queued.ID {
		t.Fatalf("the outbox across the restart: %+v", got)
	}

	restored := spec
	restored.ClaudeArgs = []string{"--name", "alpha", "--resume", "s-1"}
	restored.Adopt = true
	after := startBridged(t, tmuxPath, "2.1.289", time.Minute, restored, withHome(home), durable)
	s2, launch2 := connectMod(t, after.hub, tmuxPath, "alpha")
	if launch2 != launch {
		t.Fatalf("reconnected as launch %q, want the adopted %q", launch2, launch)
	}
	again := nextCmd(t, s2)
	handed = append(handed, again)
	ackCmd(t, after.hub, "alpha", launch2, again, true)
	waitFor(t, "the message to leave the outbox", func() bool { return len(mailOf(t, home, "alpha")) == 0 })
	handed = append(handed, drainCmds(s2)...)
	if again.ID != queued.ID || runsOf(handed, "while you were busy") != 1 || len(handed) != 2 {
		t.Fatalf("handed %+v across the restart; want the message twice under one id, so it runs once", handed)
	}
}

// A mod that refuses the opening ran nothing behind it: the legacy
// relaunch pastes what waited there, once each and in order, and the
// outbox is left empty.
func TestARefusedOpeningCarriesWhatWaitedToTheLegacyRelaunch(t *testing.T) {
	pastes := recordPastes(t)
	tmuxPath, logPath := statefulTmux(t, "")
	home := t.TempDir()
	seedMail(t, home, "alpha",
		outbox.Entry{ID: "c-1", Text: "first"},
		outbox.Entry{ID: "c-2", Text: "second"})
	spec := claudeSpec(t, "alpha")
	spec.OpeningBriefPath = writeBrief(t, "the opening")
	f := startBridged(t, tmuxPath, "2.1.289", time.Minute, spec, withHome(home), durable)
	waitForNewSessions(t, logPath, 1)
	s, launch := connectMod(t, f.hub, tmuxPath, "alpha")
	ackCmd(t, f.hub, "alpha", launch, nextCmd(t, s), false)
	if held := drainCmds(s); len(held) != 0 {
		t.Fatalf("the refused launch was handed %+v", held)
	}

	lines := waitForNewSessions(t, logPath, 2)
	if strings.Contains(lines[1], "--plugin-dir") || !strings.Contains(lines[1], claudeharness.BriefArgvWord(spec.OpeningBriefPath)) {
		t.Fatalf("want a legacy relaunch with the opening on argv:\n%s", lines[1])
	}
	waitFor(t, "the carried messages to be pasted", func() bool { return len(mailOf(t, home, "alpha")) == 0 })
	if got := pastes(); strings.Join(got, ",") != "leo-alpha|first,leo-alpha|second" {
		t.Fatalf("pasted %q; want each carried message once, in order", got)
	}
}

// A launch with messages to carry whose mod never connects gets them no
// other way: like one with an opening, it is relaunched legacy, where they
// are pasted.
func TestCarriedMessagesWithoutAModRelaunchLegacy(t *testing.T) {
	pastes := recordPastes(t)
	tmuxPath, logPath := statefulTmux(t, "")
	home := t.TempDir()
	seedMail(t, home, "alpha", outbox.Entry{ID: "c-1", Text: "first"})
	_ = startBridged(t, tmuxPath, "2.1.289", 30*time.Millisecond, claudeSpec(t, "alpha"), withHome(home), durable)

	lines := waitForNewSessions(t, logPath, 2)
	if strings.Contains(lines[1], "--plugin-dir") {
		t.Fatalf("want a legacy relaunch:\n%s", lines[1])
	}
	waitFor(t, "the carried message to be pasted", func() bool { return len(mailOf(t, home, "alpha")) == 0 })
	if got := pastes(); len(got) != 1 || got[0] != "leo-alpha|first" {
		t.Fatalf("pasted %q, want the carried message once", got)
	}
}

// A message queued for an agent renamed while live stays its: the outbox
// moves with the name, and the ack clears it there.
func TestALiveRenameMovesTheOutbox(t *testing.T) {
	tmuxPath, logPath := statefulTmux(t, "")
	home := t.TempDir()
	f := startBridged(t, tmuxPath, "2.1.289", time.Minute, claudeSpec(t, "alpha"), withHome(home), durable)
	waitForNewSessions(t, logPath, 1)
	s, launch := connectMod(t, f.hub, tmuxPath, "alpha")
	queueMessage(t, f, "alpha", "hello", "")
	cmd := nextCmd(t, s)
	f.sv.mu.Lock()
	f.sv.states["alpha"].Status = "running"
	f.sv.mu.Unlock()
	if err := f.sv.RenameAgent("alpha", "gamma"); err != nil {
		t.Fatal(err)
	}
	if got := mailOf(t, home, "gamma"); len(got) != 1 || len(mailOf(t, home, "alpha")) != 0 {
		t.Fatalf("after the rename: gamma has %+v, alpha %+v", got, mailOf(t, home, "alpha"))
	}
	ackCmd(t, f.hub, "alpha", launch, cmd, true)
	waitFor(t, "the ack to clear the renamed outbox", func() bool { return len(mailOf(t, home, "gamma")) == 0 })
}

// Deleting an agent drops what it never took, and each agent that sent
// some is told: at once if it is running, else in its own outbox for its
// next launch. What a human or a task sent is only logged.
func TestAgentDeletionTellsTheSenders(t *testing.T) {
	pastes := recordPastes(t)
	home := t.TempDir()
	sv := NewSupervisor(context.Background())
	sv.homePath = home
	sv.SetOutbox(mailStore(home))
	addIdentity(sv, "beta") // running, without a bridge
	agentRecord(t, home, agentstore.Record{Name: "delta"})
	seedMail(t, home, "alpha",
		outbox.Entry{ID: "c-1", Text: "From agent beta via leo:\n\nare you there?", From: "beta"},
		outbox.Entry{ID: "c-2", Text: "from a human"},
		outbox.Entry{ID: "c-3", Text: "status?", From: "delta"},
		outbox.Entry{ID: "c-4", Text: "gone", From: "nobody"})

	sv.DropAgentMail("alpha")

	if got := mailOf(t, home, "alpha"); len(got) != 0 {
		t.Fatalf("the deleted agent's outbox kept %+v", got)
	}
	waitFor(t, "beta's notice", func() bool { return len(pastes()) == 1 })
	if p := pastes()[0]; !strings.HasPrefix(p, "leo-beta|") || !strings.Contains(p, "alpha") || !strings.Contains(p, "are you there?") {
		t.Fatalf("beta was told %q", p)
	}
	notice := mailOf(t, home, "delta")
	if len(notice) != 1 || !strings.Contains(notice[0].Text, "alpha") || !strings.Contains(notice[0].Text, "status?") {
		t.Fatalf("delta's outbox holds %+v, want the notice for its next launch", notice)
	}
	if got := mailOf(t, home, "nobody"); len(got) != 0 {
		t.Fatalf("a notice was queued for an agent that does not exist: %+v", got)
	}
}
