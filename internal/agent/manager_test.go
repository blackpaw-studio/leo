package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agentstore"
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

	args := BuildTemplateArgs(cfg, tmpl, "test-agent", "/tmp/workspace", "", "")

	assertContainsFlag(t, args, "--model", "opus")
	assertContainsFlag(t, args, "--max-turns", "200")
	assertContainsFlag(t, args, "--add-dir", "/tmp/workspace")
	assertContains(t, args, "--remote-control")
	assertContainsFlag(t, args, "--name", "test-agent")
}

func TestBuildTemplateArgsInheritsDefaults(t *testing.T) {
	cfg := &config.Config{
		Defaults: config.DefaultsConfig{
			Model:    "haiku",
			MaxTurns: 50,
			HarnessOptions: map[string]any{
				"permission_mode":      "auto",
				"allowed_tools":        []any{"Read", "Write"},
				"append_system_prompt": "be helpful",
			},
		},
	}
	tmpl := config.TemplateConfig{}

	args := BuildTemplateArgs(cfg, tmpl, "test", "/tmp/ws", "", "")

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

	args := BuildTemplateArgs(cfg, tmpl, "test", "/tmp/ws", "", "")

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

	args := BuildTemplateArgs(cfg, tmpl, "test", "/tmp/ws", "", "")

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
	tmpl := config.TemplateConfig{HarnessOptions: map[string]any{"agent": "my-agent"}}

	args := BuildTemplateArgs(cfg, tmpl, "test", "/tmp/ws", "", "")
	assertContainsFlag(t, args, "--agent", "my-agent")
}

func TestBuildTemplateArgsRemoteControlDisabled(t *testing.T) {
	cfg := &config.Config{}
	tmpl := config.TemplateConfig{HarnessOptions: map[string]any{"remote_control": false}}

	args := BuildTemplateArgs(cfg, tmpl, "test", "/tmp/ws", "", "")
	for _, a := range args {
		if a == "--remote-control" {
			t.Error("--remote-control should not be present when disabled")
		}
	}
}

func TestBuildTemplateArgsPromptIsTrailingPositional(t *testing.T) {
	cfg := &config.Config{}
	tmpl := config.TemplateConfig{Model: "opus"}

	args := BuildTemplateArgs(cfg, tmpl, "test", "/tmp/ws", "investigate alert X", "")

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

	args := BuildTemplateArgs(cfg, tmpl, "test", "/tmp/ws", "", "")

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

// --- Task 5: IdleSuspend stamped on record at spawn ---

func TestSpawnStampsResolvedIdleSuspend(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{
		HomePath: home,
		Defaults: config.DefaultsConfig{Model: "sonnet"},
		Templates: map[string]config.TemplateConfig{
			"t": {Workspace: home, IdleSuspendAfter: "24h"},
		},
	}
	sup := &capturingSupervisor{}
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")

	if _, err := m.Spawn(context.Background(), SpawnSpec{Template: "t", Repo: "demo"}); err != nil {
		t.Fatalf("spawn: %v", err)
	}

	recs, _ := agentstore.Load(agentstore.FilePath(home))
	if len(recs) != 1 {
		t.Fatalf("want 1 record, got %d", len(recs))
	}
	for _, r := range recs {
		if r.IdleSuspendAfter != (24 * time.Hour).String() {
			t.Fatalf("idle interval not stamped: %q", r.IdleSuspendAfter)
		}
	}

	// per-spawn override beats the template
	for k := range recs {
		agentstore.Remove(home, k)
	}
	if _, err := m.Spawn(context.Background(), SpawnSpec{Template: "t", Repo: "demo2", IdleSuspend: "15m"}); err != nil {
		t.Fatalf("spawn override: %v", err)
	}
	recs, _ = agentstore.Load(agentstore.FilePath(home))
	for _, r := range recs {
		if r.IdleSuspendAfter != (15 * time.Minute).String() {
			t.Fatalf("override not applied: %q", r.IdleSuspendAfter)
		}
	}
}

// TestSpawnStampsClaudeHarnessByDefault characterizes the harness-aware spawn
// path: an agent spawned from a template with no explicit harness (the only
// kind config validation allows today) must persist Harness == "claude" on
// the agentstore record and keep --session-id in ClaudeArgs, so nothing
// about the claude spawn path changes for existing configs.
func TestSpawnStampsClaudeHarnessByDefault(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{
		HomePath: home,
		Defaults: config.DefaultsConfig{Model: "sonnet"},
		Templates: map[string]config.TemplateConfig{
			"t": {Workspace: home},
		},
	}
	sup := &capturingSupervisor{}
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")

	if _, err := m.Spawn(context.Background(), SpawnSpec{Template: "t", Repo: "demo"}); err != nil {
		t.Fatalf("spawn: %v", err)
	}

	recs, err := agentstore.Load(agentstore.FilePath(home))
	if err != nil || len(recs) != 1 {
		t.Fatalf("want 1 record, got %d (err=%v)", len(recs), err)
	}
	for _, r := range recs {
		if r.Harness != "claude" {
			t.Errorf("Harness = %q, want %q", r.Harness, "claude")
		}
		if !hasFlagValue(r.ClaudeArgs, "--session-id", "") {
			t.Errorf("expected --session-id in ClaudeArgs, got %v", r.ClaudeArgs)
		}
	}
}

// --- Task 6: Manager.Suspend ---

func TestSuspendMarksRecordAndStops(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home}
	sup := &capturingSupervisor{
		agents: map[string]ProcessState{
			"leo-x": {Name: "leo-x", Status: "running"},
		},
	}
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-x", Workspace: "/w", SessionID: "sid"})

	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	if err := m.Suspend("leo-x"); err != nil {
		t.Fatalf("suspend: %v", err)
	}

	if len(sup.stopCalls) == 0 || sup.stopCalls[0] != "leo-x" {
		t.Fatal("StopAgent was not called")
	}
	recs, _ := agentstore.Load(agentstore.FilePath(home))
	if !recs["leo-x"].Suspended {
		t.Fatal("record not marked Suspended")
	}
	if recs["leo-x"].SessionID != "sid" {
		t.Fatal("SessionID must be preserved for resume")
	}

	// not-running => error
	if err := m.Suspend("ghost"); err == nil {
		t.Fatal("suspending a non-running agent should error")
	}
}

func TestSuspendRollsBackFlagOnStopFailure(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home}
	sup := &capturingSupervisor{
		agents:  map[string]ProcessState{"leo-x": {Name: "leo-x", Status: "running"}},
		stopErr: errors.New("tmux kill failed"),
	}
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-x", Workspace: "/w", SessionID: "sid"})

	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	if err := m.Suspend("leo-x"); err == nil {
		t.Fatal("expected error when StopAgent fails")
	}

	// Record must NOT be left in Suspended=true state after a failed stop.
	recs, _ := agentstore.Load(agentstore.FilePath(home))
	if recs["leo-x"].Suspended {
		t.Fatal("Suspended flag must be rolled back when StopAgent fails")
	}
}

// --- Task 7: Manager.Resume ---

func TestResumeRespawnsWithResumeAndClearsFlag(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home}
	sup := &capturingSupervisor{}
	_ = agentstore.Save(home, agentstore.Record{
		Name:       "leo-x",
		Workspace:  "/w",
		SessionID:  "sid",
		ClaudeArgs: []string{"--model", "sonnet", "--session-id", "sid"},
		Suspended:  true,
	})

	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	rec, err := m.Resume("leo-x")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if rec.Status != "starting" {
		t.Fatalf("want starting, got %q", rec.Status)
	}

	// spawned with --resume sid, no --session-id
	if sup.spawnCall == nil {
		t.Fatal("SpawnAgent was not called")
	}
	got := sup.spawnCall.ClaudeArgs
	if !containsPair(got, "--resume", "sid") || containsFlag(got, "--session-id") {
		t.Fatalf("resume args wrong: %v", got)
	}

	recs, _ := agentstore.Load(agentstore.FilePath(home))
	if recs["leo-x"].Suspended {
		t.Fatal("Suspended flag not cleared after resume")
	}

	// resuming a non-suspended/unknown agent errors
	if _, err := m.Resume("ghost"); err == nil {
		t.Fatal("resuming unknown agent should error")
	}
}

// --- Task 8: List surfaces suspended agents ---

func TestListSurfacesSuspendedAgents(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home}
	sup := &capturingSupervisor{} // no live agents
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-shared", Workspace: "/w", Suspended: true})
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-wt", Workspace: "/w2", Branch: "feat", Suspended: true})

	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	got := m.List()

	statuses := map[string]string{}
	for _, r := range got {
		statuses[r.Name] = r.Status
	}
	if statuses["leo-shared"] != "suspended" {
		t.Fatalf("shared suspended agent missing/wrong: %v", statuses)
	}
	if statuses["leo-wt"] != "suspended" {
		t.Fatalf("worktree suspended agent missing/wrong: %v", statuses)
	}
}

// containsPair returns true when args contains flag followed immediately by value.
func containsPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

// containsFlag returns true when args contains the given flag (with or without value).
func containsFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
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
