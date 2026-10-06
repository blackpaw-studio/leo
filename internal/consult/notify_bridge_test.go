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
	owners map[string]string // bridge key → agent holding it now
}

func newBridgedCaller(t *testing.T, name, pane string) *bridgedCaller {
	t.Helper()
	c := &bridgedCaller{hub: bridge.New(bridge.Options{}), box: outbox.New(t.TempDir(), outbox.Options{}), panes: map[string]string{name: pane}, owners: map[string]string{name: name}}
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
	}, func(key string) (string, bool) {
		owner, ok := c.owners[key]
		return owner, ok
	}, fallback)
}

// send delivers line the way the sweep does: over the transport the
// delivery picks for rec.
func send(d *BridgeNotificationDelivery, rec Record, key, line string) error {
	return d.DeliverVia(context.Background(), rec, d.NotificationTransport(context.Background(), rec), key, line)
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
	if err := send(delivery, rec, "d-7#2", "[leo] dispatch d-7#2 done"); err != nil {
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
		if err := send(delivery, rec, "d-7#2", "line"); err != nil {
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
			if err := send(delivery, rec, "d-7#1", "line"); err != nil {
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
	err := send(c.delivery(&fakeNotificationDelivery{}), notifyRecord(), "d-7#2", "line")
	if !errors.Is(err, ErrNotificationNotSent) {
		t.Fatalf("err = %v, want ErrNotificationNotSent", err)
	}
}

type keyedFakeDelivery struct {
	fakeNotificationDelivery
	keys []string
}

func (f *keyedFakeDelivery) NotificationTransport(context.Context, Record) string { return "" }

func (f *keyedFakeDelivery) DeliverVia(_ context.Context, r Record, _, key, line string) error {
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

// A caller that recorded its bridge key is routed by that key to whoever
// holds it now: a renamed agent's outbox, with no pane check needed.
func TestNotificationRoutesByCallerBridgeKeyAfterARename(t *testing.T) {
	c := newBridgedCaller(t, "orch", "%4")
	c.owners = map[string]string{"orch": "orch-renamed"}
	var queuedFor []string
	durable := c.router.Queue
	c.router.Queue = func(agent string, t bridge.Target, cmd bridge.Command, from string) (*bridge.Ticket, error) {
		queuedFor = append(queuedFor, agent)
		return durable(agent, t, cmd, from)
	}
	fallback := &fakeNotificationDelivery{ready: true}
	rec := Record{ID: "d-7", Caller: "orch", CallerBridgeKey: "orch", CallerPaneID: "%99", CallerHarness: "claude", Notify: true}
	if err := send(c.delivery(fallback), rec, "d-7#2", "line"); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if cmd := c.next(t); cmd.ID != "notify-d-7-2" {
		t.Fatalf("command = %+v", cmd)
	}
	if len(queuedFor) != 1 || queuedFor[0] != "orch-renamed" || len(fallback.calls) != 0 {
		t.Fatalf("queued for %v, fallback %v; want the key's current owner's outbox", queuedFor, fallback.calls)
	}
}

// A bridged dispatch that dispatches again is the caller: its key routes to
// its own bridge (dispatches keep no outbox), not its orchestrator's.
func TestNotificationRoutesToACallingDispatchByItsBridgeKey(t *testing.T) {
	c := newBridgedCaller(t, "orch", "%4")
	key := DispatchBridgeKey("d-parent")
	if _, err := c.hub.Open(key, "launch-parent"); err != nil {
		t.Fatal(err)
	}
	stream, err := c.hub.Connect(key, "launch-parent")
	if err != nil {
		t.Fatal(err)
	}
	fallback := &fakeNotificationDelivery{ready: true}
	rec := Record{ID: "d-7", Caller: "orch", CallerBridgeKey: key, CallerPaneID: "%12", CallerHarness: "claude", Notify: true}
	if err := send(c.delivery(fallback), rec, "d-7#1", "line"); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd, err := stream.Next(ctx)
	if err != nil || cmd.ID != "notify-d-7-1" {
		t.Fatalf("dispatch's bridge got %+v, %v", cmd, err)
	}
	if st := c.hub.State("orch"); st.Pending != 0 || len(fallback.calls) != 0 {
		t.Fatalf("orch pending %d, fallback %v; want neither", st.Pending, fallback.calls)
	}
}

// A recorded key whose bridge is not connected falls back, as an
// unbridged caller does.
func TestNotificationWithADisconnectedCallerBridgeKeyFallsBack(t *testing.T) {
	c := newBridgedCaller(t, "orch", "%4")
	fallback := &fakeNotificationDelivery{ready: true}
	rec := Record{ID: "d-7", Caller: "orch", CallerBridgeKey: "gone", CallerPaneID: "%4", CallerHarness: "claude"}
	if err := send(c.delivery(fallback), rec, "d-7#1", "line"); err != nil || len(fallback.calls) != 1 {
		t.Fatalf("err %v fallback %v; want the fallback used", err, fallback.calls)
	}
}

// The daemon restarts after persisting a bridge claim but before queueing
// the deliver: the restored claim is retried on the bridge under the same
// outbox id, and delivered exactly once.
func TestRestoredBridgeClaimIsDeliveredExactlyOnce(t *testing.T) {
	c := newBridgedCaller(t, "orch", "%4")
	d := NewDispatcher(NewFileRecorder(t.TempDir()))
	d.SetNotificationDelivery(c.delivery(&fakeNotificationDelivery{ready: true}))
	rec := notifyRecord()
	rec.Kind, rec.Status = "dispatch", StatusDone
	rec.Notifications = map[string]Notification{"d-7#2": {Disposition: NotificationClaimed, Transport: NotificationTransportBridge, ClaimedAt: d.now(), Message: "line"}}
	d.restorePendingNotifications(rec)
	d.SweepNotifications(context.Background())
	d.SweepNotifications(context.Background())
	if cmd := c.next(t); cmd.ID != "notify-d-7-2" || cmd.Text != "line" {
		t.Fatalf("command = %+v", cmd)
	}
	if entries, _ := c.box.List("orch"); len(entries) != 1 {
		t.Fatalf("outbox = %+v, want one entry", entries)
	}
	got, err := d.Get("d-7")
	if err != nil {
		t.Fatal(err)
	}
	if n := got.Notifications["d-7#2"]; n.Disposition != NotificationDelivered {
		t.Fatalf("notification = %+v, want delivered", n)
	}
}

// A claim records the transport it was made for, so a restart knows which
// claims are safe to retry.
func TestBridgeClaimRecordsItsTransport(t *testing.T) {
	c := newBridgedCaller(t, "orch", "%4")
	d := NewDispatcher(nil)
	d.SetNotificationDelivery(c.delivery(&fakeNotificationDelivery{ready: true}))
	rec := notifyRecord()
	rec.Kind, rec.Status = "dispatch", StatusDone
	rec.Notifications = map[string]Notification{"d-7#2": {Disposition: NotificationPending, Message: "line"}}
	h := &durableTestHandle{}
	d.runs["d-7"] = &runState{record: rec, handle: h}
	d.SweepNotifications(context.Background())
	if n := h.rec.Notifications["d-7#2"]; n.Disposition != NotificationDelivered || n.Transport != NotificationTransportBridge {
		t.Fatalf("notification = %+v, want delivered over the bridge", n)
	}
}

// A restored bridge claim whose caller's bridge is not back yet stays
// claimed for the bridge: falling back could deliver it twice.
func TestRestoredBridgeClaimWaitsForTheBridge(t *testing.T) {
	c := newBridgedCaller(t, "orch", "%4")
	fallback := &fakeNotificationDelivery{ready: true}
	d := NewDispatcher(nil)
	d.SetNotificationDelivery(c.delivery(fallback))
	rec := Record{ID: "d-7", Kind: "dispatch", Caller: "other", CallerPaneID: "%9", CallerHarness: "claude", Notify: true, Status: StatusDone,
		Notifications: map[string]Notification{"d-7#2": {Disposition: NotificationClaimed, Transport: NotificationTransportBridge, ClaimedAt: d.now(), Message: "line"}}}
	s := &runState{record: rec, handle: &durableTestHandle{}}
	d.runs["d-7"] = s
	d.SweepNotifications(context.Background())
	if n := s.record.Notifications["d-7#2"]; n.Disposition != NotificationClaimed || len(fallback.calls) != 0 {
		t.Fatalf("notification = %+v fallback %v; want still claimed, nothing sent", n, fallback.calls)
	}
}
