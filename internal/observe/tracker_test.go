package observe

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/tmux"
)

// recordingPublisher captures every event published to it.
type recordingPublisher struct {
	events []Event
}

func (r *recordingPublisher) Publish(ev Event) { r.events = append(r.events, ev) }

// fakeCaptureCommand fakes the capture-pane exec seam with a real, harmless
// binary (printf) that just echoes the desired fixture output, so tests never
// shell out to tmux.
func fakeCaptureCommand(output string) func(ctx context.Context, name string, arg ...string) *exec.Cmd {
	return func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "printf", "%s", output)
	}
}

func TestClassifyFreshAgentBelowIdleThresholdIsWorking(t *testing.T) {
	// Arrange
	tr := NewTracker("", func() map[string]string { return nil }, nil, WithIdleThreshold(15*time.Second))
	now := time.Date(2026, 1, 1, 0, 0, 10, 0, time.UTC)
	sess := tmux.SessionActivity{LastActivity: now.Add(-5 * time.Second)}

	// Act
	result, sample := tr.classify(now, false, AgentActivity{}, sess)

	// Assert
	if result.Activity != ActivityWorking {
		t.Fatalf("expected working, got %s", result.Activity)
	}
	if !sample {
		t.Fatalf("expected fresh working agent to be sampled")
	}
	if !result.LastActivityAt.Equal(sess.LastActivity) {
		t.Fatalf("expected LastActivityAt %v, got %v", sess.LastActivity, result.LastActivityAt)
	}
}

func TestClassifyFreshAgentPastIdleThresholdIsIdle(t *testing.T) {
	// Arrange
	tr := NewTracker("", func() map[string]string { return nil }, nil, WithIdleThreshold(15*time.Second))
	now := time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC)
	sess := tmux.SessionActivity{LastActivity: now.Add(-30 * time.Second)}

	// Act
	result, sample := tr.classify(now, false, AgentActivity{}, sess)

	// Assert
	if result.Activity != ActivityIdle {
		t.Fatalf("expected idle, got %s", result.Activity)
	}
	if sample {
		t.Fatalf("expected idle agent not to be sampled")
	}
}

func TestClassifyAdvancedActivityIsWorking(t *testing.T) {
	// Arrange
	tr := NewTracker("", func() map[string]string { return nil }, nil, WithIdleThreshold(15*time.Second))
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	prev := AgentActivity{Activity: ActivityIdle, LastActivityAt: base}
	sess := tmux.SessionActivity{LastActivity: base.Add(5 * time.Second)}
	now := base.Add(5 * time.Second)

	// Act
	result, sample := tr.classify(now, true, prev, sess)

	// Assert
	if result.Activity != ActivityWorking {
		t.Fatalf("expected working after advance, got %s", result.Activity)
	}
	if !sample {
		t.Fatalf("expected advance to trigger a pane sample")
	}
	if !result.LastActivityAt.Equal(sess.LastActivity) {
		t.Fatalf("expected LastActivityAt to move to %v, got %v", sess.LastActivity, result.LastActivityAt)
	}
}

func TestClassifyQuietPastThresholdBecomesIdle(t *testing.T) {
	// Arrange
	tr := NewTracker("", func() map[string]string { return nil }, nil, WithIdleThreshold(15*time.Second))
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	action := &Action{Kind: ActionKindPane, Detail: "go test ./..."}
	prev := AgentActivity{Activity: ActivityWorking, LastActivityAt: base, CurrentAction: action}
	sess := tmux.SessionActivity{LastActivity: base} // unchanged
	now := base.Add(16 * time.Second)

	// Act
	result, sample := tr.classify(now, true, prev, sess)

	// Assert
	if result.Activity != ActivityIdle {
		t.Fatalf("expected idle after quiet period, got %s", result.Activity)
	}
	if sample {
		t.Fatalf("idle transition must not sample the pane")
	}
	// The action is only ever sampled while working; it must not survive
	// into an idle reading, or a visualizer would render a stale
	// "currently doing" indicator indefinitely for an agent that finished
	// working long ago.
	if result.CurrentAction != nil {
		t.Fatalf("expected action cleared on transition to idle, got %+v", result.CurrentAction)
	}
}

func TestClassifyQuietBelowThresholdStaysWorking(t *testing.T) {
	// Arrange
	tr := NewTracker("", func() map[string]string { return nil }, nil, WithIdleThreshold(15*time.Second))
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	prev := AgentActivity{Activity: ActivityWorking, LastActivityAt: base}
	sess := tmux.SessionActivity{LastActivity: base}
	now := base.Add(5 * time.Second)

	// Act
	result, sample := tr.classify(now, true, prev, sess)

	// Assert
	if result.Activity != ActivityWorking {
		t.Fatalf("expected to remain working, got %s", result.Activity)
	}
	if sample {
		t.Fatalf("no-advance, no-threshold transition must not sample")
	}
}

func TestSameAction(t *testing.T) {
	a := &Action{Kind: ActionKindPane, Detail: "x"}
	b := &Action{Kind: ActionKindPane, Detail: "x"}
	c := &Action{Kind: ActionKindPane, Detail: "y"}
	if !sameAction(nil, nil) {
		t.Fatal("nil, nil should be equal")
	}
	if sameAction(nil, a) || sameAction(a, nil) {
		t.Fatal("nil vs non-nil should differ")
	}
	if !sameAction(a, b) {
		t.Fatal("equal-content actions should be equal")
	}
	if sameAction(a, c) {
		t.Fatal("different-content actions should differ")
	}
}

func TestTrackerActivitiesReturnsDefensiveCopy(t *testing.T) {
	// Arrange
	tr := NewTracker("", func() map[string]string { return nil }, nil)
	tr.activities = map[string]AgentActivity{
		"den": {Activity: ActivityWorking, CurrentAction: &Action{Kind: ActionKindPane, Detail: "orig"}},
	}

	// Act
	copy1 := tr.Activities()
	copy1["den"].CurrentAction.Detail = "mutated"
	copy2 := tr.Activities()

	// Assert
	if copy2["den"].CurrentAction.Detail != "orig" {
		t.Fatalf("expected internal state unaffected by mutation of returned copy, got %q", copy2["den"].CurrentAction.Detail)
	}
}

// TestTrackerPublishDoesNotAliasCurrentAction guards against publish handing
// a subscriber the same *Action the tracker stores: mutating the source
// action after publish must never be observable on the delivered payload.
func TestTrackerPublishDoesNotAliasCurrentAction(t *testing.T) {
	// Arrange
	pub := &recordingPublisher{}
	tr := NewTracker("", func() map[string]string { return nil }, pub)
	action := &Action{Kind: ActionKindPane, Detail: "orig"}

	// Act
	tr.publish("den", AgentActivity{Activity: ActivityWorking, CurrentAction: action})
	action.Detail = "mutated"

	// Assert
	if len(pub.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(pub.events))
	}
	payload, ok := pub.events[0].Payload.(*AgentActivityPayload)
	if !ok {
		t.Fatalf("expected AgentActivityPayload, got %T", pub.events[0].Payload)
	}
	if payload.CurrentAction == action {
		t.Fatal("payload.CurrentAction must not alias the source *Action")
	}
	if payload.CurrentAction.Detail != "orig" {
		t.Fatalf("expected published action unaffected by later mutation, got %q", payload.CurrentAction.Detail)
	}
}

func TestTrackerSweepPublishesOnActivityChange(t *testing.T) {
	// Arrange
	origList := listSessionActivityFn
	origCapture := captureExecCommand
	t.Cleanup(func() {
		listSessionActivityFn = origList
		captureExecCommand = origCapture
	})

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	listSessionActivityFn = func(ctx context.Context, tmuxPath string) (map[string]tmux.SessionActivity, error) {
		return map[string]tmux.SessionActivity{
			"leo-den": {LastActivity: base},
		}, nil
	}
	captureExecCommand = fakeCaptureCommand("Running go test ./...\n")

	pub := &recordingPublisher{}
	sessions := func() map[string]string { return map[string]string{"den": "leo-den"} }
	tr := NewTracker("tmux", sessions, pub, WithIdleThreshold(15*time.Second), WithClock(func() time.Time { return base }))

	// Act
	tr.sweep(context.Background())

	// Assert
	activities := tr.Activities()
	act, ok := activities["den"]
	if !ok || act.Activity != ActivityWorking {
		t.Fatalf("expected den to be working, got %+v (ok=%v)", act, ok)
	}
	if act.CurrentAction == nil || act.CurrentAction.Detail != "Running go test ./..." {
		t.Fatalf("expected captured pane action, got %+v", act.CurrentAction)
	}
	if len(pub.events) != 1 {
		t.Fatalf("expected exactly one publish for the new working agent, got %d", len(pub.events))
	}
	payload, ok := pub.events[0].Payload.(*AgentActivityPayload)
	if !ok {
		t.Fatalf("expected AgentActivityPayload, got %T", pub.events[0].Payload)
	}
	if payload.Agent != "den" || payload.Activity != ActivityWorking {
		t.Fatalf("unexpected payload: %+v", payload)
	}
}

func TestTrackerSweepDoesNotPublishWhenNothingChanged(t *testing.T) {
	// Arrange
	origList := listSessionActivityFn
	t.Cleanup(func() { listSessionActivityFn = origList })

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	listSessionActivityFn = func(ctx context.Context, tmuxPath string) (map[string]tmux.SessionActivity, error) {
		return map[string]tmux.SessionActivity{"leo-den": {LastActivity: base}}, nil
	}

	pub := &recordingPublisher{}
	sessions := func() map[string]string { return map[string]string{"den": "leo-den"} }
	tr := NewTracker("tmux", sessions, pub, WithIdleThreshold(15*time.Second), WithClock(func() time.Time { return base.Add(30 * time.Second) }))
	// Prime state: agent already known idle at base.
	tr.activities = map[string]AgentActivity{"den": {Activity: ActivityIdle, LastActivityAt: base}}

	// Act: same tmux reading, clock still past threshold — no transition.
	tr.sweep(context.Background())

	// Assert
	if len(pub.events) != 0 {
		t.Fatalf("expected no publish when activity is unchanged, got %d events", len(pub.events))
	}
}

// TestTrackerSweepForgetsAgentRemovedFromSessionMap guards the second half
// of finding #4: when RenameAgent re-keys the supervisor's identity map, the
// old name simply stops appearing in sessionNames() the next sweep. The
// tracker must forget it entirely rather than continuing to report a
// reading for a name that no longer exists — a stale name must never emit
// activity after it's gone.
func TestTrackerSweepForgetsAgentRemovedFromSessionMap(t *testing.T) {
	// Arrange
	origList := listSessionActivityFn
	t.Cleanup(func() { listSessionActivityFn = origList })
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	listSessionActivityFn = func(ctx context.Context, tmuxPath string) (map[string]tmux.SessionActivity, error) {
		return map[string]tmux.SessionActivity{"leo-den": {LastActivity: base}}, nil
	}

	names := map[string]string{"den": "leo-den"}
	sessions := func() map[string]string { return names }
	tr := NewTracker("tmux", sessions, nil, WithClock(func() time.Time { return base }))
	tr.sweep(context.Background())
	if _, ok := tr.Activities()["den"]; !ok {
		t.Fatal("expected den present after first sweep")
	}

	// Act: "den" renamed away — sessionNames() no longer reports it at all
	// (mirrors service.Supervisor.SessionNames after RenameAgent re-keys).
	names = map[string]string{}
	tr.sweep(context.Background())

	// Assert
	if _, ok := tr.Activities()["den"]; ok {
		t.Fatal("expected den forgotten once it disappears from sessionNames()")
	}
}

func TestTrackerSweepMarksUnknownWhenNoSession(t *testing.T) {
	// Arrange
	origList := listSessionActivityFn
	t.Cleanup(func() { listSessionActivityFn = origList })
	listSessionActivityFn = func(ctx context.Context, tmuxPath string) (map[string]tmux.SessionActivity, error) {
		return map[string]tmux.SessionActivity{}, nil
	}

	sessions := func() map[string]string { return map[string]string{"den": "leo-den"} }
	tr := NewTracker("tmux", sessions, nil)

	// Act
	tr.sweep(context.Background())

	// Assert
	act := tr.Activities()["den"]
	if act.Activity != ActivityUnknown {
		t.Fatalf("expected unknown activity with no tmux session, got %s", act.Activity)
	}
}

func TestTrackerStartExitsPromptlyOnCancel(t *testing.T) {
	// Arrange
	origList := listSessionActivityFn
	t.Cleanup(func() { listSessionActivityFn = origList })
	listSessionActivityFn = func(ctx context.Context, tmuxPath string) (map[string]tmux.SessionActivity, error) {
		return map[string]tmux.SessionActivity{}, nil
	}
	tr := NewTracker("tmux", func() map[string]string { return nil }, nil, WithSweepInterval(time.Hour))
	ctx, cancel := context.WithCancel(context.Background())

	// Act
	done := make(chan struct{})
	go func() {
		tr.Start(ctx)
		close(done)
	}()
	cancel()

	// Assert
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not exit promptly after context cancellation")
	}
}
