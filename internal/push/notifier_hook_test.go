package push

import (
	"testing"

	"github.com/blackpaw-studio/leo/internal/observe"
)

func finishedWithPreview(agent, preview string) observe.Event {
	ev := activity(agent, observe.AttentionFinished, nil)
	ev.Payload.(*observe.AgentActivityPayload).Attention.Preview = preview
	return ev
}

// The native Stop hook publishes finished before the bridge's turn_completed
// arrives, with its own preview on the transition.
func TestNativeStopFinishedUsesItsOwnPreviewAndDoesNotLeakTheLateBridgeOne(t *testing.T) {
	n, sender, _ := startNotifier(t)

	n.Publish(finishedWithPreview("a", "from the Stop payload"))
	n.Publish(turnCompleted("a", "late bridge preview")) // after the alert
	n.Publish(activity("a", observe.AttentionWorking, nil))
	n.Publish(activity("a", observe.AttentionFinished, nil)) // next turn, no preview

	if got := sender.next(t); got.Body != "from the Stop payload" {
		t.Fatalf("first body = %q, want the Stop payload's preview", got.Body)
	}
	if got := sender.next(t); got.Body != "" {
		t.Fatalf("next turn's body = %q, want the late preview not to leak", got.Body)
	}
}

func TestNativeStopPreviewBeatsTheCachedBridgePreview(t *testing.T) {
	n, sender, _ := startNotifier(t)

	n.Publish(turnCompleted("a", "bridge"))
	n.Publish(finishedWithPreview("a", "stop"))

	if got := sender.next(t); got.Body != "stop" {
		t.Fatalf("body = %q, want the transition's own preview", got.Body)
	}
}

func TestSuppressedFinishedStillConsumesThePreview(t *testing.T) {
	n, sender, _ := startNotifier(t, WithPolicy(func(_, state string) bool { return state != "finished" }))

	n.Publish(turnCompleted("a", "suppressed turn"))
	n.Publish(activity("a", observe.AttentionFinished, nil)) // policy drops it
	n.Publish(activity("a", observe.AttentionWorking, nil))
	n.Publish(activity("a", observe.AttentionErrored, nil))
	n.Publish(activity("a", observe.AttentionWorking, nil))
	n.Publish(activity("a", observe.AttentionFinished, nil))

	if got := sender.next(t); got.Kind != "errored" || got.Body != "" {
		t.Fatalf("alert = %+v, want the errored alert", got)
	}
}

func TestLeavingFinishedDropsAPreviewThatArrivedAfterIt(t *testing.T) {
	n, sender, _ := startNotifier(t)

	n.Publish(activity("a", observe.AttentionFinished, nil))
	n.Publish(turnCompleted("a", "late"))
	n.Publish(activity("a", observe.AttentionNeedsInput, nil))
	n.Publish(activity("a", observe.AttentionFinished, nil))

	sender.next(t)
	sender.next(t)
	if got := sender.next(t); got.Kind != "finished" || got.Body != "" {
		t.Fatalf("alert = %+v, want a title-only finished", got)
	}
}

func TestRepeatedWorkingDoesNotDropTheCurrentTurnsPreview(t *testing.T) {
	n, sender, _ := startNotifier(t)

	n.Publish(activity("a", observe.AttentionWorking, nil))
	n.Publish(turnCompleted("a", "this turn"))
	n.Publish(activity("a", observe.AttentionWorking, nil)) // a repeat, not a transition
	n.Publish(activity("a", observe.AttentionFinished, nil))

	if got := sender.next(t); got.Body != "this turn" {
		t.Fatalf("body = %q, want the preview kept across a repeated working", got.Body)
	}
}
