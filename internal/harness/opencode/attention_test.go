package opencode

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// useTurnPluginDir points the turn plugin at a temp dir for one test.
func useTurnPluginDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "plugins")
	prev := turnPluginDir
	turnPluginDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { turnPluginDir = prev })
	return dir
}

func configPlugins(t *testing.T, env map[string]string) []string {
	t.Helper()
	raw, ok := env["OPENCODE_CONFIG_CONTENT"]
	if !ok {
		return nil
	}
	var cfg struct {
		Plugin []string `json:"plugin"`
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("config content is not JSON: %v\n%s", err, raw)
	}
	return cfg.Plugin
}

func TestOpencodeAttentionLaunchInstallsTurnPlugin(t *testing.T) {
	dir := useTurnPluginDir(t)
	hooker, ok := (Opencode{}).Driver().(harness.AttentionHooker)
	if !ok || !hooker.AttentionSupported() {
		t.Fatal("opencode driver does not support attention hooks")
	}

	args := []string{"--model", "lmstudio/qwen"}
	got, supported, err := hooker.AttentionLaunch(harness.SessionHandle{}, args, []string{"/opt/leo", "dispatch", "report"})
	if err != nil || !supported || !reflect.DeepEqual(got, args) {
		t.Fatalf("AttentionLaunch = %#v supported=%v err=%v; want argv unchanged, supported", got, supported, err)
	}

	env, err := Opencode{}.Env(harness.LaunchSpec{Kind: harness.KindAgent, Options: Options{}})
	if err != nil {
		t.Fatal(err)
	}
	plugins := configPlugins(t, env)
	if len(plugins) != 2 {
		t.Fatalf("plugin = %#v, want the shell env plugin then the leo turn plugin", plugins)
	}
	path, err := pluginPath(turnPluginPrefix, turnPluginSource)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != dir || plugins[1] != "file://"+path {
		t.Fatalf("plugin = %q, want file://%s under %s", plugins[1], path, dir)
	}
	written, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(written, turnPluginSource) {
		t.Fatalf("plugin file at %s: err=%v, content matches=%v", path, err, bytes.Equal(written, turnPluginSource))
	}
}

func TestOpencodeTurnPluginOnlyForSupervisedAgents(t *testing.T) {
	useTurnPluginDir(t)
	turnPath, err := pluginPath(turnPluginPrefix, turnPluginSource)
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range []harness.LaunchSpec{
		{Kind: harness.KindTask, Options: Options{}},
		{Kind: harness.KindAgent, Dispatched: true, Options: Options{}},
	} {
		env, err := Opencode{}.Env(spec)
		if err != nil {
			t.Fatal(err)
		}
		for _, plugin := range configPlugins(t, env) {
			if plugin == "file://"+turnPath {
				t.Errorf("kind=%s dispatched=%v: turn plugin installed", spec.Kind, spec.Dispatched)
			}
		}
	}
}

// Agent and dispatch panes live in leo's tmux server, so a bare
// `tmux kill-server` in their shell would target it. The shell env plugin is
// installed for every agent launch, dispatched ones included, and for no task
// (a oneshot task has no pane).
func TestOpencodeShellEnvPluginForEveryAgentLaunch(t *testing.T) {
	dir := useTurnPluginDir(t)
	shellPath, err := pluginPath(shellEnvPluginPrefix, shellEnvPluginSource)
	if err != nil {
		t.Fatal(err)
	}
	want := "file://" + shellPath
	for _, tc := range []struct {
		name string
		spec harness.LaunchSpec
		want bool
	}{
		{"agent", harness.LaunchSpec{Kind: harness.KindAgent, Options: Options{}}, true},
		{"dispatched agent", harness.LaunchSpec{Kind: harness.KindAgent, Dispatched: true, Options: Options{}}, true},
		{"task", harness.LaunchSpec{Kind: harness.KindTask, Options: Options{}}, false},
	} {
		env, err := Opencode{}.Env(tc.spec)
		if err != nil {
			t.Fatal(err)
		}
		got := slices.Contains(configPlugins(t, env), want)
		if got != tc.want {
			t.Errorf("%s: shell env plugin installed = %v, want %v (plugins %#v)", tc.name, got, tc.want, configPlugins(t, env))
		}
	}
	if filepath.Dir(shellPath) != dir {
		t.Errorf("plugin path %s not under %s", shellPath, dir)
	}
	written, err := os.ReadFile(shellPath)
	if err != nil || !bytes.Equal(written, shellEnvPluginSource) {
		t.Fatalf("plugin file at %s: err=%v, content matches=%v", shellPath, err, bytes.Equal(written, shellEnvPluginSource))
	}
}

// opencode builds a shell command's env as {...process.env, ...hook env}, so
// deleting a key from the hook's env strips nothing: the variables must be
// overridden with empty strings. tmux ignores an empty TMUX, so the command
// falls back to $TMUX_TMPDIR's default socket instead of leo's server.
func TestOpencodeShellEnvPluginBlanksTmuxInTheShellEnvironment(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	plugin := filepath.Join(t.TempDir(), "leo-shellenv.mjs")
	if err := os.WriteFile(plugin, shellEnvPluginSource, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, filepath.Join("testdata", "shell_env_plugin_harness.mjs"), plugin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("plugin harness: %v\n%s", err, out)
	}
	var got map[string]string
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("harness output is not JSON: %v\n%s", err, out)
	}
	want := map[string]string{"TMUX": "", "TMUX_PANE": "", "TMUX_TMPDIR": "/tmp/leo-tmux", "PATH": "/usr/bin"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("effective shell env = %#v, want %#v", got, want)
	}
}

// TestOpencodeTurnPluginRelaysTurnHooks runs the embedded plugin under node
// against a fake fetch and opencode client, and checks the exact hook
// payloads it posts to the daemon for each way a turn can end.
func TestOpencodeTurnPluginRelaysTurnHooks(t *testing.T) {
	submit := map[string]string{"hook_event_name": "UserPromptSubmit", "session_id": "ses_1", "prompt": "reply OK\nplease"}
	for _, tc := range []struct {
		scenario string
		want     []map[string]string
	}{
		{"turn", []map[string]string{submit, {"hook_event_name": "Stop", "session_id": "ses_1", "last_assistant_message": "OK"}}},
		{"abort", []map[string]string{submit, {"hook_event_name": "Interrupt", "session_id": "ses_1"}}},
		{"session-error", []map[string]string{submit, {"hook_event_name": "StopFailure", "session_id": "ses_1", "error": "UnknownError: boom"}}},
		{"message-error", []map[string]string{submit, {"hook_event_name": "StopFailure", "session_id": "ses_1", "error": "ProviderAuthError: bad key"}}},
	} {
		t.Run(tc.scenario, func(t *testing.T) {
			out := runTurnPlugin(t, tc.scenario, "LEO_ATTENTION_TOKEN=tok-1", "LEO_WEB_PORT=8399", "LEO_API_TOKEN=api-1", "LEO_DISPATCH_ID=")
			var got []struct {
				URL     string            `json:"url"`
				Headers map[string]string `json:"headers"`
				Body    struct {
					Token   string            `json:"token"`
					Payload map[string]string `json:"payload"`
				} `json:"body"`
			}
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("harness output is not JSON: %v\n%s", err, out)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("posted %d hooks, want %d:\n%s", len(got), len(tc.want), out)
			}
			for i, post := range got {
				if post.URL != "http://127.0.0.1:8399/api/agent/hook" || post.Headers["Authorization"] != "Bearer api-1" || post.Body.Token != "tok-1" {
					t.Errorf("post %d routed to %s auth=%q token=%q", i, post.URL, post.Headers["Authorization"], post.Body.Token)
				}
				if !reflect.DeepEqual(post.Body.Payload, tc.want[i]) {
					t.Errorf("post %d payload = %v, want %v", i, post.Body.Payload, tc.want[i])
				}
			}
		})
	}
}

// runTurnPlugin runs the embedded plugin through one harness scenario under
// node with env added, returning what it posted (skips without node).
func runTurnPlugin(t *testing.T, scenario string, env ...string) []byte {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	plugin := filepath.Join(t.TempDir(), "leo-turns.mjs")
	if err := os.WriteFile(plugin, turnPluginSource, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, filepath.Join("testdata", "turn_plugin_harness.mjs"), plugin, scenario)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("plugin harness: %v\n%s", err, out)
	}
	return out
}

func TestOpencodeTurnPluginInertOutsideSupervisedAgents(t *testing.T) {
	for name, env := range map[string][]string{
		"no attention token": {"LEO_ATTENTION_TOKEN=", "LEO_WEB_PORT=8399"},
		"inside a dispatch":  {"LEO_ATTENTION_TOKEN=tok-1", "LEO_WEB_PORT=8399", "LEO_DISPATCH_ID=d-1"},
	} {
		if out := runTurnPlugin(t, "turn", env...); string(bytes.TrimSpace(out)) != "[]" {
			t.Errorf("%s: posted %s, want nothing", name, out)
		}
	}
}
