package consult

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
)

func testConfig() *config.Config {
	return &config.Config{
		Templates: map[string]config.TemplateConfig{
			"gpt": {Harness: "claude"}, // claude adapter: easiest ParseEvents fixture
		},
	}
}

func echoResult(text string) func(ctx context.Context, name string, args ...string) *exec.Cmd {
	return func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "echo", `{"type":"result","result":"`+text+`","is_error":false}`)
	}
}

func TestDispatchUnknownTemplate(t *testing.T) {
	d := NewDispatcher(func(context.Context, string, string) error { return nil })
	_, err := d.Dispatch(testConfig(), Request{From: "caller", Template: "nope", Prompt: "q"})
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("expected unknown-template error, got %v", err)
	}
}

func TestDispatchInvalidModelOverride(t *testing.T) {
	d := NewDispatcher(func(context.Context, string, string) error { return nil })
	_, err := d.Dispatch(testConfig(), Request{From: "caller", Template: "gpt", Model: "gpt-9-nano", Prompt: "q"})
	if err == nil {
		t.Fatal("expected model validation error")
	}
}

func TestDispatchRunsAndDeliversReply(t *testing.T) {
	got := make(chan struct {
		name string
		body string
	}, 1)
	d := NewDispatcher(func(_ context.Context, name, body string) error {
		got <- struct {
			name string
			body string
		}{name, body}
		return nil
	})
	d.ExecCommandContext = echoResult("opinion text")

	tk, err := d.Dispatch(testConfig(), Request{From: "caller", Template: "gpt", Prompt: "q", Workspace: t.TempDir()})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if tk.ID == "" || tk.Harness != "claude" {
		t.Fatalf("unexpected ticket %+v", tk)
	}

	select {
	case r := <-got:
		if r.name != "caller" {
			t.Fatalf("delivered to %q, want caller", r.name)
		}
		if !strings.Contains(r.body, "opinion text") || !strings.Contains(r.body, "[consult "+tk.ID) {
			t.Fatalf("unexpected reply body: %q", r.body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reply never delivered")
	}
}

// TestDispatchBuildsClaudeArgs is the regression guard for the
// --max-turns-0 bug: it exercises the real claude adapter's Args() output
// (not a stub) and asserts a positive --max-turns value along with the
// other flags a consult must carry (and must not carry — no --mcp-config,
// since consultants get no leo tools).
func TestDispatchBuildsClaudeArgs(t *testing.T) {
	cfg := &config.Config{
		Templates: map[string]config.TemplateConfig{
			"gpt": {Harness: "claude", Model: "opus"},
		},
	}

	var gotName string
	var gotArgs []string
	got := make(chan string, 1)
	d := NewDispatcher(func(_ context.Context, _, body string) error {
		got <- body
		return nil
	})
	d.ExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		gotName = name
		gotArgs = args
		return exec.CommandContext(ctx, "echo", `{"type":"result","result":"ok","is_error":false}`)
	}

	_, err := d.Dispatch(cfg, Request{From: "caller", Template: "gpt", Prompt: "what do you think?", Workspace: t.TempDir()})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("reply never delivered")
	}

	if gotName == "" {
		t.Fatal("consultant never exec'd")
	}

	promptIdx := -1
	for i, a := range gotArgs {
		if a == "-p" {
			promptIdx = i
			break
		}
	}
	if promptIdx == -1 || promptIdx+1 >= len(gotArgs) {
		t.Fatalf("no -p flag in args: %v", gotArgs)
	}
	prompt := gotArgs[promptIdx+1]
	if !strings.HasPrefix(prompt, preamble) {
		t.Errorf("prompt does not start with advisory preamble: %q", prompt)
	}
	if !strings.Contains(prompt, "what do you think?") {
		t.Errorf("prompt missing question: %q", prompt)
	}

	assertFlag := func(flag, want string) {
		t.Helper()
		for i, a := range gotArgs {
			if a == flag {
				if i+1 >= len(gotArgs) || gotArgs[i+1] != want {
					t.Errorf("%s = %v, want %q", flag, gotArgs[i+1:], want)
				}
				return
			}
		}
		t.Errorf("missing flag %s in args: %v", flag, gotArgs)
	}
	assertFlag("--model", "opus")
	assertFlag("--max-turns", "15")
	assertFlag("--output-format", "stream-json")

	for _, a := range gotArgs {
		if a == "--mcp-config" {
			t.Errorf("consult args should not include --mcp-config (no leo tools): %v", gotArgs)
		}
	}
}

func TestDispatchFailureDeliversErrorNotice(t *testing.T) {
	got := make(chan string, 1)
	d := NewDispatcher(func(_ context.Context, _, body string) error {
		got <- body
		return nil
	})
	d.ExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "false")
	}

	tk, err := d.Dispatch(testConfig(), Request{From: "caller", Template: "gpt", Prompt: "q", Workspace: t.TempDir()})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	select {
	case body := <-got:
		if !strings.Contains(body, "failed after") || !strings.Contains(body, tk.ID) {
			t.Fatalf("unexpected failure body: %q", body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("failure notice never delivered")
	}
}
