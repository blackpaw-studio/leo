package consult

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"testing"
)

func TestNotificationReadyUsesExactPaneAndSessionArgvWithoutDraftKeys(t *testing.T) {
	var calls [][]string
	command := func(_ context.Context, name string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string{name}, args...))
		if len(calls) == 1 {
			return exec.Command("printf", "$9")
		}
		return exec.Command("printf", "› existing draft")
	}
	d := NewTmuxNotificationDelivery("/opt/tmux", command)
	r := Record{CallerPaneID: "%7", CallerSessionID: "$9", CallerHarness: "codex"}
	if d.Ready(context.Background(), r) {
		t.Fatal("draft reported ready")
	}
	want := []string{"/opt/tmux", "-L", "leo", "display-message", "-p", "-t", "%7", "#{session_id}"}
	if !reflect.DeepEqual(calls[0], want) {
		t.Fatalf("argv=%q want=%q", calls[0], want)
	}
	if len(calls) != 2 || calls[1][3] != "capture-pane" {
		t.Fatalf("calls=%q", calls)
	}
}

func TestNotificationDeliverComposerRaceIsProvenNotSent(t *testing.T) {
	call := 0
	d := NewTmuxNotificationDelivery("tmux", func(context.Context, string, ...string) *exec.Cmd {
		call++
		if call == 1 {
			return exec.Command("printf", "$1")
		}
		return exec.Command("printf", "• Working · esc to interrupt\n›")
	})
	err := d.Deliver(context.Background(), Record{CallerPaneID: "%1", CallerSessionID: "$1", CallerHarness: "codex"}, "line")
	if !errors.Is(err, ErrNotificationNotSent) {
		t.Fatalf("error=%v", err)
	}
}

func TestNotificationReadyRejectsStaleCallerPaneBeforeCapture(t *testing.T) {
	var calls int
	d := NewTmuxNotificationDelivery("tmux", func(context.Context, string, ...string) *exec.Cmd { calls++; return exec.Command("printf", "$new") })
	if d.Ready(context.Background(), Record{CallerPaneID: "%7", CallerSessionID: "$old", CallerHarness: "codex"}) {
		t.Fatal("stale pane ready")
	}
	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
}
