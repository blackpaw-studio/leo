package codex

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/harness"
)

func TestAttentionLaunchInstallsHooksInCodexHome(t *testing.T) {
	home := t.TempDir()
	prepareLeoHookCommand = func() string { return "/opt/leo dispatch report" }
	t.Cleanup(func() { prepareLeoHookCommand = defaultLeoHookCommand })
	hooker, ok := (Codex{}).Driver().(harness.AttentionHooker)
	if !ok {
		t.Fatal("codex driver is not a harness.AttentionHooker")
	}
	args := []string{"--model", "gpt"}

	got, supported, err := hooker.AttentionLaunch(harness.SessionHandle{Env: map[string]string{"CODEX_HOME": home}, Workspace: t.TempDir()}, args, []string{"/ignored"})

	if err != nil || !supported {
		t.Fatalf("err=%v supported=%v", err, supported)
	}
	if !reflect.DeepEqual(got, args) {
		t.Fatalf("args = %#v, want unchanged %#v", got, args)
	}
	hooks, err := os.ReadFile(filepath.Join(home, "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []string{"UserPromptSubmit", "Stop", "Interrupt"} {
		if !strings.Contains(string(hooks), `"`+event+`"`) {
			t.Errorf("hooks.json missing %s: %s", event, hooks)
		}
	}
	if !strings.Contains(string(hooks), "/opt/leo dispatch report") {
		t.Errorf("hooks.json missing report command: %s", hooks)
	}
	config, _ := os.ReadFile(filepath.Join(home, "config.toml"))
	if !strings.Contains(string(config), "trusted_hash") {
		t.Errorf("config.toml has no hook trust entries: %s", config)
	}
}

func TestAttentionLaunchReportsPrepareFailure(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "hooks.json"), []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/usr/bin/untrusted"}]}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	hooker := (Codex{}).Driver().(harness.AttentionHooker)
	args := []string{"--x"}

	got, supported, err := hooker.AttentionLaunch(harness.SessionHandle{Env: map[string]string{"CODEX_HOME": home}}, args, nil)

	if err == nil || supported || !reflect.DeepEqual(got, args) {
		t.Fatalf("got %#v supported=%v err=%v; want args unchanged, unsupported, error", got, supported, err)
	}
}
