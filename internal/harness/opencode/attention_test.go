package opencode

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
	if len(plugins) != 1 {
		t.Fatalf("plugin = %#v, want exactly the leo turn plugin", plugins)
	}
	path, err := turnPluginPath()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != dir || plugins[0] != "file://"+path {
		t.Fatalf("plugin = %q, want file://%s under %s", plugins[0], path, dir)
	}
	written, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(written, turnPluginSource) {
		t.Fatalf("plugin file at %s: err=%v, content matches=%v", path, err, bytes.Equal(written, turnPluginSource))
	}
}

func TestOpencodeTurnPluginOnlyForSupervisedAgents(t *testing.T) {
	useTurnPluginDir(t)
	for _, spec := range []harness.LaunchSpec{
		{Kind: harness.KindTask, Options: Options{}},
		{Kind: harness.KindAgent, Dispatched: true, Options: Options{}},
	} {
		env, err := Opencode{}.Env(spec)
		if err != nil {
			t.Fatal(err)
		}
		if plugins := configPlugins(t, env); len(plugins) != 0 {
			t.Errorf("kind=%s dispatched=%v: plugin = %#v, want none", spec.Kind, spec.Dispatched, plugins)
		}
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
