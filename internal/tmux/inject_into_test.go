package tmux

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"testing"
)

const claudeEmptyComposer = "─\n❯\n─"

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
			return exec.Command("echo", "─\n❯ hello\n─")
		}
		if slices.Contains(args, "send-keys") && !armed {
			t.Fatal("Enter sent before arm")
		}
		return exec.Command("true")
	}
	if err := InjectInto(context.Background(), "tmux", "%1", func(string) ComposerState { return ComposerEmpty }, "hello", func() error { armed = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(calls[len(calls)-1], "Enter") {
		t.Fatalf("last call = %#v", calls[len(calls)-1])
	}
}

func TestInjectIntoUsesNamedBufferAndConfirmsMultilineCollapsedPaste(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()
	var calls [][]string
	captures := 0
	execCommand = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		calls = append(calls, args)
		if slices.Contains(args, "capture-pane") {
			captures++
			if captures == 1 {
				return exec.Command("echo", "empty")
			}
			return exec.Command("echo", "─\n❯ [Pasted text #1 +4 lines]\n─")
		}
		return exec.Command("true")
	}
	text := "first line is distinctive enough for confirmation\nsecond\nthird\nfourth\nfifth"
	if err := InjectInto(context.Background(), "tmux", "%1", func(string) ComposerState { return ComposerEmpty }, text, nil); err != nil {
		t.Fatal(err)
	}
	var buffer string
	for _, call := range calls {
		if slices.Contains(call, "set-buffer") {
			i := slices.Index(call, "set-buffer")
			if len(call) < i+4 || call[i+1] != "-b" {
				t.Fatalf("set-buffer call = %#v", call)
			}
			buffer = call[i+2]
		}
		if slices.Contains(call, "paste-buffer") {
			i := slices.Index(call, "paste-buffer")
			if len(call) < i+5 || call[i+1] != "-b" || call[i+2] != buffer || !slices.Contains(call, "-d") {
				t.Fatalf("paste-buffer call = %#v, buffer = %q", call, buffer)
			}
		}
	}
	if buffer == "" {
		t.Fatal("no named buffer")
	}
}

func TestInjectIntoArmErrorPreventsEnter(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()
	var calls [][]string
	captures := 0
	execCommand = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		calls = append(calls, args)
		if slices.Contains(args, "capture-pane") {
			captures++
			if captures == 1 {
				return exec.Command("echo", "empty")
			}
			return exec.Command("echo", "─\n❯ hello\n─")
		}
		return exec.Command("true")
	}
	err := InjectInto(context.Background(), "tmux", "%1", func(string) ComposerState { return ComposerEmpty }, "hello", func() error { return errors.New("settled") })
	if err == nil {
		t.Fatal("arm error accepted")
	}
	for _, call := range calls {
		if slices.Contains(call, "Enter") {
			t.Fatalf("unexpected enter: %#v", calls)
		}
	}
}

func TestInjectIntoDoesNotConfirmMessageOnlyInHistory(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()
	origAttempts := injectConfirmAttempts
	injectConfirmAttempts = 1
	defer func() { injectConfirmAttempts = origAttempts }()
	captures := 0
	execCommand = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		if slices.Contains(args, "capture-pane") {
			captures++
			if captures == 1 {
				return exec.Command("echo", claudeEmptyComposer)
			}
			return exec.Command("echo", "hello appears in history\n"+claudeEmptyComposer)
		}
		return exec.Command("true")
	}
	err := InjectInto(context.Background(), "tmux", "%1", ClaudeComposerClassifier, "hello", nil)
	if !errors.Is(err, ErrPasteFailed) {
		t.Fatalf("error = %v, want paste confirmation failure", err)
	}
}

func TestInjectIntoDeletesNamedBufferWhenPasteFails(t *testing.T) {
	var calls [][]string
	command := func(_ context.Context, _ string, args ...string) *exec.Cmd {
		calls = append(calls, args)
		if slices.Contains(args, "capture-pane") {
			return exec.Command("echo", claudeEmptyComposer)
		}
		if slices.Contains(args, "paste-buffer") {
			return exec.Command("false")
		}
		return exec.Command("true")
	}
	err := InjectIntoWith(context.Background(), "tmux", "%1", ClaudeComposerClassifier, "hello", nil, command)
	if err == nil {
		t.Fatal("paste failure accepted")
	}
	var buffer string
	for _, call := range calls {
		if i := slices.Index(call, "set-buffer"); i >= 0 {
			buffer = call[i+2]
		}
	}
	if buffer == "" {
		t.Fatal("set-buffer was not called")
	}
	for _, call := range calls {
		if i := slices.Index(call, "delete-buffer"); i >= 0 && slices.Contains(call, buffer) {
			return
		}
	}
	t.Fatalf("buffer %q was not deleted after paste failure: %#v", buffer, calls)
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
