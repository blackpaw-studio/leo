package consult

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A follow-up the dispatch's claude never took, still pending when the
// dispatch ends, reads as a notice in its turn result, not as the bare
// message, so its orchestrator knows it never ran. A turn claude did take
// keeps its own text.
func TestAnUndeliveredFollowUpEndsWithANotice(t *testing.T) {
	d := NewDispatcher(newFakeRecorder())
	rt := &fakeInteractiveRuntime{arm: true, empty: true}
	d.SetInteractiveRuntime(rt)
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "x", Cwd: t.TempDir(), Mode: ModeInteractive})
	if err != nil {
		t.Fatal(err)
	}
	_ = d.Report(started.ID, hook(t, "UserPromptSubmit", "a"))
	_ = d.Report(started.ID, hook(t, "Stop", "a"))
	const followUp = "please also fix the docs"
	sent, err := d.Send(context.Background(), started.ID, followUp)
	if err != nil {
		t.Fatal(err)
	}
	if sent.Delivered {
		t.Fatal("follow-up delivered before claude took it")
	}

	if _, err := d.Cancel(started.ID); err != nil {
		t.Fatal(err)
	}

	rec, _ := d.Get(started.ID)
	turn := turnByID(rec, sent.TurnID)
	if turn.Outcome == "" || turn.Delivered {
		t.Fatalf("follow-up turn after the dispatch ended: %+v", turn)
	}
	body := interactiveEntry(rec, sent.TurnID, time.Now()).Text
	if !strings.Contains(body, "not delivered") || !strings.Contains(body, followUp) {
		t.Fatalf("follow-up turn result = %q, want a not-delivered notice quoting it", body)
	}
}

// A turn its session took and was still running when the dispatch ended
// was delivered: it keeps its own text, with no notice.
func TestATakenTurnEndsWithoutANotice(t *testing.T) {
	d := NewDispatcher(newFakeRecorder())
	rt := &fakeInteractiveRuntime{arm: true, empty: true}
	d.SetInteractiveRuntime(rt)
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "x", Cwd: t.TempDir(), Mode: ModeInteractive})
	if err != nil {
		t.Fatal(err)
	}
	_ = d.Report(started.ID, hook(t, "UserPromptSubmit", "a"))
	before, _ := d.Get(started.ID)
	opening := turnByID(before, started.ID+"#1")
	if !opening.Delivered || opening.Outcome != "" {
		t.Fatalf("opening before the end: %+v", opening)
	}

	if _, err := d.Cancel(started.ID); err != nil {
		t.Fatal(err)
	}

	rec, _ := d.Get(started.ID)
	if got := turnByID(rec, opening.TurnID).Text; got != opening.Text {
		t.Fatalf("taken turn text = %q, want its own %q", got, opening.Text)
	}
}
