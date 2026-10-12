package push

import (
	"slices"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/chat"
	"github.com/blackpaw-studio/leo/internal/observe"
)

func chatEvent(m chat.Message) observe.Event {
	return observe.Event{Type: chat.EventMessage, Payload: &chat.MessagePayload{Agent: m.Agent, Message: m}}
}

func chatUpdate(m chat.Message) observe.Event {
	return observe.Event{Type: chat.EventUpdated, Payload: &chat.UpdatedPayload{Agent: m.Agent, Message: m, Change: chat.ChangeState}}
}

// idleNotifier is a Notifier with no delivery worker: Publish queues
// synchronously, so its queue holds exactly the alerts queued so far.
func idleNotifier(opts ...NotifierOption) *Notifier {
	return NewNotifier(nil, newRecordingSender(), opts...)
}

func noAlert(t *testing.T, n *Notifier) {
	t.Helper()
	if len(n.queue) != 0 {
		t.Fatalf("unexpected alert %+v", <-n.queue)
	}
}

func TestOutboundChatMessagesPush(t *testing.T) {
	n, sender, next := startNotifier(t)
	n.Publish(chatEvent(chat.Message{ID: "m1", Agent: "assistant", Direction: chat.DirectionOut, Kind: chat.KindText, Text: "Build is green"}))
	a := sender.next(t)
	if a.Agent != "assistant" || a.Kind != "chat" || a.Title != "assistant" || a.Body != "Build is green" || a.MessageID != "m1" || a.MessageKind != chat.KindText {
		t.Fatalf("alert = %+v", a)
	}
	if next.count() != 1 {
		t.Fatal("the event must still be forwarded")
	}

	n.Publish(chatEvent(chat.Message{ID: "m2", Agent: "assistant", Direction: chat.DirectionOut, Kind: chat.KindAsk, Text: "Deploy?", Options: []chat.Option{{Value: "y", Label: "Yes"}}}))
	a = sender.next(t)
	if a.MessageKind != chat.KindAsk || !slices.Equal(a.Options, []Option{{Value: "y", Label: "Yes"}}) {
		t.Fatalf("ask alert = %+v", a)
	}

	n.Publish(chatEvent(chat.Message{ID: "m3", Agent: "assistant", Direction: chat.DirectionOut, Kind: chat.KindFile, Attachments: []chat.Attachment{{Filename: "r.pdf"}, {Filename: "s.png"}}}))
	if a = sender.next(t); a.Body != "r.pdf, s.png" {
		t.Fatalf("file alert body = %q", a.Body)
	}
}

func TestChatMessagesThatDoNotPush(t *testing.T) {
	n := idleNotifier()
	for _, m := range []chat.Message{
		{ID: "in", Agent: "a", Direction: chat.DirectionIn, Kind: chat.KindText, Text: "from me"},
		{ID: "sys", Agent: "a", Direction: chat.DirectionOut, Kind: chat.KindSystem, Text: "Starting a…"},
		{ID: "perm", Agent: "a", Direction: chat.DirectionOut, Kind: chat.KindPermission, State: chat.StateOpen},
	} {
		n.Publish(chatEvent(m))
	}
	noAlert(t, n)
}

func TestChatPushFollowsThePolicy(t *testing.T) {
	var asked []string
	n := idleNotifier(WithPolicy(func(agent, state string) bool {
		asked = append(asked, agent+"/"+state)
		return false
	}))
	n.Publish(chatEvent(chat.Message{ID: "m1", Agent: "web", Direction: chat.DirectionOut, Kind: chat.KindTask, Text: "report"}))
	noAlert(t, n)
	if !slices.Equal(asked, []string{"web/chat"}) {
		t.Fatalf("policy asked %v", asked)
	}
}

func TestPermissionAttentionAlertCarriesTheChatMessageID(t *testing.T) {
	n, sender, _ := startNotifier(t)
	perm := chat.Message{ID: "p1", Agent: "a", Direction: chat.DirectionOut, Kind: chat.KindPermission, State: chat.StateOpen}
	n.Publish(chatEvent(perm))
	n.Publish(activity("a", observe.AttentionNeedsInput, &observe.AttentionReason{Kind: observe.AttentionReasonPermission, Tool: "Bash"}))
	a := sender.next(t)
	if a.Kind != string(observe.AttentionNeedsInput) || a.MessageID != "p1" || a.MessageKind != chat.KindPermission {
		t.Fatalf("alert = %+v", a)
	}

	// A classic (unbridged) agent's permission_prompt hook carries no reason.
	n.Publish(activity("a", observe.AttentionWorking, nil))
	n.Publish(activity("a", observe.AttentionNeedsInput, nil))
	if a = sender.next(t); a.MessageID != "p1" {
		t.Fatalf("reasonless needs_input alert = %+v", a)
	}

	perm.State = chat.StateAnswered
	n.Publish(chatUpdate(perm))
	n.Publish(activity("a", observe.AttentionWorking, nil))
	n.Publish(activity("a", observe.AttentionNeedsInput, &observe.AttentionReason{Kind: observe.AttentionReasonPermission, Tool: "Bash"}))
	if a = sender.next(t); a.MessageID != "" {
		t.Fatalf("an answered prompt's id must not ride a later alert: %+v", a)
	}
}

// fakeAfter is a timer seam the test fires by hand.
type fakeAfter struct {
	waits []time.Duration
	fns   []func()
	live  []bool
}

func (f *fakeAfter) afterFunc(d time.Duration, fn func()) func() bool {
	i := len(f.fns)
	f.waits, f.fns, f.live = append(f.waits, d), append(f.fns, fn), append(f.live, true)
	return func() bool {
		was := f.live[i]
		f.live[i] = false
		return was
	}
}

// fire runs every timer not stopped yet.
func (f *fakeAfter) fire() {
	for i, fn := range f.fns {
		if f.live[i] {
			f.live[i] = false
			fn()
		}
	}
}

func waitingNotifier(timers *fakeAfter) *Notifier {
	n := idleNotifier(WithPermissionWait(2 * time.Second))
	n.afterFunc = timers.afterFunc
	return n
}

func nextQueued(t *testing.T, n *Notifier) Alert {
	t.Helper()
	select {
	case a := <-n.queue:
		return a
	default:
		t.Fatal("no alert queued")
		return Alert{}
	}
}

var permissionReason = &observe.AttentionReason{Kind: observe.AttentionReasonPermission, Tool: "Bash"}

// The chat permission message usually opens first; either way round the
// alert carries its id.
func TestPermissionAlertCarriesTheIDInEitherArrivalOrder(t *testing.T) {
	perm := chat.Message{ID: "p1", Agent: "a", Direction: chat.DirectionOut, Kind: chat.KindPermission, State: chat.StateOpen}

	t.Run("chat message first", func(t *testing.T) {
		timers := &fakeAfter{}
		n := waitingNotifier(timers)
		n.Publish(chatEvent(perm))
		n.Publish(activity("a", observe.AttentionNeedsInput, permissionReason))
		if a := nextQueued(t, n); a.MessageID != "p1" || a.MessageKind != chat.KindPermission {
			t.Fatalf("alert = %+v", a)
		}
		if len(timers.fns) != 0 {
			t.Fatal("an alert that has its id waits for nothing")
		}
	})

	t.Run("attention first", func(t *testing.T) {
		timers := &fakeAfter{}
		n := waitingNotifier(timers)
		n.Publish(activity("a", observe.AttentionNeedsInput, permissionReason))
		noAlert(t, n)
		if len(timers.waits) != 1 || timers.waits[0] != 2*time.Second {
			t.Fatalf("waits = %v", timers.waits)
		}
		n.Publish(chatEvent(perm))
		if a := nextQueued(t, n); a.MessageID != "p1" || a.Kind != string(observe.AttentionNeedsInput) {
			t.Fatalf("alert = %+v", a)
		}
		timers.fire()
		noAlert(t, n)
	})

	t.Run("attention first, no chat message", func(t *testing.T) {
		timers := &fakeAfter{}
		n := waitingNotifier(timers)
		n.Publish(activity("a", observe.AttentionNeedsInput, nil))
		noAlert(t, n)
		timers.fire()
		if a := nextQueued(t, n); a.MessageID != "" || a.Kind != string(observe.AttentionNeedsInput) {
			t.Fatalf("alert = %+v", a)
		}
	})
}

// A question is not a permission prompt: it never waits.
func TestQuestionAlertDoesNotWaitForAPermissionMessage(t *testing.T) {
	timers := &fakeAfter{}
	n := waitingNotifier(timers)
	n.Publish(activity("a", observe.AttentionNeedsInput, &observe.AttentionReason{Kind: observe.AttentionReasonQuestion}))
	if a := nextQueued(t, n); a.MessageID != "" {
		t.Fatalf("alert = %+v", a)
	}
}
