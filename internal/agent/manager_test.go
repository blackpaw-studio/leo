package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

// --- ResolveWorkspace Tests ---

func TestResolveWorkspacePlainName(t *testing.T) {
	dir := t.TempDir()
	tmpl := config.TemplateConfig{Workspace: dir}

	workspace, name, err := ResolveWorkspace(tmpl, "coding", "myproject", "")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	expected := filepath.Join(dir, "myproject")
	if workspace != expected {
		t.Errorf("workspace = %q, want %q", workspace, expected)
	}
	if _, err := os.Stat(expected); err != nil {
		t.Errorf("expected workspace dir to be created: %v", err)
	}
	if name != "leo-coding-myproject" {
		t.Errorf("name = %q, want leo-coding-myproject", name)
	}
}

func TestResolveWorkspaceWithSlashExistingClone(t *testing.T) {
	dir := t.TempDir()
	repoDir := filepath.Join(dir, "myrepo", ".git")
	if err := os.MkdirAll(repoDir, 0750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	tmpl := config.TemplateConfig{Workspace: dir}

	workspace, name, err := ResolveWorkspace(tmpl, "coding", "owner/myrepo", "")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	expected := filepath.Join(dir, "myrepo")
	if workspace != expected {
		t.Errorf("workspace = %q, want %q", workspace, expected)
	}
	if name != "leo-coding-owner-myrepo" {
		t.Errorf("name = %q, want leo-coding-owner-myrepo", name)
	}
}

func TestResolveWorkspaceNameOverride(t *testing.T) {
	dir := t.TempDir()
	tmpl := config.TemplateConfig{Workspace: dir}

	_, name, err := ResolveWorkspace(tmpl, "coding", "test", "custom-name")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if name != "custom-name" {
		t.Errorf("name = %q, want custom-name", name)
	}
}

func TestResolveWorkspaceDefaultWorkspace(t *testing.T) {
	tmpl := config.TemplateConfig{}

	workspace, _, err := ResolveWorkspace(tmpl, "coding", "test", "")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if workspace == "" {
		t.Error("expected non-empty default workspace")
	}
}

// --- BuildTemplateArgs Tests ---

func TestBuildTemplateArgsBasic(t *testing.T) {
	cfg := &config.Config{
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 10},
	}
	tmpl := config.TemplateConfig{
		Model:    "opus",
		MaxTurns: 200,
	}

	args := BuildTemplateArgs(cfg, tmpl, "test-agent", "/tmp/workspace", "")

	assertContainsFlag(t, args, "--model", "opus")
	assertContainsFlag(t, args, "--max-turns", "200")
	assertContainsFlag(t, args, "--add-dir", "/tmp/workspace")
	assertContains(t, args, "--remote-control")
	assertContainsFlag(t, args, "--name", "test-agent")
}

func TestBuildTemplateArgsInheritsDefaults(t *testing.T) {
	cfg := &config.Config{
		Defaults: config.DefaultsConfig{
			Model:              "haiku",
			MaxTurns:           50,
			PermissionMode:     "auto",
			AllowedTools:       []string{"Read", "Write"},
			AppendSystemPrompt: "be helpful",
		},
	}
	tmpl := config.TemplateConfig{}

	args := BuildTemplateArgs(cfg, tmpl, "test", "/tmp/ws", "")

	assertContainsFlag(t, args, "--model", "haiku")
	assertContainsFlag(t, args, "--max-turns", "50")
	assertContainsFlag(t, args, "--permission-mode", "auto")
	assertContainsFlag(t, args, "--allowed-tools", "Read,Write")
	assertContainsFlag(t, args, "--append-system-prompt", "be helpful")
}

func TestBuildTemplateArgsChannels(t *testing.T) {
	cfg := &config.Config{}
	tmpl := config.TemplateConfig{
		Channels: []string{"plugin:telegram@official", "plugin:slack@custom"},
	}

	args := BuildTemplateArgs(cfg, tmpl, "test", "/tmp/ws", "")

	count := 0
	for _, a := range args {
		if a == "--channels" {
			count++
		}
	}
	if count != 2 {
		t.Errorf("expected 2 --channels flags, got %d", count)
	}
}

func TestBuildTemplateArgsDevChannels(t *testing.T) {
	cfg := &config.Config{}
	tmpl := config.TemplateConfig{
		Channels:    []string{"plugin:telegram@official"},
		DevChannels: []string{"plugin:blackpaw-telegram@blackpaw-plugins"},
	}

	args := BuildTemplateArgs(cfg, tmpl, "test", "/tmp/ws", "")

	var sawChan, sawDev bool
	for i, a := range args {
		if a == "--channels" && i+1 < len(args) && args[i+1] == "plugin:telegram@official" {
			sawChan = true
		}
		if a == "--dangerously-load-development-channels" && i+1 < len(args) && args[i+1] == "plugin:blackpaw-telegram@blackpaw-plugins" {
			sawDev = true
		}
	}
	if !sawChan {
		t.Errorf("missing --channels flag, args: %v", args)
	}
	if !sawDev {
		t.Errorf("missing --dangerously-load-development-channels flag, args: %v", args)
	}
}

func TestBuildTemplateArgsAgent(t *testing.T) {
	cfg := &config.Config{}
	tmpl := config.TemplateConfig{Agent: "my-agent"}

	args := BuildTemplateArgs(cfg, tmpl, "test", "/tmp/ws", "")
	assertContainsFlag(t, args, "--agent", "my-agent")
}

func TestBuildTemplateArgsRemoteControlDisabled(t *testing.T) {
	cfg := &config.Config{}
	rc := false
	tmpl := config.TemplateConfig{RemoteControl: &rc}

	args := BuildTemplateArgs(cfg, tmpl, "test", "/tmp/ws", "")
	for _, a := range args {
		if a == "--remote-control" {
			t.Error("--remote-control should not be present when disabled")
		}
	}
}

func TestBuildTemplateArgsPromptIsTrailingPositional(t *testing.T) {
	cfg := &config.Config{}
	tmpl := config.TemplateConfig{Model: "opus"}

	args := BuildTemplateArgs(cfg, tmpl, "test", "/tmp/ws", "investigate alert X")

	if len(args) == 0 {
		t.Fatal("expected non-empty args")
	}
	// The prompt must be the final element, after every flag, so claude
	// treats it as the opening interactive turn.
	if last := args[len(args)-1]; last != "investigate alert X" {
		t.Errorf("expected trailing positional %q, got %q (full: %v)", "investigate alert X", last, args)
	}
	// It must not be introduced via -p/--print, which would make the
	// session one-shot instead of interactive.
	for _, a := range args {
		if a == "-p" || a == "--print" {
			t.Errorf("prompt must not be passed via %q — agents must stay interactive (args: %v)", a, args)
		}
	}
}

func TestBuildTemplateArgsNoPromptOmitsPositional(t *testing.T) {
	cfg := &config.Config{}
	tmpl := config.TemplateConfig{Model: "opus"}

	args := BuildTemplateArgs(cfg, tmpl, "test", "/tmp/ws", "")

	// Backward compat: with no prompt, the final arg is still a flag value
	// (--max-turns N), never a bare positional.
	if last := args[len(args)-1]; last == "" {
		t.Errorf("empty prompt must not append a trailing positional, got %v", args)
	}
	if args[len(args)-2] != "--max-turns" {
		t.Errorf("expected args to end with the --max-turns flag pair when no prompt is given, got %v", args)
	}
}

func TestMergeEnvOverridesTemplateOnCollision(t *testing.T) {
	base := map[string]string{"FOO": "1", "BAR": "template"}
	overlay := map[string]string{"BAR": "spawn", "SLACK_THREAD_TS": "123.456"}

	got := mergeEnv(base, overlay)

	want := map[string]string{"FOO": "1", "BAR": "spawn", "SLACK_THREAD_TS": "123.456"}
	if len(got) != len(want) {
		t.Fatalf("merged env has %d keys, want %d: %v", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("merged[%q] = %q, want %q", k, got[k], v)
		}
	}
	// Inputs must not be mutated (immutability).
	if base["BAR"] != "template" {
		t.Errorf("mergeEnv mutated the base map: BAR = %q", base["BAR"])
	}
}

func TestMergeEnvNilInputs(t *testing.T) {
	if got := mergeEnv(nil, nil); got != nil {
		t.Errorf("mergeEnv(nil, nil) = %v, want nil", got)
	}
	only := map[string]string{"A": "1"}
	if got := mergeEnv(only, nil); len(got) != 1 || got["A"] != "1" {
		t.Errorf("mergeEnv(only, nil) = %v, want {A:1}", got)
	}
	if got := mergeEnv(nil, only); len(got) != 1 || got["A"] != "1" {
		t.Errorf("mergeEnv(nil, only) = %v, want {A:1}", got)
	}
}

// --- Helpers ---

func assertContains(t *testing.T, args []string, flag string) {
	t.Helper()
	for _, a := range args {
		if a == flag {
			return
		}
	}
	t.Errorf("expected args to contain %q, got %v", flag, args)
}

func assertContainsFlag(t *testing.T, args []string, flag, value string) {
	t.Helper()
	for i, a := range args {
		if a == flag && i+1 < len(args) && args[i+1] == value {
			return
		}
	}
	t.Errorf("expected args to contain %s %s, got %v", flag, value, args)
}
