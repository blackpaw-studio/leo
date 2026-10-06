package consult

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
	"github.com/blackpaw-studio/leo/internal/outbox"
)

const notifyLaunch = "launch-orch"

// bridgedCaller is an orchestrator agent whose leo bridge is connected:
// its router queues delivers durably in a real outbox, as the supervisor's
// does, and stream reads what reaches its mod.
type bridgedCaller struct {
	hub    *bridge.Hub
	router *bridge.Router
	box    *outbox.Store
	stream *bridge.Stream
	panes  map[string]string // agent → primary pane
}

func newBridgedCaller(t *testing.T, name, pane string) *bridgedCaller {
	t.Helper()
	c := &bridgedCaller{hub: bridge.New(bridge.Options{}), box: outbox.New(t.TempDir(), outbox.Options{}), panes: map[string]string{name: pane}}
	t.Cleanup(c.hub.Close)
	target, err := c.hub.Open(name, notifyLaunch)
	if err != nil {
		t.Fatal(err)
	}
	if c.stream, err = c.hub.Connect(name, notifyLaunch); err != nil {
		t.Fatal(err)
	}
	c.router = &bridge.Router{
		Hub:     c.hub,
		Targets: func(agent string) (bridge.Target, bool) { return target, agent == name },
		Queue: func(agent string, t bridge.Target, cmd bridge.Command, from string) (*bridge.Ticket, error) {
			if err := c.box.Append(agent, outbox.Entry{ID: cmd.ID, Text: cmd.Text, AsUser: cmd.AsUser, From: from}); err != nil {
				return nil, err
			}
			return c.hub.EnqueueTo(t, cmd)
		},
	}
	return c
}

func (c *bridgedCaller) delivery(fallback NotificationDelivery) *BridgeNotificationDelivery {
	return NewBridgeNotificationDelivery(c.router, func(_ context.Context, agent string) (string, error) {
		if pane, ok := c.panes[agent]; ok {
			return pane, nil
		}
		return "", errors.New("no such agent")
	}, fallback)
}

func (c *bridgedCaller) next(t *testing.T) bridge.Command {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd, err := c.stream.Next(ctx)
	if err != nil {
		t.Fatalf("no command reached the mod: %v", err)
	}
	return cmd
}

func notifyRecord() Record {
	return Record{ID: "d-7", Caller: "orch", CallerPaneID: "%4", CallerHarness: "claude", Notify: true}
}

func TestNotificationCommandIDIsDeterministic(t *testing.T) {
	cases := map[string]string{"d-7#2": "notify-d-7-2", "d-7#10": "notify-d-7-10", "d-7": "notify-d-7-run"}
	for key, want := range cases {
		if got := NotificationCommandID("d-7", key); got != want {
			t.Errorf("NotificationCommandID(d-7, %q) = %q, want %q", key, got, want)
		}
	}
}

// A bridged caller gets its notification as a bridge deliver queued in its
// durable outbox under the notification's stable id, not over the inbox.
func TestBridgedCallerNotificationGoesThroughTheOutbox(t *testing.T) {
	c := newBridgedCaller(t, "orch", "%4")
	fallback := &fakeNotificationDelivery{ready: true}
	delivery := c.delivery(fallback)
	rec := notifyRecord()
	if !delivery.Ready(context.Background(), rec) {
		t.Fatal("a bridged caller is not ready")
	}
	if err := delivery.DeliverNotification(context.Background(), rec, "d-7#2", "[leo] dispatch d-7#2 done"); err != nil {
		t.Fatalf("DeliverNotification: %v", err)
	}
	cmd := c.next(t)
	if cmd.ID != "notify-d-7-2" || cmd.Op != bridge.OpDeliver || cmd.AsUser || cmd.Text != "[leo] dispatch d-7#2 done" {
		t.Fatalf("command = %+v, want a non-user deliver notify-d-7-2 of the line", cmd)
	}
	if entries, _ := c.box.List("orch"); len(entries) != 1 || entries[0].ID != "notify-d-7-2" {
		t.Fatalf("outbox = %+v, want the notification queued under its id", entries)
	}
	if len(fallback.calls) != 0 {
		t.Fatalf("fallback used for a bridged caller: %v", fallback.calls)
	}
}

// Delivering the same notification again (a sweep retry, a restarted
// daemon) queues nothing new: the id is the notification's, so the outbox
// and the hub see a repeat, and the mod's dedup re-acks one it ran.
func TestBridgedNotificationRedeliveryIsDeduplicated(t *testing.T) {
	c := newBridgedCaller(t, "orch", "%4")
	delivery := c.delivery(&fakeNotificationDelivery{ready: true})
	rec := notifyRecord()
	for range 2 {
		if err := delivery.DeliverNotification(context.Background(), rec, "d-7#2", "line"); err != nil {
			t.Fatalf("DeliverNotification: %v", err)
		}
	}
	if entries, _ := c.box.List("orch"); len(entries) != 1 {
		t.Fatalf("outbox = %+v, want one entry", entries)
	}
	if st := c.hub.State("orch"); st.Pending != 1 {
		t.Fatalf("hub pending = %d, want 1", st.Pending)
	}
}

// Callers without a usable bridge keep today's path: an unbridged agent, a
// human, and a dispatch that inherited its orchestrator's process name (its
// pane is not the orchestrator's primary pane, so the orchestrator's bridge
// is not its own).
func TestUnbridgedCallerNotificationFallsBack(t *testing.T) {
	c := newBridgedCaller(t, "orch", "%4")
	cases := map[string]Record{
		"unbridged agent":   {ID: "d-7", Caller: "other", CallerPaneID: "%9", CallerHarness: "claude"},
		"no caller":         {ID: "d-7", CallerPaneID: "%9", CallerHarness: "claude"},
		"nested dispatch":   {ID: "d-7", Caller: "orch", CallerPaneID: "%12", CallerHarness: "claude"},
		"codex in its pane": {ID: "d-7", Caller: "other", CallerPaneID: "%9", CallerHarness: "codex"},
	}
	for name, rec := range cases {
		t.Run(name, func(t *testing.T) {
			fallback := &fakeNotificationDelivery{ready: false}
			delivery := c.delivery(fallback)
			if delivery.Ready(context.Background(), rec) {
				t.Fatal("Ready did not defer to the fallback")
			}
			if err := delivery.DeliverNotification(context.Background(), rec, "d-7#1", "line"); err != nil {
				t.Fatalf("DeliverNotification: %v", err)
			}
			if len(fallback.calls) != 1 || fallback.calls[0] != rec.CallerPaneID+"\x00line" {
				t.Fatalf("fallback calls = %q, want the line to the caller pane", fallback.calls)
			}
		})
	}
	if st := c.hub.State("orch"); st.Pending != 0 {
		t.Fatalf("hub pending = %d, want nothing queued for orch", st.Pending)
	}
}

// A bridge that refuses the queue (outbox full, launch ended) sent nothing,
// so the sweep may try again.
func TestBridgedNotificationQueueFailureIsNotSent(t *testing.T) {
	c := newBridgedCaller(t, "orch", "%4")
	c.router.Queue = func(string, bridge.Target, bridge.Command, string) (*bridge.Ticket, error) {
		return nil, outbox.ErrFull
	}
	err := c.delivery(&fakeNotificationDelivery{}).DeliverNotification(context.Background(), notifyRecord(), "d-7#2", "line")
	if !errors.Is(err, ErrNotificationNotSent) {
		t.Fatalf("err = %v, want ErrNotificationNotSent", err)
	}
}

type keyedFakeDelivery struct {
	fakeNotificationDelivery
	keys []string
}

func (f *keyedFakeDelivery) DeliverNotification(_ context.Context, r Record, key, line string) error {
	f.keys = append(f.keys, key)
	return f.Deliver(context.Background(), r, line)
}

// The sweep hands a delivery that wants it the notification's key, which
// the bridge path turns into the command id.
func TestSweepNotificationsPassesTheKeyToAKeyedDelivery(t *testing.T) {
	d := NewDispatcher(nil)
	f := &keyedFakeDelivery{fakeNotificationDelivery: fakeNotificationDelivery{ready: true}}
	d.SetNotificationDelivery(f)
	s := &runState{record: Record{ID: "d-x", Kind: "dispatch", Notify: true, CallerPaneID: "%1", Status: StatusDone, Notifications: map[string]Notification{"d-x#3": {Disposition: NotificationPending, Message: "line"}}}, handle: &durableTestHandle{}}
	d.runs["d-x"] = s
	d.SweepNotifications(context.Background())
	if len(f.keys) != 1 || f.keys[0] != "d-x#3" || s.record.Notifications["d-x#3"].Disposition != NotificationDelivered {
		t.Fatalf("keys=%v ledger=%+v", f.keys, s.record.Notifications)
	}
}
