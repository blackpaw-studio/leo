package consult

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
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

func TestFindPaneByDispatchID(t *testing.T) {
	r := NewInteractiveRuntime("x", nil, nil, "tmux", "/opt/leo")
	var got []string
	r.ExecCommandContext = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		got = append([]string(nil), args...)
		return exec.Command("printf", "%%8\tenv LEO_DISPATCH_ID=d-other codex\n%%9\tenv LEO_DISPATCH_ID=d-cafe codex\n")
	}
	pane, err := r.FindPaneByDispatchID("@7", "d-cafe")
	if err != nil || pane != "%9" {
		t.Fatalf("FindPaneByDispatchID=%q,%v", pane, err)
	}
	want := tmux.Args("list-panes", "-t", "@7", "-F", "#{pane_id}\t#{pane_start_command}")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv=%#v want=%#v", got, want)
	}
	r.ExecCommandContext = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		return exec.Command("printf", "%%8\tenv LEO_DISPATCH_ID=d-cafe2 codex\n")
	}
	if pane, err := r.FindPaneByDispatchID("@7", "d-cafe"); err != nil || pane != "" {
		t.Fatalf("adopted mismatched dispatch pane=%q err=%v", pane, err)
	}
}

func TestOpeningInjectWaitsForReady(t *testing.T) {
	r := NewInteractiveRuntime("x", nil, nil, "tmux", "/opt/leo")
	r.StartupPollInterval = time.Nanosecond
	r.StartupTimeout = time.Second
	var calls [][]string
	captures := []string{
		"starting harness\nMCP warning\n", "starting harness\nMCP warning\n", "────\n❯ \n────\n", // InjectOpening's own readiness poll.
		"────\n❯ \n────\n",                          // InjectIntoWith's baseline capture, before staging/pasting.
		"────\n❯ [Pasted text #1 +1 lines]\n────\n", // confirm loop: paste placeholder lands.
		"────\n❯ [Pasted text #1 +1 lines]\n────\n", // confirm loop stability re-check: same content.
	}
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
	t.Setenv("HOME", dir)
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

func TestInteractiveClaudeDispatchLaunchProfile(t *testing.T) {
	home := t.TempDir()
	registry := filepath.Join(home, ".claude", "plugins", "installed_plugins.json")
	if err := os.MkdirAll(filepath.Dir(registry), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registry, []byte(`{"plugins":{"b@market":{},"a@local":{}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{HomePath: t.TempDir(), Web: config.WebConfig{Enabled: true}, Delegation: &config.DelegationConfig{ActiveProfile: "p", Profiles: map[string]config.Profile{"p": {Roles: map[string]config.RoleTarget{"implement": {Template: "claude"}}}}}, Templates: map[string]config.TemplateConfig{
		"claude": {Harness: "claude", Model: "sonnet", Env: map[string]string{"HOME": home}},
	}}
	r := NewInteractiveRuntime("/tmp/leo.yaml", func() (*config.Config, error) { return cfg, nil }, nil, "tmux", "/opt/leo")
	var launch []string
	r.ExecCommandContext = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		if slices.Contains(args, "new-window") {
			launch = append([]string(nil), args...)
			return exec.Command("echo", "%42")
		}
		if slices.Contains(args, "has-session") {
			return exec.Command("false")
		}
		return exec.Command("true")
	}
	if _, _, err := r.Launch(context.Background(), LaunchRequest{ID: "d-profile", Template: "claude", Cwd: t.TempDir(), Dispatched: true}); err != nil {
		t.Fatal(err)
	}
	command := launch[len(launch)-1]
	if strings.Contains(command, "--append-system-prompt") {
		t.Fatalf("interactive dispatch leaked system context into %q", command)
	}
	if !containsAll(command, "--strict-mcp-config", "--mcp-config", `{"mcpServers":{"leo":{"command":"leo","args":["mcp-server"]}}}`) {
		t.Fatalf("profile flags missing from %q", command)
	}
	match := regexp.MustCompile(`'--settings' '([^']*)'`).FindStringSubmatch(command)
	if len(match) != 2 {
		t.Fatalf("settings missing from %q", command)
	}
	var settings map[string]any
	if err := json.Unmarshal([]byte(match[1]), &settings); err != nil {
		t.Fatal(err)
	}
	if settings["crossSessionInbound"] != "accept" {
		t.Fatalf("settings = %#v", settings)
	}
	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("hooks = %#v", settings["hooks"])
	}
	for _, event := range []string{"Stop", "UserPromptSubmit", "SessionEnd"} {
		if _, ok := hooks[event]; !ok {
			t.Errorf("missing %s hook: %#v", event, hooks)
		}
	}
	if want := map[string]any{"a@local": false, "b@market": false}; !reflect.DeepEqual(settings["enabledPlugins"], want) {
		t.Fatalf("enabledPlugins = %#v, want %#v", settings["enabledPlugins"], want)
	}
}

func TestInteractiveLaunchFallbackSession(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
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
		if slices.Contains(args, "list-panes") {
			return exec.Command("printf", "%%1\t0")
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

func TestRuntimePanePresenceOutcomes(t *testing.T) {
	r := NewInteractiveRuntime("", nil, nil, "tmux", "leo")
	tests := []struct {
		name string
		cmd  func() *exec.Cmd
		want PanePresence
		err  bool
	}{
		{"alive-multi-pane", func() *exec.Cmd { return exec.Command("printf", "%%2\t0\n%%1\t0\n") }, PanePresentAlive, false},
		{"dead-multi-pane", func() *exec.Cmd { return exec.Command("printf", "%%2\t0\n%%1\t1\n") }, PanePresentDead, false},
		{"absent", func() *exec.Cmd { return exec.Command("sh", "-c", `echo "can't find pane: %1" >&2; exit 1`) }, PaneAbsent, false},
		{"error", func() *exec.Cmd { return exec.Command("sh", "-c", `echo "socket unavailable" >&2; exit 1`) }, PaneAbsent, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r.ExecCommandContext = func(context.Context, string, ...string) *exec.Cmd { return tt.cmd() }
			got, err := r.PanePresence("%1")
			if got != tt.want || (err != nil) != tt.err {
				t.Fatalf("PanePresence=%v,%v want=%v err=%v", got, err, tt.want, tt.err)
			}
		})
	}
}

func TestStartCommandHasDispatchIDExactTokens(t *testing.T) {
	tests := []struct {
		name, command string
		want          bool
	}{
		{"interactive", `'env' 'LEO_DISPATCH_ID=d-abc' 'codex'`, true},
		{"headless", `'/opt/leo' dispatch watch d-abc`, true},
		{"prompt-id", `'codex' 'please inspect d-abc before replying'`, false},
		{"prompt-sequence", `'codex' 'please run dispatch watch d-abc later'`, false},
		{"different-env", `'env' 'LEO_DISPATCH_ID=d-abc2' 'codex'`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := startCommandHasDispatchID(tt.command, "d-abc"); got != tt.want {
				t.Fatalf("match=%v want=%v command=%q", got, tt.want, tt.command)
			}
		})
	}
}

func TestInteractiveViewerSessionOverrides(t *testing.T) {
	r := NewInteractiveRuntime("", nil, nil, "tmux", "leo")
	var calls [][]string
	r.ExecCommandContext = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string(nil), args...))
		return exec.Command("printf", "@leo_viewer_placement window\\n@leo_viewer_max_panes 5\\n")
	}
	got := r.ViewerOverrides(context.Background(), "$1")
	if got.Placement != "window" || got.MaxPanes != 5 {
		t.Fatalf("overrides=%+v", got)
	}
	want := [][]string{{"-L", "leo", "show-options", "-t", "$1"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%#v", calls)
	}
}

func TestSessionAliveDistinguishesMissingFromTmuxFailure(t *testing.T) {
	r := NewInteractiveRuntime("", nil, nil, "tmux", "leo")
	r.ExecCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "echo \"can't find session: x\" >&2; exit 1")
	}
	if alive, err := r.SessionAlive("$1"); err != nil || alive {
		t.Fatalf("missing=%v,%v", alive, err)
	}
	r.ExecCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "echo permission denied >&2; exit 1")
	}
	if alive, err := r.SessionAlive("$1"); err == nil || alive {
		t.Fatalf("failure=%v,%v", alive, err)
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

func TestInteractiveSplitArgv(t *testing.T) {
	codexHome, cwd := t.TempDir(), t.TempDir()
	cfg := &config.Config{HomePath: t.TempDir(), Templates: map[string]config.TemplateConfig{"codex": {Harness: "codex", Model: "gpt-5", Env: map[string]string{"CODEX_HOME": codexHome}}}}
	r := NewInteractiveRuntime("/tmp/leo.yaml", func() (*config.Config, error) { return cfg, nil }, nil, "tmux", "/opt/leo")
	var calls [][]string
	r.ExecCommandContext = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string(nil), args...))
		if len(args) > 2 && args[2] == "split-window" {
			return exec.Command("printf", "%%9\\n")
		}
		return exec.Command("true")
	}
	placement := ViewerPlacement{Kind: "split", Target: "%1", MainPaneHeight: 60}
	if _, _, err := r.Launch(context.Background(), LaunchRequest{ID: "d-abc", Template: "codex", Cwd: cwd, Name: "work", CallerPaneID: "%1", CallerWindowID: "@1", Placement: placement}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 8 {
		t.Fatalf("calls=%#v", calls)
	}
	if !reflect.DeepEqual(calls[0], []string{"-L", "leo", "has-session", "-t", "=leo-dispatch"}) {
		t.Fatalf("probe=%#v", calls[0])
	}
	if !reflect.DeepEqual(calls[1], []string{"-L", "leo", "set-window-option", "-t", "@1", "remain-on-exit", "on"}) {
		t.Fatalf("pre-split remain-on-exit=%#v", calls[1])
	}
	launch := calls[2]
	wantPrefix := []string{"-L", "leo", "split-window", "-d", "-P", "-F", "#{pane_id}", "-t", "%1", "-c", cwd, "-e", "CODEX_HOME=" + codexHome, "-e", "LEO_CONFIG=/tmp/leo.yaml", "-e", "LEO_DISPATCH_ID=d-abc"}
	resolved, _ := filepath.EvalSymlinks(cwd)
	wantCommand := "'env' 'LEO_DISPATCH_ID=d-abc' 'codex' '-a' 'never' '--model' 'gpt-5' '-c' 'sandbox_workspace_write.writable_roots=[\"" + resolved + "/.agents\"]' '-c' 'check_for_update_on_startup=false'"
	wantLaunch := append(append([]string(nil), wantPrefix...), wantCommand)
	if !reflect.DeepEqual(launch, wantLaunch) {
		t.Fatalf("launch=%#v", launch)
	}
	wantTail := [][]string{{"-L", "leo", "select-pane", "-t", "%9", "-T", "work·abc"}, {"-L", "leo", "set-option", "-p", "-t", "%9", "remain-on-exit", "on"}, {"-L", "leo", "set-window-option", "-u", "-t", "@1", "remain-on-exit"}, {"-L", "leo", "set-option", "-w", "-t", "@1", "main-pane-height", "60%"}, {"-L", "leo", "select-layout", "-t", "@1", "main-horizontal"}}
	if !reflect.DeepEqual(calls[3:], wantTail) {
		t.Fatalf("calls=%#v\nwant tail=%#v", calls, wantTail)
	}
}

func TestInteractiveSplitFallbackArgv(t *testing.T) {
	codexHome, cwd := t.TempDir(), t.TempDir()
	cfg := &config.Config{HomePath: t.TempDir(), Templates: map[string]config.TemplateConfig{"codex": {Harness: "codex", Env: map[string]string{"CODEX_HOME": codexHome}}}}
	r := NewInteractiveRuntime("/tmp/leo.yaml", func() (*config.Config, error) { return cfg, nil }, nil, "tmux", "/opt/leo")
	var calls [][]string
	r.ExecCommandContext = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string(nil), args...))
		if len(args) > 2 && args[2] == "split-window" {
			return exec.Command("false")
		}
		if len(args) > 2 && args[2] == "new-window" {
			return exec.Command("printf", "%%8\\n")
		}
		return exec.Command("true")
	}
	pane, _, err := r.Launch(context.Background(), LaunchRequest{ID: "d-abc", Template: "codex", Cwd: cwd, CallerPaneID: "%1", Placement: ViewerPlacement{Kind: "split", Target: "%1", MainPaneHeight: 60}})
	if err != nil {
		t.Fatal(err)
	}
	resolved, _ := filepath.EvalSymlinks(cwd)
	command := "'env' 'LEO_DISPATCH_ID=d-abc' 'codex' '-a' 'never' '--model' 'sonnet' '-c' 'sandbox_workspace_write.writable_roots=[\"" + resolved + "/.agents\"]' '-c' 'check_for_update_on_startup=false'"
	suffix := []string{"-c", cwd, "-e", "CODEX_HOME=" + codexHome, "-e", "LEO_CONFIG=/tmp/leo.yaml", "-e", "LEO_DISPATCH_ID=d-abc", command}
	want := [][]string{
		{"-L", "leo", "has-session", "-t", "=leo-dispatch"},
		{"-L", "leo", "set-window-option", "-t", "", "remain-on-exit", "on"},
		append([]string{"-L", "leo", "split-window", "-d", "-P", "-F", "#{pane_id}", "-t", "%1"}, suffix...),
		{"-L", "leo", "set-window-option", "-u", "-t", "", "remain-on-exit"},
		append([]string{"-L", "leo", "new-window", "-d", "-P", "-F", "#{pane_id}", "-t", "=leo-dispatch", "-n", "codex·abc"}, suffix...),
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%#v\nwant=%#v", calls, want)
	}
	if got := r.ViewerKind(pane); got != "window" {
		t.Fatalf("ViewerKind=%q", got)
	}
}

func TestInteractiveSplitEmptyPaneIDUnsetsWindowOption(t *testing.T) {
	codexHome, cwd := t.TempDir(), t.TempDir()
	cfg := &config.Config{HomePath: t.TempDir(), Templates: map[string]config.TemplateConfig{"codex": {Harness: "codex", Env: map[string]string{"CODEX_HOME": codexHome}}}}
	r := NewInteractiveRuntime("/tmp/leo.yaml", func() (*config.Config, error) { return cfg, nil }, nil, "tmux", "/opt/leo")
	var calls [][]string
	r.ExecCommandContext = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string(nil), args...))
		if len(args) > 2 && args[2] == "split-window" {
			// Succeeds but reports no pane id.
			return exec.Command("true")
		}
		return exec.Command("true")
	}
	_, _, err := r.Launch(context.Background(), LaunchRequest{ID: "d-abc", Template: "codex", Cwd: cwd, CallerPaneID: "%1", CallerWindowID: "@1", Placement: ViewerPlacement{Kind: "split", Target: "%1", MainPaneHeight: 60}})
	if err == nil || err.Error() != "tmux returned no pane id" {
		t.Fatalf("err=%v", err)
	}
	resolved, _ := filepath.EvalSymlinks(cwd)
	command := "'env' 'LEO_DISPATCH_ID=d-abc' 'codex' '-a' 'never' '--model' 'sonnet' '-c' 'sandbox_workspace_write.writable_roots=[\"" + resolved + "/.agents\"]' '-c' 'check_for_update_on_startup=false'"
	want := [][]string{
		{"-L", "leo", "has-session", "-t", "=leo-dispatch"},
		{"-L", "leo", "set-window-option", "-t", "@1", "remain-on-exit", "on"},
		{"-L", "leo", "split-window", "-d", "-P", "-F", "#{pane_id}", "-t", "%1", "-c", cwd, "-e", "CODEX_HOME=" + codexHome, "-e", "LEO_CONFIG=/tmp/leo.yaml", "-e", "LEO_DISPATCH_ID=d-abc", command},
		{"-L", "leo", "set-window-option", "-u", "-t", "@1", "remain-on-exit"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%#v\nwant=%#v", calls, want)
	}
}

func TestInteractiveSplitCloseLayout(t *testing.T) {
	r := NewInteractiveRuntime("", nil, nil, "tmux", "leo")
	var calls [][]string
	r.ExecCommandContext = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string(nil), args...))
		return exec.Command("true")
	}
	if err := r.Kill("%9"); err != nil {
		t.Fatal(err)
	}
	if err := r.ReapplyLayout("%1"); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"-L", "leo", "kill-pane", "-t", "%9"}, {"-L", "leo", "select-layout", "-t", "%1", "main-horizontal"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%#v want=%#v", calls, want)
	}
}
