package consult

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

func testConfig() *config.Config {
	return &config.Config{Templates: map[string]config.TemplateConfig{
		"claude":   {Harness: "claude", Model: "opus"},
		"codex":    {Harness: "codex", Model: "gpt-5.3-codex"},
		"opencode": {Harness: "opencode", Model: "anthropic/claude-sonnet-4-5"},
	}}
}

func TestConsultValidation(t *testing.T) {
	d := NewDispatcher(nil)
	if _, err := d.Consult(context.Background(), testConfig(), Request{Template: "nope", Prompt: "q"}); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("expected unknown-template error, got %v", err)
	}
	if _, err := d.Consult(context.Background(), testConfig(), Request{Template: "claude", Model: "gpt-9-nano", Prompt: "q"}); err == nil {
		t.Fatal("expected model validation error")
	}
}

func TestConsultAllHarnessesReturnSynchronousResult(t *testing.T) {
	tests := []struct {
		name       string
		output     string
		wantBinary string
		wantArgs   []string
	}{
		{"claude", `{"type":"result","result":"claude opinion","is_error":false}`, "claude", []string{"-p", "--output-format", "stream-json"}},
		{"codex", `{"type":"thread.started","thread_id":"t1"}
{"type":"item.completed","item":{"type":"agent_message","text":"codex opinion"}}`, "codex", []string{"exec", "--json", "--skip-git-repo-check"}},
		{"opencode", `{"type":"text","sessionID":"s1","part":{"text":"opencode opinion"}}`, "opencode", []string{"run", "--format", "json"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := NewDispatcher(nil)
			var gotBinary string
			var gotArgs []string
			d.ExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
				gotBinary, gotArgs = name, append([]string(nil), args...)
				return exec.CommandContext(ctx, "echo", tt.output)
			}
			result, err := d.Consult(context.Background(), testConfig(), Request{
				Template: tt.name, Prompt: "what do you think?", Workspace: t.TempDir(),
			})
			if err != nil {
				t.Fatalf("Consult: %v", err)
			}
			if result.Harness != tt.name || !strings.Contains(result.Text, tt.name+" opinion") {
				t.Fatalf("unexpected result %+v", result)
			}
			if !strings.Contains(gotBinary, tt.wantBinary) {
				t.Errorf("binary %q", gotBinary)
			}
			joined := strings.Join(gotArgs, " ")
			for _, want := range tt.wantArgs {
				if !strings.Contains(joined, want) {
					t.Errorf("args %v missing %q", gotArgs, want)
				}
			}
			if !strings.Contains(joined, preamble) || !strings.Contains(joined, "what do you think?") {
				t.Errorf("prompt missing from args: %v", gotArgs)
			}
		})
	}
}

func TestConsultReturnsExecutionFailure(t *testing.T) {
	d := NewDispatcher(nil)
	d.ExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "false")
	}
	_, err := d.Consult(context.Background(), testConfig(), Request{Template: "claude", Prompt: "q", Workspace: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "failed") {
		t.Fatalf("expected execution failure, got %v", err)
	}
}

func TestConsultHonorsCallerCancellationWhileQueued(t *testing.T) {
	d := NewDispatcher(nil)
	for range maxConcurrent {
		d.sem <- struct{}{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := d.Consult(ctx, testConfig(), Request{Template: "claude", Prompt: "q"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}
