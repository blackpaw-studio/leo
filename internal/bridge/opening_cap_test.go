package bridge

import (
	"errors"
	"testing"
)

// A launch's opening, gated or not, takes no slot under the agent's cap:
// the launch carries an outbox as large as the cap over behind it, and
// the last carried message is never held back for the opening's sake.
func TestAnOpeningTakesNoSlotUnderTheCap(t *testing.T) {
	for name, enqueue := range map[string]func(*Hub, Target, Command) (*Ticket, error){
		"gate":  (*Hub).EnqueueGate,
		"plain": (*Hub).EnqueueOpening,
	} {
		t.Run(name, func(t *testing.T) {
			h := newTestHub(newFakeClock(), func(o *Options) { o.MaxPending = 2 })
			target := mustOpen(t, h, agentA)
			if _, err := enqueue(h, target, Deliver("the opening", true)); err != nil {
				t.Fatal(err)
			}
			for _, text := range []string{"first", "second"} {
				if _, err := h.EnqueueTo(target, Deliver(text, false)); err != nil {
					t.Fatalf("carrying %q behind the opening: %v", text, err)
				}
			}
			if _, err := h.EnqueueTo(target, Deliver("third", false)); !errors.Is(err, ErrOutboxFull) {
				t.Fatalf("past the cap: err=%v, want ErrOutboxFull", err)
			}
		})
	}
}

// A generation has one opening: a second, under another id, is refused
// rather than let a caller queue past the cap.
func TestAGenerationHasOneOpening(t *testing.T) {
	h := newTestHub(newFakeClock())
	target := mustOpen(t, h, agentA)
	first := Deliver("the opening", true)
	first.ID = "opening-1"
	if _, err := h.EnqueueGate(target, first); err != nil {
		t.Fatal(err)
	}
	if _, err := h.EnqueueGate(target, first); err != nil {
		t.Fatalf("queueing the same opening again: %v", err)
	}
	if _, err := h.EnqueueOpening(target, Deliver("another opening", true)); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("a second opening: err=%v, want ErrInvalidCommand", err)
	}
}
