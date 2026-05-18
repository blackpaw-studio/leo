package tmux

import (
	"context"
	"os/exec"
	"reflect"
	"testing"
)

func TestInjectPromptCalls(t *testing.T) {
	var got [][]string
	orig := execCommand
	defer func() { execCommand = orig }()
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		got = append(got, append([]string{name}, args...))
		return exec.Command("true")
	}
	if err := InjectPrompt(context.Background(), "tmux", "leo-session-foo", "hello\nworld"); err != nil {
		t.Fatalf("InjectPrompt: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 tmux calls, got %d: %#v", len(got), got)
	}
	expectSet := []string{"tmux", "-L", "leo", "set-buffer", "-b", "leo", "--", "hello\nworld"}
	expectPaste := []string{"tmux", "-L", "leo", "paste-buffer", "-b", "leo", "-t", "leo-session-foo", "-d"}
	expectEnter := []string{"tmux", "-L", "leo", "send-keys", "-t", "leo-session-foo", "Enter"}
	if !reflect.DeepEqual(got[0], expectSet) {
		t.Fatalf("set-buffer call wrong:\n got %#v\nwant %#v", got[0], expectSet)
	}
	if !reflect.DeepEqual(got[1], expectPaste) {
		t.Fatalf("paste-buffer call wrong:\n got %#v\nwant %#v", got[1], expectPaste)
	}
	if !reflect.DeepEqual(got[2], expectEnter) {
		t.Fatalf("send-keys Enter call wrong:\n got %#v\nwant %#v", got[2], expectEnter)
	}
}

func TestAbortPromptCalls(t *testing.T) {
	var got [][]string
	orig := execCommand
	defer func() { execCommand = orig }()
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		got = append(got, append([]string{name}, args...))
		return exec.Command("true")
	}
	if err := AbortPrompt(context.Background(), "tmux", "leo-session-foo"); err != nil {
		t.Fatalf("AbortPrompt: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(got))
	}
	if got[0][len(got[0])-1] != "Escape" || got[1][len(got[1])-1] != "C-c" {
		t.Fatalf("expected Escape then C-c, got %#v / %#v", got[0], got[1])
	}
}
