package tmux

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"testing"
)

func TestInjectIntoRejectsWithoutKeys(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()
	var calls [][]string
	execCommand = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		calls = append(calls, args)
		return exec.Command("echo", "busy")
	}
	if err := InjectInto(context.Background(), "tmux", "%1", func(string) ComposerState { return ComposerBusy }, "hello", nil); !errors.Is(err, ErrComposerBusy) {
		t.Fatalf("error = %v", err)
	}
	if len(calls) != 1 || !slices.Contains(calls[0], "capture-pane") {
		t.Fatalf("writes = %#v", calls)
	}
	calls = nil
	if err := InjectInto(context.Background(), "tmux", "%1", func(string) ComposerState { return ComposerUnknown }, "hello", nil); !errors.Is(err, ErrComposerUnknown) {
		t.Fatalf("error = %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("writes = %#v", calls)
	}
}

func TestInjectIntoPasteConfirmArmOrder(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()
	var calls [][]string
	capture := 0
	armed := false
	execCommand = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		calls = append(calls, args)
		if slices.Contains(args, "capture-pane") {
			capture++
			if capture == 1 {
				return exec.Command("echo", "empty")
			}
			return exec.Command("echo", "hello")
		}
		if slices.Contains(args, "send-keys") && !armed {
			t.Fatal("Enter sent before arm")
		}
		return exec.Command("true")
	}
	if err := InjectInto(context.Background(), "tmux", "%1", func(string) ComposerState { return ComposerEmpty }, "hello", func() { armed = true }); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(calls[len(calls)-1], "Enter") {
		t.Fatalf("last call = %#v", calls[len(calls)-1])
	}
}

func TestInjectIntoPasteFailedNoEnter(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()
	origAttempts := submitConfirmAttempts
	submitConfirmAttempts = 1
	defer func() { submitConfirmAttempts = origAttempts }()
	var calls [][]string
	execCommand = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		calls = append(calls, args)
		return exec.Command("echo", "empty")
	}
	err := InjectInto(context.Background(), "tmux", "%1", func(string) ComposerState { return ComposerEmpty }, "hello", nil)
	if !errors.Is(err, ErrPasteFailed) {
		t.Fatalf("error = %v", err)
	}
	for _, c := range calls {
		if slices.Contains(c, "Enter") {
			t.Fatalf("unexpected Enter: %#v", calls)
		}
	}
}
