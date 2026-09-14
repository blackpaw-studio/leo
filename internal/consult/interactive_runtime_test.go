package consult

import (
	"context"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

func TestInteractiveLaunchArgv(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{HomePath: dir, Templates: map[string]config.TemplateConfig{"claude": {Harness: "claude", Model: "sonnet", Env: map[string]string{"THING": "value"}}}}
	r := NewInteractiveRuntime("/tmp/leo.yaml", func() (*config.Config, error) { return cfg, nil }, func(string) (string, bool) { return "leo-caller", true }, "tmux", "/opt/leo")
	r.AgentToken = "token"
	var calls [][]string
	r.ExecCommandContext = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		calls = append(calls, args)
		if slices.Contains(args, "new-window") {
			return exec.Command("echo", "%42")
		}
		return exec.Command("true")
	}
	pane, _, err := r.Launch(context.Background(), LaunchRequest{ID: "d-deadbeef", Template: "claude", Caller: "caller", Cwd: dir, Name: "work"})
	if err != nil || pane != "%42" {
		t.Fatalf("Launch = %q, %v", pane, err)
	}
	var launch []string
	for _, c := range calls {
		if slices.Contains(c, "new-window") {
			launch = c
		}
	}
	if !slices.Contains(launch, "#{pane_id}") || !slices.Contains(launch, "LEO_DISPATCH_ID=d-deadbeef") || !slices.Contains(launch, "LEO_API_TOKEN=token") {
		t.Fatalf("launch argv = %#v", launch)
	}
	if got := launch[len(launch)-1]; !containsAll(got, "/opt/leo --config /tmp/leo.yaml dispatch report") {
		t.Fatalf("hook command missing from %q", got)
	}
}

func TestInteractiveLaunchFallbackSession(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{HomePath: dir, Templates: map[string]config.TemplateConfig{"claude": {Harness: "claude"}}}
	r := NewInteractiveRuntime("/tmp/leo.yaml", func() (*config.Config, error) { return cfg, nil }, func(string) (string, bool) { return "leo-dead", true }, "tmux", "/opt/leo")
	var calls [][]string
	r.ExecCommandContext = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		calls = append(calls, args)
		if slices.Contains(args, "new-window") {
			return exec.Command("echo", "%2")
		}
		if slices.Contains(args, "has-session") {
			return exec.Command("false")
		}
		return exec.Command("true")
	}
	_, _, err := r.Launch(context.Background(), LaunchRequest{ID: "d-feed", Template: "claude", Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range calls {
		if slices.Contains(c, "new-window") && slices.Contains(c, "=leo-dispatch") {
			found = true
		}
	}
	if !found {
		t.Fatalf("fallback launch missing: %#v", calls)
	}
}

func TestRuntimeAliveKillComposerEmpty(t *testing.T) {
	r := NewInteractiveRuntime("x", nil, nil, "tmux", "/opt/leo")
	r.ExecCommandContext = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		if slices.Contains(args, "display-message") {
			return exec.Command("echo", "0")
		}
		if slices.Contains(args, "capture-pane") {
			return exec.Command("echo", "❯ ")
		}
		return exec.Command("true")
	}
	if !r.Alive("%1") || !r.ComposerEmpty("%1") {
		t.Fatal("runtime probes failed")
	}
	if err := r.Kill("%1"); err != nil {
		t.Fatal(err)
	}
}

func containsAll(s string, words ...string) bool {
	for _, word := range words {
		if !contains(s, word) {
			return false
		}
	}
	return true
}
func contains(s, want string) bool { return strings.Contains(s, want) }
