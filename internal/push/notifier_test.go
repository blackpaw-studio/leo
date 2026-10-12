package push

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/observe"
)

// recordingSender hands each Alert to a channel the test reads.
type recordingSender struct{ got chan Alert }

func newRecordingSender() *recordingSender { return &recordingSender{got: make(chan Alert, 64)} }

func (s *recordingSender) Send(_ context.Context, a Alert) { s.got <- a }

// next returns the next alert, failing if none arrives.
func (s *recordingSender) next(t *testing.T) Alert {
	t.Helper()
	select {
	case a := <-s.got:
		return a
	case <-time.After(5 * time.Second):
		t.Fatal("no alert delivered")
		return Alert{}
	}
}

// capturePublisher records forwarded events.
type capturePublisher struct {
	mu  sync.Mutex
	evs []observe.Event
}

func (p *capturePublisher) Publish(ev observe.Event) {
	p.mu.Lock()
	p.evs = append(p.evs, ev)
	p.mu.Unlock()
}

func (p *capturePublisher) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.evs)
}

func activity(agent string, state observe.AttentionState, reason *observe.AttentionReason) observe.Event {
	return observe.Event{
		Type: observe.EventAgentActivity,
		Payload: &observe.AgentActivityPayload{
			Agent:     agent,
			Attention: &observe.AgentAttention{State: state, Reason: reason},
		},
	}
}

func startNotifier(t *testing.T, opts ...NotifierOption) (*Notifier, *recordingSender, *capturePublisher) {
	t.Helper()
	sender, next := newRecordingSender(), &capturePublisher{}
	opts = append([]NotifierOption{WithNotifierClock(func() time.Time { return fixedNow })}, opts...)
	n := NewNotifier(next, sender, opts...)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	n.Start(ctx)
	return n, sender, next
}

func TestTransitionIntoNeedsYouStatesSendsAnAlert(t *testing.T) {
	cases := []struct {
		state     observe.AttentionState
		reason    *observe.AttentionReason
		wantTitle string
	}{
		{observe.AttentionNeedsInput, &observe.AttentionReason{Kind: observe.AttentionReasonPermission, Tool: "Bash"}, "Needs permission: Bash"},
		{observe.AttentionNeedsInput, nil, "Waiting for your input"},
		{observe.AttentionFinished, nil, "Finished"},
		{observe.AttentionErrored, nil, "Hit an error"},
	}
	for _, tc := range cases {
		t.Run(string(tc.state)+"/"+tc.wantTitle, func(t *testing.T) {
			n, sender, _ := startNotifier(t)

			n.Publish(activity("builder", tc.state, tc.reason))

			got := sender.next(t)
			want := Alert{Agent: "builder", Kind: string(tc.state), Title: tc.wantTitle, At: fixedNow}
			if got != want {
				t.Fatalf("alert = %+v, want %+v", got, want)
			}
		})
	}
}

func TestOnlyTransitionsSendNotRepeatsOrQuietStates(t *testing.T) {
	n, sender, _ := startNotifier(t)

	n.Publish(activity("a", observe.AttentionWorking, nil))  // not needs-you
	n.Publish(activity("a", observe.AttentionUnknown, nil))  // not needs-you
	n.Publish(activity("a", observe.AttentionFinished, nil)) // transition → send
	n.Publish(activity("a", observe.AttentionFinished, nil)) // repeat (new revision, tracker sweep…) → no
	n.Publish(activity("a", observe.AttentionFinished, nil)) // repeat → no
	n.Publish(activity("b", observe.AttentionFinished, nil)) // other agent → send
	n.Publish(activity("a", observe.AttentionWorking, nil))  // left the state
	n.Publish(activity("a", observe.AttentionFinished, nil)) // transition again → send

	var agents []string
	for range 3 {
		agents = append(agents, sender.next(t).Agent)
	}
	if want := []string{"a", "b", "a"}; !equal(agents, want) {
		t.Fatalf("alerts for %v, want %v", agents, want)
	}
	n.Publish(activity("sentinel", observe.AttentionErrored, nil))
	if a := sender.next(t); a.Agent != "sentinel" {
		t.Fatalf("extra alert for %q slipped in before the sentinel", a.Agent)
	}
}

func TestNeedsInputReasonChangeIsNotANewTransition(t *testing.T) {
	n, sender, _ := startNotifier(t)
	perm := &observe.AttentionReason{Kind: observe.AttentionReasonPermission, Tool: "Bash"}
	question := &observe.AttentionReason{Kind: observe.AttentionReasonQuestion}

	n.Publish(activity("a", observe.AttentionNeedsInput, perm))
	n.Publish(activity("a", observe.AttentionNeedsInput, question))
	n.Publish(activity("sentinel", observe.AttentionErrored, nil))

	if a := sender.next(t); a.Agent != "a" {
		t.Fatalf("first alert for %q", a.Agent)
	}
	if a := sender.next(t); a.Agent != "sentinel" {
		t.Fatalf("a reason change on the same state alerted again (%q)", a.Agent)
	}
}

func TestStoppedAgentForgetsItsStateSoARespawnAlertsAgain(t *testing.T) {
	n, sender, _ := startNotifier(t)

	n.Publish(activity("a", observe.AttentionFinished, nil))
	n.Publish(observe.Event{Type: observe.EventAgentStopped, Payload: &observe.AgentStoppedPayload{Agent: "a"}})
	n.Publish(activity("a", observe.AttentionFinished, nil))

	sender.next(t)
	sender.next(t) // fails if the respawn's first finished was mistaken for a repeat
}

func TestEventsAreForwardedUnchanged(t *testing.T) {
	n, _, next := startNotifier(t)
	n.Publish(activity("a", observe.AttentionWorking, nil))
	n.Publish(observe.Event{Type: observe.EventAgentStopped, Payload: &observe.AgentStoppedPayload{Agent: "a"}})
	n.Publish(observe.Event{Type: observe.EventAgentActivity, Payload: &observe.AgentActivityPayload{Agent: "x"}}) // no attention
	if next.count() != 3 {
		t.Fatalf("forwarded %d events, want 3", next.count())
	}
}

// blockedSender never returns until released.
type blockedSender struct{ release chan struct{} }

func (s *blockedSender) Send(ctx context.Context, _ Alert) {
	select {
	case <-s.release:
	case <-ctx.Done():
	}
}

func TestPublishNeverBlocksWhenTheSenderStallsAndQueueFills(t *testing.T) {
	sender := &blockedSender{release: make(chan struct{})}
	n := NewNotifier(nil, sender, WithQueueSize(2))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n.Start(ctx)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 50 { // far more transitions than worker + queue can hold
			agent := string(rune('a' + i%26))
			n.Publish(activity(agent, observe.AttentionFinished, nil))
			n.Publish(activity(agent, observe.AttentionWorking, nil))
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish blocked on a stalled sender")
	}
	close(sender.release)
}

func TestPublishBeforeStartDoesNotBlock(t *testing.T) {
	n := NewNotifier(nil, newRecordingSender(), WithQueueSize(1))
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 10 {
			n.Publish(activity(string(rune('a'+i)), observe.AttentionFinished, nil))
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish blocked before Start")
	}
}

func TestNilNextPublisherIsAllowed(t *testing.T) {
	sender := newRecordingSender()
	n := NewNotifier(nil, sender)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n.Start(ctx)
	n.Publish(activity("a", observe.AttentionErrored, nil))
	sender.next(t)
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// denyStates is a policy that blocks the given states for every agent and
// records each (agent, state) it was asked about.
func denyStates(asked *[][2]string, denied ...observe.AttentionState) func(agent, state string) bool {
	return func(agent, state string) bool {
		*asked = append(*asked, [2]string{agent, state})
		for _, d := range denied {
			if string(d) == state {
				return false
			}
		}
		return true
	}
}

func TestPolicyBlocksASuppressedStateAndOnlyAllowedStatesQueue(t *testing.T) {
	var asked [][2]string
	n, sender, _ := startNotifier(t, WithPolicy(denyStates(&asked, observe.AttentionFinished)))

	n.Publish(activity("a", observe.AttentionFinished, nil))   // blocked
	n.Publish(activity("a", observe.AttentionFinished, nil))   // repeat of a suppressed state: still nothing
	n.Publish(activity("a", observe.AttentionErrored, nil))    // allowed, different state
	n.Publish(activity("a", observe.AttentionWorking, nil))    // quiet state: policy not consulted
	n.Publish(activity("a", observe.AttentionFinished, nil))   // new transition into blocked state
	n.Publish(activity("b", observe.AttentionNeedsInput, nil)) // other agent, allowed

	if got := sender.next(t); got.Agent != "a" || got.Kind != "errored" {
		t.Fatalf("first alert = %+v, want a/errored (a suppressed finished must not queue)", got)
	}
	if got := sender.next(t); got.Agent != "b" || got.Kind != "needs_input" {
		t.Fatalf("second alert = %+v, want b/needs_input", got)
	}
	select {
	case extra := <-sender.got:
		t.Fatalf("unexpected extra alert %+v", extra)
	default:
	}
	// Consulted once per needs-you transition: finished, errored, finished, needs_input.
	if len(asked) != 4 {
		t.Fatalf("policy consulted %d times (%v), want 4", len(asked), asked)
	}
}

// A suppressed transition still updates last, so flipping the policy later
// does not turn the already-seen state into a push.
func TestSuppressedTransitionStillRecordsState(t *testing.T) {
	allow := false
	n, sender, _ := startNotifier(t, WithPolicy(func(string, string) bool { return allow }))

	n.Publish(activity("a", observe.AttentionFinished, nil)) // suppressed
	allow = true
	n.Publish(activity("a", observe.AttentionFinished, nil)) // same state: not a transition
	n.Publish(activity("a", observe.AttentionErrored, nil))  // real transition

	if got := sender.next(t); got.Kind != "errored" {
		t.Fatalf("alert = %+v, want errored first", got)
	}
}

func turnCompleted(agent, preview string) observe.Event {
	return observe.Event{
		Type:    observe.EventAgentTurnCompleted,
		Payload: &observe.AgentTurnCompletedPayload{Agent: agent, Outcome: observe.TurnCompleted, Preview: preview},
	}
}

func TestAlertBodyPerState(t *testing.T) {
	cases := []struct {
		name     string
		state    observe.AttentionState
		reason   *observe.AttentionReason
		wantBody string
	}{
		{"question carries its text", observe.AttentionNeedsInput,
			&observe.AttentionReason{Kind: observe.AttentionReasonQuestion, Tool: "AskUserQuestion", Detail: "Ship it? (+1 more)"}, "Ship it? (+1 more)"},
		{"permission carries the command", observe.AttentionNeedsInput,
			&observe.AttentionReason{Kind: observe.AttentionReasonPermission, Tool: "Bash", Detail: "rm -rf build/"}, "rm -rf build/"},
		{"elicitation carries its detail", observe.AttentionNeedsInput,
			&observe.AttentionReason{Kind: observe.AttentionReasonElicitation, Detail: "github"}, "github"},
		{"needs input without a detail is title only", observe.AttentionNeedsInput,
			&observe.AttentionReason{Kind: observe.AttentionReasonPermission, Tool: "Bash"}, ""},
		{"needs input without a reason is title only", observe.AttentionNeedsInput, nil, ""},
		{"errored carries its detail when set", observe.AttentionErrored,
			&observe.AttentionReason{Detail: "exit 1"}, "exit 1"},
		{"errored without a reason is title only", observe.AttentionErrored, nil, ""},
		{"finished with no preview is title only", observe.AttentionFinished, nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n, sender, _ := startNotifier(t)

			n.Publish(activity("builder", tc.state, tc.reason))

			if got := sender.next(t); got.Body != tc.wantBody {
				t.Fatalf("body = %q, want %q", got.Body, tc.wantBody)
			}
		})
	}
}

func TestFinishedAlertUsesTheTurnPreviewPublishedBeforeIt(t *testing.T) {
	n, sender, _ := startNotifier(t)

	n.Publish(turnCompleted("builder", "Refactored the parser and added tests."))
	n.Publish(activity("builder", observe.AttentionFinished, nil))

	got := sender.next(t)
	if got.Body != "Refactored the parser and added tests." || got.Title != "Finished" {
		t.Fatalf("alert = %+v, want the finished title with the turn preview as body", got)
	}
}

func TestFinishedPreviewIsPerAgentAndLatestWins(t *testing.T) {
	n, sender, _ := startNotifier(t)

	n.Publish(turnCompleted("a", "first"))
	n.Publish(turnCompleted("b", "other agent"))
	n.Publish(turnCompleted("a", "second"))
	n.Publish(activity("a", observe.AttentionFinished, nil))

	if got := sender.next(t); got.Body != "second" {
		t.Fatalf("body = %q, want the agent's latest preview", got.Body)
	}
}

func TestFinishedPreviewIsNotReusedByALaterFinished(t *testing.T) {
	n, sender, _ := startNotifier(t)

	n.Publish(turnCompleted("a", "turn one"))
	n.Publish(activity("a", observe.AttentionFinished, nil))
	n.Publish(activity("a", observe.AttentionWorking, nil))
	n.Publish(activity("a", observe.AttentionFinished, nil)) // no new turn_completed first

	sender.next(t)
	if got := sender.next(t); got.Body != "" {
		t.Fatalf("body = %q, want no stale preview", got.Body)
	}
}

func TestStoppedAgentForgetsItsPreview(t *testing.T) {
	n, sender, _ := startNotifier(t)

	n.Publish(turnCompleted("a", "before the stop"))
	n.Publish(observe.Event{Type: observe.EventAgentStopped, Payload: &observe.AgentStoppedPayload{Agent: "a"}})
	n.Publish(activity("a", observe.AttentionFinished, nil))

	if got := sender.next(t); got.Body != "" {
		t.Fatalf("body = %q, want a respawned agent not to inherit the old preview", got.Body)
	}
}
