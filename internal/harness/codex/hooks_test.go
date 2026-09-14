package codex

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Codex 0.153.4 payload keys observed live in the pre-merge tmux spike.
var codexHookPayloadKeys = map[string][]string{
	"UserPromptSubmit": {"session_id", "turn_id", "transcript_path", "cwd", "hook_event_name", "model", "permission_mode", "prompt"},
	"Stop":             {"session_id", "turn_id", "transcript_path", "cwd", "hook_event_name", "model", "permission_mode", "stop_hook_active", "last_assistant_message"},
	"Interrupt":        {"session_id", "turn_id", "transcript_path", "cwd", "hook_event_name", "model", "permission_mode"},
	"SessionEnd":       {"session_id", "transcript_path", "cwd", "hook_event_name", "reason"},
}

func TestHookPayloadKeysDocumentation(t *testing.T) {
	for _, event := range codexHookEvents {
		if len(codexHookPayloadKeys[event]) == 0 {
			t.Errorf("missing live payload-key documentation for %s", event)
		}
	}
}

func TestTurnHooksArgv(t *testing.T) {
	got, err := (Codex{}).TurnHooks([]string{"/opt/leo", "dispatch", "report"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-c", "check_for_update_on_startup=false"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TurnHooks() = %#v, want %#v", got, want)
	}
}

func TestPrepareInteractiveIdempotent(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "hooks.json")
	if err := os.WriteFile(path, []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/usr/bin/user-hook"}]}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	canonicalHooks, err := canonicalPath(path)
	if err != nil {
		t.Fatal(err)
	}
	userHash := trustHash("Stop", nil, map[string]any{"type": "command", "command": "/usr/bin/user-hook"})
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(trustEntry(canonicalHooks+":stop:0:0", userHash)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	prepareLeoHookCommand = func() string { return "/opt/leo dispatch report" }
	t.Cleanup(func() { prepareLeoHookCommand = defaultLeoHookCommand })
	if err := (Codex{}).PrepareInteractive(home, ""); err != nil {
		t.Fatal(err)
	}
	firstHooks, _ := os.ReadFile(path)
	firstConfig, _ := os.ReadFile(filepath.Join(home, "config.toml"))
	if !strings.Contains(string(firstHooks), "/usr/bin/user-hook") {
		t.Fatal("user hook was not preserved")
	}
	if err := (Codex{}).PrepareInteractive(home, ""); err != nil {
		t.Fatal(err)
	}
	secondHooks, _ := os.ReadFile(path)
	secondConfig, _ := os.ReadFile(filepath.Join(home, "config.toml"))
	if string(firstHooks) != string(secondHooks) || string(firstConfig) != string(secondConfig) {
		t.Fatal("second PrepareInteractive changed files")
	}
}

func TestPrepareInteractiveDetectsUntrusted(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "hooks.json"), []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/usr/bin/user-hook"}]}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	err := (Codex{}).PrepareInteractive(home, "")
	if err == nil || !strings.Contains(err.Error(), "/usr/bin/user-hook") {
		t.Fatalf("PrepareInteractive() error = %v, want untrusted user hook", err)
	}
}

func TestTrustHash(t *testing.T) {
	const command = "/tmp/codex-spike/hook.sh"
	const want = "sha256:617a7a87a057bbeb8b9819d2a45b79d309f506dab52b4fdb0331a67fa568a7cd"
	if got := trustHash("Stop", nil, map[string]any{"type": "command", "command": command}); got != want {
		t.Fatalf("trustHash() = %q, want persisted Codex hash %q", got, want)
	}
}
