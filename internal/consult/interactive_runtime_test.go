package consult

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

func TestClaudeInteractiveArgvSingleSettings(t *testing.T) {
	args, err := mergeInteractiveArgs(
		[]string{"--model", "sonnet", "--settings", `{"crossSessionInbound":"accept"}`},
		[]string{"--settings", `{"hooks":{"Stop":[{}],"UserPromptSubmit":[{}],"SessionEnd":[{}]}}`},
	)
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	for i, arg := range args {
		if arg == "--settings" {
			if raw != "" {
				t.Fatalf("args contain more than one --settings: %#v", args)
			}
			raw = args[i+1]
		}
	}
	var settings map[string]any
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		t.Fatal(err)
	}
	if settings["crossSessionInbound"] != "accept" {
		t.Fatalf("settings = %#v", settings)
	}
	hooks := settings["hooks"].(map[string]any)
	for _, event := range []string{"Stop", "UserPromptSubmit", "SessionEnd"} {
		if _, ok := hooks[event]; !ok {
			t.Fatalf("settings hooks = %#v, missing %s", hooks, event)
		}
	}
}

func TestOpeningInjectWaitsForReady(t *testing.T) {
	r := NewInteractiveRuntime("x", nil, nil, "tmux", "/opt/leo")
	r.StartupPollInterval = time.Nanosecond
	r.StartupTimeout = time.Second
	var calls [][]string
	captures := []string{"starting harness\nMCP warning\n", "starting harness\nMCP warning\n", "────\n❯ \n────\n", "────\n❯ \n────\n", "────\n❯ [Pasted text #1 +1 lines]\n────\n"}
	r.ExecCommandContext = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		calls = append(calls, args)
		if slices.Contains(args, "capture-pane") {
			if len(captures) == 0 {
				t.Fatal("unexpected capture")
			}
			capture := captures[0]
			captures = captures[1:]
			return exec.Command("printf", "%s", capture)
		}
		return exec.Command("true")
	}
	if err := r.InjectOpening(context.Background(), "%1", "hello", nil); err != nil {
		t.Fatal(err)
	}
	for i, args := range calls[:3] {
		if !slices.Contains(args, "capture-pane") {
			t.Fatalf("call %d before ready wrote keys: %#v", i, args)
		}
	}
	if slices.Contains(calls[0], "send-keys") || slices.Contains(calls[1], "send-keys") || slices.Contains(calls[2], "send-keys") {
		t.Fatalf("keystrokes before ready: %#v", calls[:3])
	}
}

func TestOpeningInjectTimeoutKeepsPane(t *testing.T) {
	r := NewInteractiveRuntime("x", nil, nil, "tmux", "/opt/leo")
	r.StartupPollInterval = time.Nanosecond
	r.StartupTimeout = time.Millisecond
	r.ExecCommandContext = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		if slices.Contains(args, "capture-pane") {
			return exec.Command("printf", "%s", "booting\nMCP warning\nwaiting for auth\n")
		}
		return exec.Command("true")
	}
	err := r.InjectOpening(context.Background(), "%1", "hello", nil)
	var notReady *ErrNotReady
	if !errors.As(err, &notReady) || !strings.Contains(err.Error(), "MCP warning") || !strings.Contains(err.Error(), "waiting for auth") {
		t.Fatalf("error = %v, want ErrNotReady with screen excerpt", err)
	}

	d := NewDispatcher(newFakeRecorder())
	rt := &fakeInteractiveRuntime{injectErr: err}
	d.SetInteractiveRuntime(rt)
	started, startErr := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "hello", Cwd: t.TempDir(), Mode: ModeInteractive})
	if startErr != nil {
		t.Fatalf("Start() = %v", startErr)
	}
	entry := d.Wait(context.Background(), []string{started.ID + "#1"}, time.Second)[0]
	if entry.Status != StatusFailed || entry.Outcome != TurnRejected || rt.kill != 0 {
		t.Fatalf("entry=%+v kill=%d; failed opening must settle asynchronously without killing pane", entry, rt.kill)
	}
}

func TestSendDoesNotWait(t *testing.T) {
	r := NewInteractiveRuntime("x", nil, nil, "tmux", "/opt/leo")
	var captures int
	r.ExecCommandContext = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		if slices.Contains(args, "capture-pane") {
			captures++
			return exec.Command("printf", "%s", "starting harness\n")
		}
		return exec.Command("true")
	}
	err := r.Inject(context.Background(), "%1", "hello", nil)
	if !errors.Is(err, tmux.ErrComposerUnknown) || captures != 1 {
		t.Fatalf("Inject = %v after %d captures, want immediate unknown rejection", err, captures)
	}
}

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
	if got := launch[len(launch)-1]; strings.Count(got, "--settings") != 1 || !containsAll(got, "crossSessionInbound", "Stop", "UserPromptSubmit", "SessionEnd") {
		t.Fatalf("Claude interactive settings were not merged: %q", got)
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

func TestInteractiveLaunchPreparesCodexHome(t *testing.T) {
	t.Run("template CODEX_HOME", func(t *testing.T) {
		codexHome := t.TempDir()
		r := interactiveCodexRuntime(t, &config.Config{HomePath: t.TempDir(), Templates: map[string]config.TemplateConfig{
			"codex": {Harness: "codex", Env: map[string]string{"CODEX_HOME": codexHome}},
		}})
		if _, _, err := r.Launch(context.Background(), LaunchRequest{ID: "d-home", Template: "codex", Cwd: t.TempDir()}); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(codexHome, "hooks.json")); err != nil {
			t.Fatalf("hooks.json in template CODEX_HOME: %v", err)
		}
	})

	t.Run("daemon environment fallback", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("CODEX_HOME", "")
		r := interactiveCodexRuntime(t, &config.Config{HomePath: t.TempDir(), Templates: map[string]config.TemplateConfig{
			"codex": {Harness: "codex"},
		}})
		if _, _, err := r.Launch(context.Background(), LaunchRequest{ID: "d-home", Template: "codex", Cwd: t.TempDir()}); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(home, ".codex", "hooks.json")); err != nil {
			t.Fatalf("hooks.json in default CODEX_HOME: %v", err)
		}
	})
}

func TestInteractiveLaunchTrustsWorkspace(t *testing.T) {
	codexHome := t.TempDir()
	real := filepath.Join(t.TempDir(), "real-workspace")
	if err := os.Mkdir(real, 0o750); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "workspace-link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(link)
	if err != nil {
		t.Fatal(err)
	}
	r := interactiveCodexRuntime(t, &config.Config{HomePath: t.TempDir(), Templates: map[string]config.TemplateConfig{
		"codex": {Harness: "codex", Env: map[string]string{"CODEX_HOME": codexHome}},
	}})
	for range 2 {
		if _, _, err := r.Launch(context.Background(), LaunchRequest{ID: "d-trust", Template: "codex", Cwd: link}); err != nil {
			t.Fatal(err)
		}
	}
	config, err := os.ReadFile(filepath.Join(codexHome, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	header := `[projects."` + resolved + `"]`
	if got := strings.Count(string(config), header); got != 1 {
		t.Fatalf("workspace trust entries = %d, want 1:\n%s", got, config)
	}
}

func interactiveCodexRuntime(t *testing.T, cfg *config.Config) *TmuxInteractiveRuntime {
	t.Helper()
	r := NewInteractiveRuntime("/tmp/leo.yaml", func() (*config.Config, error) { return cfg, nil }, nil, "tmux", "/opt/leo")
	r.ExecCommandContext = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		if slices.Contains(args, "new-window") {
			return exec.Command("echo", "%42")
		}
		return exec.Command("true")
	}
	return r
}

func TestRuntimeAliveKillComposerEmpty(t *testing.T) {
	r := NewInteractiveRuntime("x", nil, nil, "tmux", "/opt/leo")
	r.ExecCommandContext = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		if slices.Contains(args, "display-message") {
			return exec.Command("echo", "0")
		}
		if slices.Contains(args, "capture-pane") {
			return exec.Command("printf", "%s", "────\n❯ \n────\n")
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

func TestRuntimePaneAliveDistinguishesProbeFailureFromAbsence(t *testing.T) {
	r := NewInteractiveRuntime("", nil, nil, "tmux", "leo")
	r.ExecCommandContext = func(context.Context, string, ...string) *exec.Cmd { return exec.Command("false") }
	if alive, err := r.PaneAlive("%1"); err == nil || alive {
		t.Fatalf("PaneAlive = %v, %v; want false with error", alive, err)
	}
	r.ExecCommandContext = func(context.Context, string, ...string) *exec.Cmd { return exec.Command("printf", "1") }
	if alive, err := r.PaneAlive("%1"); err != nil || alive {
		t.Fatalf("PaneAlive absent = %v, %v", alive, err)
	}
	calls := 0
	r.ExecCommandContext = func(context.Context, string, ...string) *exec.Cmd {
		calls++
		if calls == 1 {
			return exec.Command("false")
		}
		return exec.Command("printf", "%%2\n")
	}
	if alive, err := r.PaneAlive("%1"); err != nil || alive {
		t.Fatalf("inventory-confirmed absence = %v, %v", alive, err)
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
