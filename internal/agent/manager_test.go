package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/leomcp"
)

// TestRecordCarriesNoEnv guards a credential leak. Record is the payload of
// GET /api/agent/list, which is what the leo_list_agents MCP tool serves to
// every agent — so an Env field here deposits live agent credentials
// (OP_SERVICE_ACCOUNT_TOKEN and friends) into any transcript that lists
// agents. Nothing reads Record.Env; the agentstore record is where the
// supervisor gets env from. Keep it off the public view.
func TestRecordCarriesNoEnv(t *testing.T) {
	if _, found := reflect.TypeOf(Record{}).FieldByName("Env"); found {
		t.Error("agent.Record must not carry Env: it is served verbatim by leo_list_agents")
	}
}

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

func TestResolveWorkspaceEmptyRepoUsesBaseWorkspace(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fresh") // does not exist yet
	tmpl := config.TemplateConfig{Workspace: dir}

	workspace, name, err := ResolveWorkspace(tmpl, "assistant", "", "")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if workspace != dir {
		t.Errorf("workspace = %q, want %q (base workspace directly, no subdir)", workspace, dir)
	}
	if name != "assistant" {
		t.Errorf("name = %q, want %q", name, "assistant")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("expected base workspace dir to be created: %v", err)
	}
}

func TestResolveWorkspaceEmptyRepoNameOverride(t *testing.T) {
	dir := t.TempDir()
	tmpl := config.TemplateConfig{Workspace: dir}

	_, name, err := ResolveWorkspace(tmpl, "assistant", "", "custom-name")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if name != "custom-name" {
		t.Errorf("name = %q, want custom-name", name)
	}
}

func TestDeriveSharedAgentNameEmptyRepo(t *testing.T) {
	if got := DeriveSharedAgentName("assistant", "", ""); got != "assistant" {
		t.Errorf("DeriveSharedAgentName = %q, want %q", got, "assistant")
	}
	if got := DeriveSharedAgentName("assistant", "", "custom"); got != "custom" {
		t.Errorf("DeriveSharedAgentName with override = %q, want %q", got, "custom")
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

	args, _ := BuildTemplateArgs(cfg, tmpl, "test-agent", "/tmp/workspace", "", "")

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

	args, _ := BuildTemplateArgs(cfg, tmpl, "test", "/tmp/ws", "", "")

	assertContainsFlag(t, args, "--model", "haiku")
	assertContainsFlag(t, args, "--max-turns", "50")
	assertContainsFlag(t, args, "--permission-mode", "auto")
	assertContainsFlag(t, args, "--allowed-tools", "Read,Write")
	assertContainsFlag(t, args, "--append-system-prompt", leoSkillNudgeText+"\n\nbe helpful")
}

func TestBuildTemplateArgsChannels(t *testing.T) {
	// HomePath set: BuildTemplateArgs reaches AppendArg, which writes
	// leo-mcp.json under HomePath/state; an empty HomePath would no-op.
	cfg := &config.Config{HomePath: t.TempDir()}
	tmpl := config.TemplateConfig{
		Channels: []string{"plugin:telegram@official", "plugin:slack@custom"},
	}

	args, _ := BuildTemplateArgs(cfg, tmpl, "test", "/tmp/ws", "", "")

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
	// HomePath set: BuildTemplateArgs reaches AppendArg, which writes
	// leo-mcp.json under HomePath/state; an empty HomePath would no-op.
	cfg := &config.Config{HomePath: t.TempDir()}
	tmpl := config.TemplateConfig{
		Channels:    []string{"plugin:telegram@official"},
		DevChannels: []string{"plugin:blackpaw-telegram@blackpaw-plugins"},
	}

	args, _ := BuildTemplateArgs(cfg, tmpl, "test", "/tmp/ws", "", "")

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
	// HomePath set: BuildTemplateArgs reaches AppendArg, which writes
	// leo-mcp.json under HomePath/state; an empty HomePath would no-op.
	cfg := &config.Config{HomePath: t.TempDir()}
	tmpl := config.TemplateConfig{HarnessOptions: map[string]any{"agent": "my-agent"}}

	args, _ := BuildTemplateArgs(cfg, tmpl, "test", "/tmp/ws", "", "")
	assertContainsFlag(t, args, "--agent", "my-agent")
}

func TestBuildTemplateArgsRemoteControlDisabled(t *testing.T) {
	// HomePath set: BuildTemplateArgs reaches AppendArg, which writes
	// leo-mcp.json under HomePath/state; an empty HomePath would no-op.
	cfg := &config.Config{HomePath: t.TempDir()}
	tmpl := config.TemplateConfig{HarnessOptions: map[string]any{"remote_control": false}}

	args, _ := BuildTemplateArgs(cfg, tmpl, "test", "/tmp/ws", "", "")
	for _, a := range args {
		if a == "--remote-control" {
			t.Error("--remote-control should not be present when disabled")
		}
	}
}

func TestBuildTemplateArgsPromptIsTrailingPositional(t *testing.T) {
	// HomePath set: BuildTemplateArgs reaches AppendArg, which writes
	// leo-mcp.json under HomePath/state; an empty HomePath would no-op.
	cfg := &config.Config{HomePath: t.TempDir()}
	tmpl := config.TemplateConfig{Model: "opus"}

	args, _ := BuildTemplateArgs(cfg, tmpl, "test", "/tmp/ws", "investigate alert X", "")

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
	// HomePath set: BuildTemplateArgs reaches AppendArg, which writes
	// leo-mcp.json under HomePath/state; an empty HomePath would no-op.
	cfg := &config.Config{HomePath: t.TempDir()}
	tmpl := config.TemplateConfig{Model: "opus"}

	args, _ := BuildTemplateArgs(cfg, tmpl, "test", "/tmp/ws", "", "")

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

// --- Spawn with no repo (repo-less template run) ---

func TestSpawnWithEmptyRepoSucceeds(t *testing.T) {
	home := t.TempDir()
	wsDir := filepath.Join(home, "ws")
	cfg := &config.Config{
		HomePath: home,
		Defaults: config.DefaultsConfig{Model: "sonnet"},
		Templates: map[string]config.TemplateConfig{
			"assistant": {Workspace: wsDir},
		},
	}
	sup := &capturingSupervisor{}
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")

	rec, err := m.Spawn(context.Background(), SpawnSpec{Template: "assistant"})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if rec.Name != "assistant" {
		t.Errorf("Name = %q, want %q", rec.Name, "assistant")
	}
	if rec.Workspace != wsDir {
		t.Errorf("Workspace = %q, want %q", rec.Workspace, wsDir)
	}
	if rec.Repo != "" {
		t.Errorf("Repo = %q, want empty", rec.Repo)
	}

	recs, err := agentstore.Load(agentstore.FilePath(home))
	if err != nil || len(recs) != 1 {
		t.Fatalf("want 1 record, got %d (err=%v)", len(recs), err)
	}
	stored, ok := recs["assistant"]
	if !ok {
		t.Fatalf("expected record keyed %q, got %v", "assistant", recs)
	}
	if stored.Repo != "" {
		t.Errorf("stored Repo = %q, want empty", stored.Repo)
	}
}

// --- SpawnFromTemplate (ensure-exists path for implicit persistent-task targets) ---

// TestSpawnFromTemplateReachesSupervisor verifies SpawnFromTemplate spawns a
// repo-less agent through the same supervisor path as a repo-less Spawn,
// skipping the cfg.Templates[...] lookup entirely (the caller hands the
// TemplateConfig directly, as config.ResolveTaskTarget does for implicit
// targets).
func TestSpawnFromTemplateReachesSupervisor(t *testing.T) {
	home := t.TempDir()
	wsDir := filepath.Join(home, "ws")
	cfg := &config.Config{
		HomePath: home,
		Defaults: config.DefaultsConfig{Model: "sonnet"},
	}
	sup := &capturingSupervisor{}
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")

	tmpl := config.TemplateConfig{Workspace: wsDir}
	rec, err := m.SpawnFromTemplate(context.Background(), "my-task", tmpl)
	if err != nil {
		t.Fatalf("SpawnFromTemplate: %v", err)
	}
	if rec.Name != "my-task" {
		t.Errorf("Name = %q, want %q", rec.Name, "my-task")
	}
	if rec.Workspace != wsDir {
		t.Errorf("Workspace = %q, want %q", rec.Workspace, wsDir)
	}
	if sup.spawnCall == nil {
		t.Fatalf("expected supervisor.SpawnAgent to be called")
	}
	if sup.spawnCall.Name != "my-task" {
		t.Errorf("SpawnAgent name = %q, want %q", sup.spawnCall.Name, "my-task")
	}
	if sup.spawnCall.WorkDir != wsDir {
		t.Errorf("SpawnAgent WorkDir = %q, want %q", sup.spawnCall.WorkDir, wsDir)
	}

	recs, err := agentstore.Load(agentstore.FilePath(home))
	if err != nil || len(recs) != 1 {
		t.Fatalf("want 1 record, got %d (err=%v)", len(recs), err)
	}
	if _, ok := recs["my-task"]; !ok {
		t.Fatalf("expected record keyed %q, got %v", "my-task", recs)
	}
}

func TestSpawnFromTemplateRequiresName(t *testing.T) {
	m := New(func() (*config.Config, error) { return &config.Config{}, nil }, &capturingSupervisor{}, "", "")
	if _, err := m.SpawnFromTemplate(context.Background(), "", config.TemplateConfig{}); err == nil {
		t.Fatalf("expected error for empty name")
	}
}

// --- Live / Stopped / Wakeable (ensure-exists liveness probes) ---

func TestLiveReportsSupervisorState(t *testing.T) {
	sup := &capturingSupervisor{agents: map[string]ProcessState{
		"running-agent":    {Name: "running-agent", Status: "running"},
		"starting-agent":   {Name: "starting-agent", Status: "starting"},
		"stopped-agent":    {Name: "stopped-agent", Status: "stopped"},
		"restarting-agent": {Name: "restarting-agent", Status: "restarting"},
	}}
	m := New(func() (*config.Config, error) { return &config.Config{}, nil }, sup, "", "")

	if !m.Live("running-agent") {
		t.Errorf("expected Live(running-agent) = true")
	}
	if !m.Live("starting-agent") {
		t.Errorf("expected Live(starting-agent) = true (readiness-probed on injection)")
	}
	if m.Live("stopped-agent") {
		t.Errorf("expected Live(stopped-agent) = false")
	}
	if m.Live("restarting-agent") {
		t.Errorf("expected Live(restarting-agent) = false")
	}
	if m.Live("unknown") {
		t.Errorf("expected Live(unknown) = false")
	}
}

func TestStoppedReportsAgentstoreState(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home}
	if err := agentstore.Save(home, agentstore.Record{Name: "stopped-agent", Stopped: true}); err != nil {
		t.Fatalf("seeding agentstore: %v", err)
	}
	sup := &capturingSupervisor{}
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "")

	if !m.Stopped("stopped-agent") {
		t.Errorf("expected Stopped(stopped-agent) = true")
	}
	if m.Stopped("unknown") {
		t.Errorf("expected Stopped(unknown) = false")
	}
}

func TestStoppedFalseWhenLive(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home}
	if err := agentstore.Save(home, agentstore.Record{Name: "both", Stopped: true}); err != nil {
		t.Fatalf("seeding agentstore: %v", err)
	}
	sup := &capturingSupervisor{agents: map[string]ProcessState{"both": {Name: "both", Status: "running"}}}
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "")

	if m.Stopped("both") {
		t.Errorf("expected Stopped(both) = false when Live is true")
	}
}

// TestWakeableRequiresBothStoppedAndWakeOnMessage covers the auto-wake gate
// directly: a dormant record only counts as Wakeable when WakeOnMessage is
// also true — a plain operator-initiated stop (WakeOnMessage=false) must
// report false so an inbound message never resurrects it.
func TestWakeableRequiresBothStoppedAndWakeOnMessage(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home}
	if err := agentstore.Save(home, agentstore.Record{Name: "wakeable", Stopped: true, WakeOnMessage: true}); err != nil {
		t.Fatalf("seeding agentstore: %v", err)
	}
	if err := agentstore.Save(home, agentstore.Record{Name: "not-wakeable", Stopped: true, WakeOnMessage: false}); err != nil {
		t.Fatalf("seeding agentstore: %v", err)
	}
	sup := &capturingSupervisor{}
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "")

	if !m.Wakeable("wakeable") {
		t.Error("expected Wakeable(wakeable) = true")
	}
	if m.Wakeable("not-wakeable") {
		t.Error("expected Wakeable(not-wakeable) = false")
	}
	if m.Wakeable("unknown") {
		t.Error("expected Wakeable(unknown) = false")
	}
}

func TestSpawnWithEmptyRepoNameCollisionSuffixes(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{
		HomePath: home,
		Defaults: config.DefaultsConfig{Model: "sonnet"},
		Templates: map[string]config.TemplateConfig{
			"assistant": {Workspace: t.TempDir()},
		},
	}
	sup := &capturingSupervisor{
		agents: map[string]ProcessState{
			"assistant": {Name: "assistant", Status: "running"},
		},
	}
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")

	rec, err := m.Spawn(context.Background(), SpawnSpec{Template: "assistant"})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if rec.Name != "assistant-2" {
		t.Errorf("Name = %q, want %q (collision suffix)", rec.Name, "assistant-2")
	}
}

func TestSpawnWorktreeWithoutRepoErrors(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{
		HomePath: home,
		Templates: map[string]config.TemplateConfig{
			"assistant": {Workspace: t.TempDir()},
		},
	}
	sup := &capturingSupervisor{}
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")

	_, err := m.Spawn(context.Background(), SpawnSpec{Template: "assistant", Branch: "feat/x"})
	if err == nil {
		t.Fatal("expected error spawning a worktree without a repo")
	}
	if !strings.Contains(err.Error(), "requires a repo") {
		t.Errorf("error = %q, want it to mention repo is required for worktrees", err.Error())
	}
}

// --- Task 6: Manager.Stop(opts) ---

// TestStopManualLeavesWakeOnMessageFalseSweepLeavesTrue is the load-bearing
// case distinguishing the two callers of Stop: a manual (operator-initiated)
// stop must leave WakeOnMessage=false, while the idle sweep's stop (passing
// WakeOnMessage: true) leaves it true — both while preserving SessionID.
func TestStopManualLeavesWakeOnMessageFalseSweepLeavesTrue(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home}
	sup := &capturingSupervisor{
		agents: map[string]ProcessState{
			"leo-manual": {Name: "leo-manual", Status: "running"},
			"leo-swept":  {Name: "leo-swept", Status: "running"},
		},
	}
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-manual", Workspace: "/w", SessionID: "sid-1"})
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-swept", Workspace: "/w", SessionID: "sid-2"})

	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	if err := m.Stop("leo-manual", StopOptions{WakeOnMessage: false}); err != nil {
		t.Fatalf("stop (manual): %v", err)
	}
	if err := m.Stop("leo-swept", StopOptions{WakeOnMessage: true}); err != nil {
		t.Fatalf("stop (sweep): %v", err)
	}

	recs, _ := agentstore.Load(agentstore.FilePath(home))
	manual := recs["leo-manual"]
	if !manual.Stopped || manual.WakeOnMessage {
		t.Fatalf("manual stop: got Stopped=%v WakeOnMessage=%v, want true/false", manual.Stopped, manual.WakeOnMessage)
	}
	if manual.SessionID != "sid-1" {
		t.Fatal("SessionID must be preserved across a manual stop")
	}
	swept := recs["leo-swept"]
	if !swept.Stopped || !swept.WakeOnMessage {
		t.Fatalf("swept stop: got Stopped=%v WakeOnMessage=%v, want true/true", swept.Stopped, swept.WakeOnMessage)
	}
	if swept.SessionID != "sid-2" {
		t.Fatal("SessionID must be preserved across a swept stop")
	}

	// not-live and not-persisted => error
	if err := m.Stop("ghost", StopOptions{}); err == nil {
		t.Fatal("stopping an unknown agent should error")
	}
}

// TestStopIdempotentUpdatesWakeOnMessage verifies Stop on an already-dormant
// agent is a no-op on the live process (nothing to kill) but still updates
// WakeOnMessage to the newly requested value.
func TestStopIdempotentUpdatesWakeOnMessage(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home}
	sup := &capturingSupervisor{} // nothing live
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-x", Workspace: "/w", SessionID: "sid", Stopped: true, WakeOnMessage: false})

	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	if err := m.Stop("leo-x", StopOptions{WakeOnMessage: true}); err != nil {
		t.Fatalf("re-stop: %v", err)
	}
	if len(sup.stopCalls) != 0 {
		t.Fatalf("StopAgent must not be called for an already-dormant agent, got %v", sup.stopCalls)
	}

	recs, _ := agentstore.Load(agentstore.FilePath(home))
	got := recs["leo-x"]
	if !got.Stopped || !got.WakeOnMessage {
		t.Fatalf("expected WakeOnMessage updated to true, got %+v", got)
	}
	if got.SessionID != "sid" {
		t.Fatal("SessionID must survive a re-stop")
	}
}

// TestStopAbortsBeforeMarkingOnStopAgentFailure verifies a live StopAgent
// failure is returned immediately, before the record is touched at all.
func TestStopAbortsBeforeMarkingOnStopAgentFailure(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home}
	sup := &capturingSupervisor{
		agents:  map[string]ProcessState{"leo-x": {Name: "leo-x", Status: "running"}},
		stopErr: errors.New("tmux kill failed"),
	}
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-x", Workspace: "/w", SessionID: "sid"})

	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	if err := m.Stop("leo-x", StopOptions{}); err == nil {
		t.Fatal("expected error when StopAgent fails")
	}

	recs, _ := agentstore.Load(agentstore.FilePath(home))
	if recs["leo-x"].Stopped {
		t.Fatal("record must not be marked Stopped when StopAgent fails")
	}
}

// --- Task 7: Manager.Start ---

func TestStartRespawnsWithResumeAndClearsFlags(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home}
	sup := &capturingSupervisor{}
	_ = agentstore.Save(home, agentstore.Record{
		Name:          "leo-x",
		Workspace:     "/w",
		SessionID:     "sid",
		ClaudeArgs:    []string{"--model", "sonnet", "--session-id", "sid"},
		Stopped:       true,
		WakeOnMessage: true,
	})

	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	if err := m.Start("leo-x"); err != nil {
		t.Fatalf("start: %v", err)
	}

	// spawned with --resume sid, no --session-id
	if sup.spawnCall == nil {
		t.Fatal("SpawnAgent was not called")
	}
	got := sup.spawnCall.ClaudeArgs
	if !containsPair(got, "--resume", "sid") || containsFlag(got, "--session-id") {
		t.Fatalf("start args wrong: %v", got)
	}

	recs, _ := agentstore.Load(agentstore.FilePath(home))
	saved := recs["leo-x"]
	if saved.Stopped {
		t.Error("Stopped flag not cleared after start")
	}
	if saved.WakeOnMessage {
		t.Error("WakeOnMessage flag not cleared after start")
	}

	// starting an already-live agent errors
	sup.agents["leo-x"] = ProcessState{Name: "leo-x", Status: "running"}
	if err := m.Start("leo-x"); !errors.Is(err, ErrAgentAlreadyRunning) {
		t.Fatalf("expected ErrAgentAlreadyRunning, got %v", err)
	}

	// starting an unknown agent errors
	if err := m.Start("ghost"); err == nil {
		t.Fatal("starting unknown agent should error")
	}
}

// TestStartNotStoppedErrors verifies Start refuses a persisted record that is
// not dormant (e.g. a live-but-record-stale mismatch, or a plain live agent
// whose record forgot to set Stopped).
func TestStartNotStoppedErrors(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home}
	sup := &capturingSupervisor{}
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-x", Workspace: "/w", SessionID: "sid"})

	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	err := m.Start("leo-x")
	if !errors.Is(err, ErrAgentNotStopped) {
		t.Fatalf("expected ErrAgentNotStopped, got %v", err)
	}
}

// TestStartReResolvesArgsAndEnv: starting a dormant agent must apply today's
// template config and harness env, not replay the wiring frozen at spawn.
func TestStartReResolvesArgsAndEnv(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{
		HomePath:  home,
		Templates: map[string]config.TemplateConfig{"coding": {Model: "opus"}},
	}
	sup := &capturingSupervisor{}
	_ = agentstore.Save(home, agentstore.Record{
		Name:       "leo-x",
		Template:   "coding",
		Workspace:  "/w",
		SessionID:  "sid",
		ClaudeArgs: []string{"--model", "sonnet", "--session-id", "sid"},
		Env:        map[string]string{"LEGACY_KEY": "v"},
		Stopped:    true,
	})

	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	if err := m.Start("leo-x"); err != nil {
		t.Fatalf("start: %v", err)
	}

	got := sup.spawnCall.ClaudeArgs
	if !containsPair(got, "--model", "opus") {
		t.Errorf("args not re-resolved to the current template model: %v", got)
	}
	// Start's own mechanic still holds: --resume, never --session-id.
	if !containsPair(got, "--resume", "sid") || containsFlag(got, "--session-id") {
		t.Errorf("start session args wrong: %v", got)
	}
	want := strconv.FormatInt(leomcp.ToolTimeout.Milliseconds(), 10)
	if sup.spawnCall.Env["MCP_TOOL_TIMEOUT"] != want {
		t.Errorf("harness env not refreshed on start: %v", sup.spawnCall.Env)
	}
	if sup.spawnCall.Env["LEGACY_KEY"] != "v" {
		t.Errorf("caller-supplied env dropped on start: %v", sup.spawnCall.Env)
	}

	// The re-resolved wiring must be persisted, exactly as Restart persists
	// it. Otherwise the record keeps describing the stale wiring the agent is
	// no longer running, and StaleAgents would report the same agent as
	// drifted after every update, forever.
	recs, _ := agentstore.Load(agentstore.FilePath(home))
	saved := recs["leo-x"]
	if saved.Env["MCP_TOOL_TIMEOUT"] != want {
		t.Errorf("started record did not persist the refreshed env: %v", saved.Env)
	}
	if !containsPair(saved.ClaudeArgs, "--model", "opus") {
		t.Errorf("started record did not persist the re-resolved args: %v", saved.ClaudeArgs)
	}
}

// TestStartKeepsStoredArgsWhenNotReResolvable: an ad-hoc agent (no template)
// has nothing to re-resolve from, so start must fall back to its stored
// wiring rather than spawning it bare.
func TestStartKeepsStoredArgsWhenNotReResolvable(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home}
	sup := &capturingSupervisor{}
	_ = agentstore.Save(home, agentstore.Record{
		Name:       "leo-x",
		Workspace:  "/w",
		SessionID:  "sid",
		ClaudeArgs: []string{"--model", "sonnet", "--session-id", "sid"},
		Env:        map[string]string{"ONLY": "stored"},
		Stopped:    true,
	})

	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	if err := m.Start("leo-x"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !containsPair(sup.spawnCall.ClaudeArgs, "--model", "sonnet") {
		t.Errorf("stored args not preserved: %v", sup.spawnCall.ClaudeArgs)
	}
	if sup.spawnCall.Env["ONLY"] != "stored" {
		t.Errorf("stored env not preserved: %v", sup.spawnCall.Env)
	}
}

// --- Reset ---

// TestResetStopsClearsAndRespawns asserts Reset's stop -> clear -> start
// order for a live claude agent, and that the record ends up with a fresh
// (different) SessionID rather than the pre-reset one, with NoResume cleared
// again once the fresh spawn succeeds.
func TestResetStopsClearsAndRespawns(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home}
	sup := &capturingSupervisor{
		agents: map[string]ProcessState{
			"leo-x": {Name: "leo-x", Status: "running"},
		},
	}
	_ = agentstore.Save(home, agentstore.Record{
		Name:       "leo-x",
		Workspace:  "/w",
		SessionID:  "old-sid",
		ClaudeArgs: []string{"--model", "sonnet", "--session-id", "old-sid"},
	})

	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	if err := m.Reset("leo-x"); err != nil {
		t.Fatalf("reset: %v", err)
	}

	// Order: StopAgent before SpawnAgent.
	wantOrder := []string{"stop:leo-x", "spawn:leo-x"}
	if len(sup.callOrder) != len(wantOrder) {
		t.Fatalf("callOrder = %v, want %v", sup.callOrder, wantOrder)
	}
	for i, c := range wantOrder {
		if sup.callOrder[i] != c {
			t.Fatalf("callOrder[%d] = %q, want %q (full: %v)", i, sup.callOrder[i], c, sup.callOrder)
		}
	}

	if sup.spawnCall == nil {
		t.Fatal("SpawnAgent was not called")
	}
	// Fresh spawn: a --session-id, never --resume, and NOT the old id.
	if containsFlag(sup.spawnCall.ClaudeArgs, "--resume") {
		t.Fatalf("reset spawn must not pass --resume: %v", sup.spawnCall.ClaudeArgs)
	}
	if containsPair(sup.spawnCall.ClaudeArgs, "--session-id", "old-sid") {
		t.Fatalf("reset spawn must not reuse the old session id: %v", sup.spawnCall.ClaudeArgs)
	}
	if !containsFlag(sup.spawnCall.ClaudeArgs, "--session-id") {
		t.Fatalf("reset spawn (claude) should pass a fresh --session-id: %v", sup.spawnCall.ClaudeArgs)
	}

	recs, _ := agentstore.Load(agentstore.FilePath(home))
	got := recs["leo-x"]
	if got.SessionID == "" || got.SessionID == "old-sid" {
		t.Fatalf("record SessionID = %q, want a fresh non-empty id", got.SessionID)
	}
	if got.NoResume {
		t.Fatal("NoResume must be cleared once the fresh spawn succeeds")
	}
}

// TestResetSkipsStopWhenNotLive verifies Reset on a suspended (non-live) agent
// clears state and respawns without calling StopAgent (which would error
// "not found" for a dead process).
func TestResetSkipsStopWhenNotLive(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home}
	sup := &capturingSupervisor{}
	_ = agentstore.Save(home, agentstore.Record{
		Name:       "leo-x",
		Workspace:  "/w",
		SessionID:  "old-sid",
		ClaudeArgs: []string{"--session-id", "old-sid"},
		Stopped:    true,
	})

	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	if err := m.Reset("leo-x"); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if len(sup.stopCalls) != 0 {
		t.Fatalf("StopAgent must not be called for a non-live agent, got %v", sup.stopCalls)
	}
	if sup.spawnCall == nil {
		t.Fatal("SpawnAgent was not called")
	}
}

// TestResetUnknownAgentErrors verifies Reset errors on a name with no
// persisted agentstore record.
func TestResetUnknownAgentErrors(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home}
	sup := &capturingSupervisor{}
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	if err := m.Reset("ghost"); err == nil {
		t.Fatal("resetting an unknown agent should error")
	}
}

// TestResetSpawnFailureLeavesRecoverableState verifies that when the early
// clear-and-save succeeds but the respawn fails, Reset returns an error and
// leaves the record in the documented interim state (SessionID cleared,
// NoResume set) — not live, not resumable, but recognizable
// as "mid-reset" — and that simply re-running Reset on the same name recovers:
// StopAgent is skipped (the agent isn't live) and the spawn is retried.
func TestResetSpawnFailureLeavesRecoverableState(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home}
	sup := &capturingSupervisor{
		agents: map[string]ProcessState{
			"leo-x": {Name: "leo-x", Status: "running"},
		},
		spawnErr: errors.New("supervisor boom"),
	}
	_ = agentstore.Save(home, agentstore.Record{
		Name:       "leo-x",
		Workspace:  "/w",
		SessionID:  "old-sid",
		ClaudeArgs: []string{"--model", "sonnet", "--session-id", "old-sid"},
	})

	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	err := m.Reset("leo-x")
	if err == nil {
		t.Fatal("expected error when SpawnAgent fails")
	}
	if !strings.Contains(err.Error(), "leo agent reset leo-x") {
		t.Fatalf("error should point at the recovery command, got: %v", err)
	}

	recs, _ := agentstore.Load(agentstore.FilePath(home))
	got := recs["leo-x"]
	if got.SessionID != "" {
		t.Fatalf("interim SessionID = %q, want empty", got.SessionID)
	}
	if !got.NoResume {
		t.Fatal("interim NoResume must be true")
	}
	// Recovery: re-running Reset skips the stop (agent no longer live, since
	// the earlier StopAgent call succeeded before the spawn failed) and
	// retries the spawn.
	sup.spawnErr = nil
	if err := m.Reset("leo-x"); err != nil {
		t.Fatalf("recovery reset: %v", err)
	}
	if len(sup.stopCalls) != 1 {
		t.Fatalf("recovery reset must not call StopAgent again, stopCalls = %v", sup.stopCalls)
	}

	recs, _ = agentstore.Load(agentstore.FilePath(home))
	got = recs["leo-x"]
	if got.SessionID == "" {
		t.Fatal("recovered record should have a fresh SessionID")
	}
	if got.NoResume {
		t.Fatal("recovered record NoResume must be cleared")
	}
}

// TestResetStopFailureAbortsBeforeClearing verifies a StopAgent failure stops
// Reset before the record is mutated, so a failed reset leaves the original
// session id intact rather than orphaning the agent mid-transition.
func TestResetStopFailureAbortsBeforeClearing(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home}
	sup := &capturingSupervisor{
		agents:  map[string]ProcessState{"leo-x": {Name: "leo-x", Status: "running"}},
		stopErr: errors.New("tmux kill failed"),
	}
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-x", Workspace: "/w", SessionID: "old-sid"})

	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	if err := m.Reset("leo-x"); err == nil {
		t.Fatal("expected error when StopAgent fails")
	}
	if sup.spawnCall != nil {
		t.Fatal("SpawnAgent must not be called when StopAgent fails")
	}

	recs, _ := agentstore.Load(agentstore.FilePath(home))
	if recs["leo-x"].SessionID != "old-sid" {
		t.Fatal("SessionID must be left untouched when reset aborts on stop failure")
	}
}

// --- Restart ---

// TestRestartStopsAndRespawnsWithResume verifies Restart's stop -> respawn
// order for a live claude agent, that it passes --resume (not a fresh
// --session-id, unlike Reset), and that it never marks the record dormant.
func TestRestartStopsAndRespawnsWithResume(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home}
	sup := &capturingSupervisor{
		agents: map[string]ProcessState{
			"leo-x": {Name: "leo-x", Status: "running"},
		},
	}
	_ = agentstore.Save(home, agentstore.Record{
		Name:       "leo-x",
		Workspace:  "/w",
		SessionID:  "sid",
		ClaudeArgs: []string{"--model", "sonnet", "--session-id", "sid"},
	})

	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	if err := m.Restart("leo-x"); err != nil {
		t.Fatalf("restart: %v", err)
	}

	wantOrder := []string{"stop:leo-x", "spawn:leo-x"}
	if len(sup.callOrder) != len(wantOrder) {
		t.Fatalf("callOrder = %v, want %v", sup.callOrder, wantOrder)
	}
	for i, c := range wantOrder {
		if sup.callOrder[i] != c {
			t.Fatalf("callOrder[%d] = %q, want %q (full: %v)", i, sup.callOrder[i], c, sup.callOrder)
		}
	}

	if sup.spawnCall == nil {
		t.Fatal("SpawnAgent was not called")
	}
	got := sup.spawnCall.ClaudeArgs
	if !containsPair(got, "--resume", "sid") || containsFlag(got, "--session-id") {
		t.Fatalf("restart args wrong: %v", got)
	}

	recs, _ := agentstore.Load(agentstore.FilePath(home))
	rec := recs["leo-x"]
	if rec.Stopped {
		t.Fatal("Restart must never mark the record Stopped")
	}
	if rec.SessionID != "sid" {
		t.Fatalf("SessionID = %q, want unchanged %q", rec.SessionID, "sid")
	}
}

// TestRestartNotRunningErrors verifies Restart errors (rather than silently
// no-oping) on a stopped or unknown agent — callers driving --all treat
// this as "skip", but a direct single-name Restart must surface it.
func TestRestartNotRunningErrors(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home}
	sup := &capturingSupervisor{} // nothing live
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-x", Workspace: "/w", Stopped: true})

	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	if err := m.Restart("leo-x"); err == nil {
		t.Fatal("restarting a stopped (non-live) agent should error")
	}
	if err := m.Restart("ghost"); err == nil {
		t.Fatal("restarting an unknown agent should error")
	}
	if len(sup.stopCalls) != 0 {
		t.Fatalf("StopAgent must not be called for a non-live agent, got %v", sup.stopCalls)
	}
}

// TestRestartRecoversFailedRestoreRecord verifies Restart's store fallback:
// a shared-workspace agent RestoreAgents left behind after a failed boot-time
// restore (Stopped=true, StoppedReason set, not live) must be spawned fresh
// and have Stopped/StoppedReason cleared — StopAgent is never called since
// there is nothing live to stop.
func TestRestartRecoversFailedRestoreRecord(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home}
	sup := &capturingSupervisor{} // nothing live
	_ = agentstore.Save(home, agentstore.Record{
		Name:          "leo-x",
		Workspace:     "/w",
		SessionID:     "sid",
		ClaudeArgs:    []string{"--model", "sonnet", "--session-id", "sid"},
		Stopped:       true,
		StoppedReason: "workspace missing: /w",
	})

	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	if err := m.Restart("leo-x"); err != nil {
		t.Fatalf("restart: %v", err)
	}

	if len(sup.stopCalls) != 0 {
		t.Fatalf("StopAgent must not be called for a non-live record, got %v", sup.stopCalls)
	}
	if sup.spawnCall == nil {
		t.Fatal("SpawnAgent was not called")
	}

	recs, _ := agentstore.Load(agentstore.FilePath(home))
	rec := recs["leo-x"]
	if rec.Stopped {
		t.Error("Stopped should be cleared after a successful recovery restart")
	}
	if rec.StoppedReason != "" {
		t.Errorf("StoppedReason should be cleared after a successful recovery restart, got %q", rec.StoppedReason)
	}
}

// TestRestartStopFailureAbortsBeforeSpawn verifies a StopAgent failure stops
// Restart before any respawn is attempted, and the record is left untouched.
func TestRestartStopFailureAbortsBeforeSpawn(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home}
	sup := &capturingSupervisor{
		agents:  map[string]ProcessState{"leo-x": {Name: "leo-x", Status: "running"}},
		stopErr: errors.New("tmux kill failed"),
	}
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-x", Workspace: "/w", SessionID: "sid"})

	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	if err := m.Restart("leo-x"); err == nil {
		t.Fatal("expected error when StopAgent fails")
	}
	if sup.spawnCall != nil {
		t.Fatal("SpawnAgent must not be called when StopAgent fails")
	}

	recs, _ := agentstore.Load(agentstore.FilePath(home))
	if recs["leo-x"].SessionID != "sid" {
		t.Fatal("SessionID must be left untouched when restart aborts on stop failure")
	}
}

// TestRestartAllSkipsStoppedAndIsolatesFailures verifies RestartAll bounces
// every running agent, skips stopped ones, and keeps a per-agent failure from
// blocking the rest of the batch.
func TestRestartAllSkipsStoppedAndIsolatesFailures(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home}
	sup := &capturingSupervisor{
		agents: map[string]ProcessState{
			"leo-a": {Name: "leo-a", Status: "running"},
			"leo-b": {Name: "leo-b", Status: "running"},
		},
	}
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-a", Workspace: "/w"})
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-b", Workspace: "/w"})
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-stopped-shared", Workspace: "/w", Stopped: true})
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-stopped", Workspace: "/w", Branch: "feat", Stopped: true})

	// Wrap the supervisor so leo-b's respawn fails while leo-a succeeds,
	// proving one failure doesn't abort the batch.
	sup2 := &failingOnSpawnSupervisor{capturingSupervisor: sup, failName: "leo-b"}
	m := New(func() (*config.Config, error) { return cfg, nil }, sup2, "", "tok")

	result := m.RestartAll()

	if len(result.Restarted) != 1 || result.Restarted[0] != "leo-a" {
		t.Fatalf("Restarted = %v, want [leo-a]", result.Restarted)
	}
	if len(result.Skipped) != 2 {
		t.Fatalf("Skipped = %v, want 2 entries (both stopped)", result.Skipped)
	}
	if _, ok := result.Failed["leo-b"]; !ok {
		t.Fatalf("Failed = %v, want an entry for leo-b", result.Failed)
	}
}

// TestRestartAllBouncesLiveNonRunningStatuses verifies that live agents whose
// supervisor status is not the literal "running" — e.g. "starting" or
// "restarting" (crash-loop backoff) — are still bounced by RestartAll, not
// skipped. Only "suspended"/"stopped" records are intentionally skipped.
func TestRestartAllBouncesLiveNonRunningStatuses(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home}
	sup := &capturingSupervisor{
		agents: map[string]ProcessState{
			"leo-starting":   {Name: "leo-starting", Status: "starting"},
			"leo-restarting": {Name: "leo-restarting", Status: "restarting"},
		},
	}
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-starting", Workspace: "/w"})
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-restarting", Workspace: "/w"})

	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")

	result := m.RestartAll()

	if len(result.Restarted) != 2 {
		t.Fatalf("Restarted = %v, want both live agents bounced", result.Restarted)
	}
	if len(result.Skipped) != 0 {
		t.Fatalf("Skipped = %v, want none (starting/restarting are live)", result.Skipped)
	}
	if len(result.Failed) != 0 {
		t.Fatalf("Failed = %v, want none", result.Failed)
	}
}

// TestRestartAllIncludesFailedRestoreButSkipsUserStopped locks the fix for a
// fleet-scale recovery gap: RestartAll used to skip EVERY "stopped" record,
// including a shared-workspace agent the system left Stopped+StoppedReason
// after a failed boot-time restore (see markFailedRestore) — the only batch
// recovery control the web UI exposes. Such a record must now be retried
// alongside the live agents, while a genuinely user-stopped record
// (StoppedReason empty) must still be skipped.
func TestRestartAllIncludesFailedRestoreButSkipsUserStopped(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home}
	sup := &capturingSupervisor{
		agents: map[string]ProcessState{
			"leo-a": {Name: "leo-a", Status: "running"},
		},
	}
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-a", Workspace: "/w"})
	// A user-stopped WORKTREE record: List() only surfaces a "stopped"
	// shared-workspace record when StoppedReason is set (a genuinely
	// user-stopped shared record is deleted outright by Stop, not kept — see
	// Manager.List's doc comment), so a worktree record is what actually
	// exercises the "stopped, must be skipped" branch here.
	_ = agentstore.Save(home, agentstore.Record{
		Name: "leo-user-stopped", Workspace: "/w", Branch: "feat", Stopped: true,
	})
	_ = agentstore.Save(home, agentstore.Record{
		Name: "leo-failed-restore", Workspace: t.TempDir(),
		Stopped: true, StoppedReason: "workspace missing: /w",
	})

	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")

	result := m.RestartAll()

	if len(result.Restarted) != 2 {
		t.Fatalf("Restarted = %v, want leo-a and leo-failed-restore both bounced", result.Restarted)
	}
	restartedSet := map[string]bool{}
	for _, n := range result.Restarted {
		restartedSet[n] = true
	}
	if !restartedSet["leo-failed-restore"] {
		t.Errorf("expected leo-failed-restore to be restarted, got Restarted=%v", result.Restarted)
	}
	if len(result.Skipped) != 1 || result.Skipped[0] != "leo-user-stopped" {
		t.Fatalf("Skipped = %v, want only [leo-user-stopped]", result.Skipped)
	}
	if len(result.Failed) != 0 {
		t.Fatalf("Failed = %v, want none", result.Failed)
	}

	recs, _ := agentstore.Load(agentstore.FilePath(home))
	if got := recs["leo-failed-restore"]; got.Stopped || got.StoppedReason != "" {
		t.Errorf("expected Stopped/StoppedReason cleared after successful recovery restart, got Stopped=%v StoppedReason=%q", got.Stopped, got.StoppedReason)
	}
}

// --- Restart config re-resolution ---

// TestRestartReResolvesTemplateSpawnedAgent verifies that restarting an agent
// spawned from a template that still exists in cfg, with its harness
// unchanged, rebuilds ClaudeArgs from today's template+defaults cascade (so a
// harness_options/model change made after the agent started is picked up)
// while still passing --resume, not a fresh --session-id.
func TestRestartReResolvesTemplateSpawnedAgent(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{
		HomePath: home,
		Templates: map[string]config.TemplateConfig{
			"coding": {Model: "opus", MaxTurns: 200},
		},
	}
	sup := &capturingSupervisor{
		agents: map[string]ProcessState{"leo-x": {Name: "leo-x", Status: "running"}},
	}
	_ = agentstore.Save(home, agentstore.Record{
		Name:       "leo-x",
		Template:   "coding",
		Workspace:  "/w",
		SessionID:  "sid",
		ClaudeArgs: []string{"--model", "sonnet", "--session-id", "sid"},
	})

	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	if err := m.Restart("leo-x"); err != nil {
		t.Fatalf("restart: %v", err)
	}

	got := sup.spawnCall.ClaudeArgs
	if !containsPair(got, "--model", "opus") {
		t.Fatalf("expected re-resolved args to use the current template's model, got: %v", got)
	}
	if !containsPair(got, "--max-turns", "200") {
		t.Fatalf("expected re-resolved args to include the current template's max-turns, got: %v", got)
	}
	if !containsPair(got, "--resume", "sid") || containsFlag(got, "--session-id") {
		t.Fatalf("restart args wrong: %v", got)
	}

	recs, _ := agentstore.Load(agentstore.FilePath(home))
	if !containsPair(recs["leo-x"].ClaudeArgs, "--model", "opus") {
		t.Fatalf("persisted record should store the re-resolved args, got: %v", recs["leo-x"].ClaudeArgs)
	}
}

// TestRestartAdHocAgentKeepsStoredArgs verifies an agent with no Template
// (ad-hoc/from-agent spawn) falls back to its stored args unchanged — there
// is nothing to re-resolve against.
func TestRestartAdHocAgentKeepsStoredArgs(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home}
	sup := &capturingSupervisor{
		agents: map[string]ProcessState{"leo-x": {Name: "leo-x", Status: "running"}},
	}
	stored := []string{"--model", "sonnet", "--session-id", "sid"}
	_ = agentstore.Save(home, agentstore.Record{
		Name:       "leo-x",
		Workspace:  "/w",
		SessionID:  "sid",
		ClaudeArgs: stored,
	})

	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	if err := m.Restart("leo-x"); err != nil {
		t.Fatalf("restart: %v", err)
	}

	got := sup.spawnCall.ClaudeArgs
	if !containsPair(got, "--model", "sonnet") || !containsPair(got, "--resume", "sid") {
		t.Fatalf("expected stored args (minus session-id, plus resume), got: %v", got)
	}
}

// TestRestartDeletedTemplateKeepsStoredArgs verifies an agent whose Template
// no longer exists in cfg falls back to its stored args unchanged, rather
// than erroring or silently dropping the invocation.
func TestRestartDeletedTemplateKeepsStoredArgs(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home} // no "coding" template
	sup := &capturingSupervisor{
		agents: map[string]ProcessState{"leo-x": {Name: "leo-x", Status: "running"}},
	}
	_ = agentstore.Save(home, agentstore.Record{
		Name:       "leo-x",
		Template:   "coding",
		Workspace:  "/w",
		SessionID:  "sid",
		ClaudeArgs: []string{"--model", "sonnet", "--session-id", "sid"},
	})

	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	if err := m.Restart("leo-x"); err != nil {
		t.Fatalf("restart: %v", err)
	}

	got := sup.spawnCall.ClaudeArgs
	if !containsPair(got, "--model", "sonnet") || !containsPair(got, "--resume", "sid") {
		t.Fatalf("expected stored args to survive a deleted template, got: %v", got)
	}
}

// TestRestartHarnessChangedKeepsStoredArgs verifies an agent whose effective
// harness changed since it was spawned falls back to its stored args — the
// resume mechanic, MCP bridge, and env shape differ per harness, so swapping
// harness under a resumed conversation is not supported.
func TestRestartHarnessChangedKeepsStoredArgs(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{
		HomePath: home,
		Templates: map[string]config.TemplateConfig{
			"coding": {Harness: "codex"},
		},
	}
	sup := &capturingSupervisor{
		agents: map[string]ProcessState{"leo-x": {Name: "leo-x", Status: "running"}},
	}
	_ = agentstore.Save(home, agentstore.Record{
		Name:       "leo-x",
		Template:   "coding",
		Harness:    "claude",
		Workspace:  "/w",
		SessionID:  "sid",
		ClaudeArgs: []string{"--model", "sonnet", "--session-id", "sid"},
	})

	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	if err := m.Restart("leo-x"); err != nil {
		t.Fatalf("restart: %v", err)
	}

	got := sup.spawnCall.ClaudeArgs
	if !containsPair(got, "--model", "sonnet") || !containsPair(got, "--resume", "sid") {
		t.Fatalf("expected stored args to survive a harness change, got: %v", got)
	}
}

// TestRestartSpawnEnvOverlayWinsAndSurvives verifies a re-resolved restart
// rebuilds Env as mergeEnv(mergeEnv(newHarnessEnv, tmpl.Env), rec.SpawnEnv),
// so the per-spawn overlay wins over the template's env and survives the
// re-resolve.
func TestRestartSpawnEnvOverlayWinsAndSurvives(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{
		HomePath: home,
		Templates: map[string]config.TemplateConfig{
			"coding": {Env: map[string]string{"FOO": "template", "BAR": "template"}},
		},
	}
	sup := &capturingSupervisor{
		agents: map[string]ProcessState{"leo-x": {Name: "leo-x", Status: "running"}},
	}
	_ = agentstore.Save(home, agentstore.Record{
		Name:       "leo-x",
		Template:   "coding",
		Workspace:  "/w",
		SessionID:  "sid",
		ClaudeArgs: []string{"--session-id", "sid"},
		Env:        map[string]string{"FOO": "spawn-override", "BAR": "template"},
		SpawnEnv:   map[string]string{"FOO": "spawn-override"},
	})

	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	if err := m.Restart("leo-x"); err != nil {
		t.Fatalf("restart: %v", err)
	}

	env := sup.spawnCall.Env
	if env["FOO"] != "spawn-override" {
		t.Fatalf("SpawnEnv overlay should win over template.Env, got FOO=%q", env["FOO"])
	}
	if env["BAR"] != "template" {
		t.Fatalf("expected template.Env's BAR to survive re-resolve, got BAR=%q", env["BAR"])
	}
}

// TestRestartFreshHarnessEnvWinsOverStaleInheritedEnv verifies that on a
// re-resolving restart, a key the CURRENT harness env now defines beats a
// stale value carried in rec.InheritedEnv (a worktree/from-agent spawn's
// inherited layer) — the inherited layer is re-pruned against the fresh
// harness env at restart time, not replayed from its spawn-time snapshot.
// Reachable today via opencode's OPENCODE_CONFIG_CONTENT harness env.
func TestRestartFreshHarnessEnvWinsOverStaleInheritedEnv(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{
		HomePath: home,
		Templates: map[string]config.TemplateConfig{
			"coding": {Harness: "opencode"},
		},
	}
	sup := &capturingSupervisor{
		agents: map[string]ProcessState{"leo-x": {Name: "leo-x", Status: "running"}},
	}
	_ = agentstore.Save(home, agentstore.Record{
		Name:      "leo-x",
		Template:  "coding",
		Harness:   "opencode",
		Workspace: "/w",
		SessionID: "sid",
		Env:       map[string]string{"OPENCODE_CONFIG_CONTENT": "stale-inherited"},
		InheritedEnv: map[string]string{
			"OPENCODE_CONFIG_CONTENT": "stale-inherited",
			"OTHER_INHERITED":         "carries-over",
		},
	})

	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	if err := m.Restart("leo-x"); err != nil {
		t.Fatalf("restart: %v", err)
	}

	env := sup.spawnCall.Env
	if env["OPENCODE_CONFIG_CONTENT"] == "stale-inherited" {
		t.Fatalf("fresh harness env should win over stale InheritedEnv, got %q", env["OPENCODE_CONFIG_CONTENT"])
	}
	if env["OTHER_INHERITED"] != "carries-over" {
		t.Fatalf("expected non-shadowed InheritedEnv key to survive, got: %v", env)
	}
}

// TestRestartSpawnEnvOverlayWinsOverFreshHarnessEnv verifies that SpawnEnv
// (an explicit --env override) beats even the freshly re-resolved harness
// env on restart, matching spawn-time layering where the caller's env is
// always the top overlay.
func TestRestartSpawnEnvOverlayWinsOverFreshHarnessEnv(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{
		HomePath: home,
		Templates: map[string]config.TemplateConfig{
			"coding": {Harness: "opencode"},
		},
	}
	sup := &capturingSupervisor{
		agents: map[string]ProcessState{"leo-x": {Name: "leo-x", Status: "running"}},
	}
	_ = agentstore.Save(home, agentstore.Record{
		Name:      "leo-x",
		Template:  "coding",
		Harness:   "opencode",
		Workspace: "/w",
		SessionID: "sid",
		Env:       map[string]string{"OPENCODE_CONFIG_CONTENT": "explicit-override"},
		SpawnEnv:  map[string]string{"OPENCODE_CONFIG_CONTENT": "explicit-override"},
	})

	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	if err := m.Restart("leo-x"); err != nil {
		t.Fatalf("restart: %v", err)
	}

	env := sup.spawnCall.Env
	if env["OPENCODE_CONFIG_CONTENT"] != "explicit-override" {
		t.Fatalf("SpawnEnv override should win over fresh harness env, got %q", env["OPENCODE_CONFIG_CONTENT"])
	}
}

// TestRestartLegacyRecordKeepsCallerEnvAndReResolvesArgs verifies a legacy
// record (SpawnEnv nil, written before the field existed) gets its args
// re-resolved while its caller-supplied env survives: leo can't tell which
// layer produced which stored key, so it layers rather than reconstructs and
// never silently drops env. LEGACY_KEY here is not harness-owned; the other
// half of the contract — harness-owned keys taking today's value — is covered
// by TestRestartLegacyRecordPicksUpNewHarnessEnv and
// TestRestartLegacyRecordFreshHarnessEnvBeatsStaleCopy below.
func TestRestartLegacyRecordKeepsCallerEnvAndReResolvesArgs(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{
		HomePath: home,
		Templates: map[string]config.TemplateConfig{
			"coding": {Model: "opus"},
		},
	}
	sup := &capturingSupervisor{
		agents: map[string]ProcessState{"leo-x": {Name: "leo-x", Status: "running"}},
	}
	legacyEnv := map[string]string{"LEGACY_KEY": "legacy-value"}
	_ = agentstore.Save(home, agentstore.Record{
		Name:       "leo-x",
		Template:   "coding",
		Workspace:  "/w",
		SessionID:  "sid",
		ClaudeArgs: []string{"--model", "sonnet", "--session-id", "sid"},
		Env:        legacyEnv,
		SpawnEnv:   nil, // legacy: predates SpawnEnv
	})

	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	if err := m.Restart("leo-x"); err != nil {
		t.Fatalf("restart: %v", err)
	}

	got := sup.spawnCall.ClaudeArgs
	if !containsPair(got, "--model", "opus") {
		t.Fatalf("expected args to re-resolve to the current template's model, got: %v", got)
	}
	if sup.spawnCall.Env["LEGACY_KEY"] != "legacy-value" {
		t.Fatalf("expected legacy Env to survive unchanged, got: %v", sup.spawnCall.Env)
	}
}

// TestRestartLegacyRecordPicksUpNewHarnessEnv covers the other half of the
// legacy-record contract: keeping stored env must not mean freezing the
// harness env. A harness env var introduced by a leo upgrade (here
// MCP_TOOL_TIMEOUT) has to reach the agent on restart, or `leo agent restart`
// silently no-ops for every env-delivered fix and the only remedy left is a
// full reset — which throws away the conversation restart exists to keep.
func TestRestartLegacyRecordPicksUpNewHarnessEnv(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{
		HomePath:  home,
		Templates: map[string]config.TemplateConfig{"coding": {Model: "opus"}},
	}
	sup := &capturingSupervisor{
		agents: map[string]ProcessState{"leo-x": {Name: "leo-x", Status: "running"}},
	}
	_ = agentstore.Save(home, agentstore.Record{
		Name:       "leo-x",
		Template:   "coding",
		Workspace:  "/w",
		SessionID:  "sid",
		ClaudeArgs: []string{"--model", "sonnet", "--session-id", "sid"},
		Env:        map[string]string{"LEGACY_KEY": "legacy-value"},
		SpawnEnv:   nil, // legacy: predates SpawnEnv
	})

	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	if err := m.Restart("leo-x"); err != nil {
		t.Fatalf("restart: %v", err)
	}

	want := strconv.FormatInt(leomcp.ToolTimeout.Milliseconds(), 10)
	if got := sup.spawnCall.Env["MCP_TOOL_TIMEOUT"]; got != want {
		t.Errorf("MCP_TOOL_TIMEOUT = %q, want %q (env %v)", got, want, sup.spawnCall.Env)
	}
	// Layering, not reconstructing: the caller's own key still survives.
	if sup.spawnCall.Env["LEGACY_KEY"] != "legacy-value" {
		t.Errorf("legacy env key was dropped: %v", sup.spawnCall.Env)
	}
}

// TestRestartLegacyRecordFreshHarnessEnvBeatsStaleCopy pins the precedence
// within a legacy record: a harness-owned key stored at spawn time is stale
// by definition, so today's harness value must win over the record's copy.
// User-supplied keys the harness does not own are untouched (covered above).
func TestRestartLegacyRecordFreshHarnessEnvBeatsStaleCopy(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{
		HomePath:  home,
		Templates: map[string]config.TemplateConfig{"coding": {Model: "opus"}},
	}
	sup := &capturingSupervisor{
		agents: map[string]ProcessState{"leo-x": {Name: "leo-x", Status: "running"}},
	}
	_ = agentstore.Save(home, agentstore.Record{
		Name:       "leo-x",
		Template:   "coding",
		Workspace:  "/w",
		SessionID:  "sid",
		ClaudeArgs: []string{"--model", "sonnet", "--session-id", "sid"},
		Env:        map[string]string{"MCP_TOOL_TIMEOUT": "1", "LEGACY_KEY": "legacy-value"},
		SpawnEnv:   nil,
	})

	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	if err := m.Restart("leo-x"); err != nil {
		t.Fatalf("restart: %v", err)
	}

	want := strconv.FormatInt(leomcp.ToolTimeout.Milliseconds(), 10)
	if got := sup.spawnCall.Env["MCP_TOOL_TIMEOUT"]; got != want {
		t.Errorf("stale harness value survived: MCP_TOOL_TIMEOUT = %q, want %q", got, want)
	}
	if sup.spawnCall.Env["LEGACY_KEY"] != "legacy-value" {
		t.Errorf("legacy env key was dropped: %v", sup.spawnCall.Env)
	}
}

// failingOnSpawnSupervisor wraps capturingSupervisor to fail SpawnAgent only
// for a single named agent, letting a RestartAll test exercise per-agent
// failure isolation without one shared spawnErr field failing everything.
type failingOnSpawnSupervisor struct {
	*capturingSupervisor
	failName string
}

func (s *failingOnSpawnSupervisor) SpawnAgent(req SpawnRequest) error {
	if req.Name == s.failName {
		s.callOrder = append(s.callOrder, "spawn:"+req.Name)
		return errors.New("spawn boom")
	}
	return s.capturingSupervisor.SpawnAgent(req)
}

// TestStopOnAlreadyDormantWorktreeAgentUpdatesFlagAndSkipsStopAgent verifies
// Stop on an already-dormant (WakeOnMessage=true) worktree agent skips
// StopAgent (it would error "not found" on a dead process) but still updates
// the record's WakeOnMessage to the newly requested value.
func TestStopOnAlreadyDormantWorktreeAgentUpdatesFlagAndSkipsStopAgent(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home}
	sup := &capturingSupervisor{} // no live agents — the agent is already dormant
	_ = agentstore.Save(home, agentstore.Record{
		Name:          "leo-x",
		Workspace:     "/w",
		Branch:        "feat/x",
		SessionID:     "sid",
		Stopped:       true,
		WakeOnMessage: true,
	})

	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	if err := m.Stop("leo-x", StopOptions{WakeOnMessage: false}); err != nil {
		t.Fatalf("stop dormant agent: %v", err)
	}

	if len(sup.stopCalls) != 0 {
		t.Fatalf("StopAgent must not be called for a non-live agent, got %v", sup.stopCalls)
	}

	recs, _ := agentstore.Load(agentstore.FilePath(home))
	got, ok := recs["leo-x"]
	if !ok {
		t.Fatal("worktree record should survive Stop")
	}
	if !got.Stopped {
		t.Error("record should be marked Stopped=true")
	}
	if got.WakeOnMessage {
		t.Error("WakeOnMessage should be updated to false so the agent does not auto-wake")
	}
}

// TestStopUnknownAgentErrors verifies Stop still errors on a name that is
// neither live nor persisted — the liveness guard must not turn an unknown
// agent into a silent success.
func TestStopUnknownAgentErrors(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home}
	sup := &capturingSupervisor{} // nothing live, nothing stored
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	if err := m.Stop("ghost", StopOptions{}); err == nil {
		t.Fatal("stopping an unknown agent should error")
	}
}

// --- Task 8: List surfaces stopped agents ---

func TestListSurfacesStoppedAgents(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home}
	sup := &capturingSupervisor{} // no live agents
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-shared", Workspace: "/w", Stopped: true})
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-wt", Workspace: "/w2", Branch: "feat", Stopped: true})

	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	got := m.List()

	statuses := map[string]string{}
	for _, r := range got {
		statuses[r.Name] = r.Status
	}
	if statuses["leo-shared"] != "stopped" {
		t.Fatalf("stopped shared agent missing/wrong: %v", statuses)
	}
	if statuses["leo-wt"] != "stopped" {
		t.Fatalf("stopped worktree agent missing/wrong: %v", statuses)
	}
}

// TestListSharedStoppedRecordStaysVisible locks the inversion at the heart of
// this change: a shared-workspace record — whether stopped by a failed
// restore (StoppedReason set) or by a plain Stop (no reason) — must stay
// visible in List with status "stopped", exactly like a worktree record.
// Manager.Stop no longer deletes shared-workspace records, so neither case
// drops out of the list anymore.
func TestListSharedStoppedRecordStaysVisible(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home}
	sup := &capturingSupervisor{} // no live agents
	_ = agentstore.Save(home, agentstore.Record{
		Name: "leo-failed-restore", Workspace: "/w", Stopped: true, StoppedReason: "workspace missing: /w",
	})
	_ = agentstore.Save(home, agentstore.Record{
		Name: "leo-stopped-no-reason", Workspace: "/w2", Stopped: true,
	})

	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	got := m.List()

	statuses := map[string]string{}
	for _, r := range got {
		statuses[r.Name] = r.Status
	}
	if statuses["leo-failed-restore"] != "stopped" {
		t.Fatalf("shared record stopped by a failed restore missing/wrong: %v", statuses)
	}
	if statuses["leo-stopped-no-reason"] != "stopped" {
		t.Fatalf("shared record stopped with no reason should stay visible too, got %v", statuses)
	}
}

func TestListReturnsStableSortedOrder(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home}
	sup := &capturingSupervisor{} // no live agents; store-only ordering

	// Insert out of alphabetical order across enough records that a randomized
	// map iteration would almost certainly not land on sorted order by chance.
	for _, name := range []string{"leo-delta", "leo-alpha", "leo-charlie", "leo-echo", "leo-bravo"} {
		_ = agentstore.Save(home, agentstore.Record{Name: name, Workspace: "/w", Stopped: true})
	}

	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")

	want := []string{"leo-alpha", "leo-bravo", "leo-charlie", "leo-delta", "leo-echo"}
	// Repeat: map iteration order is randomized per range, so a single pass could
	// coincidentally be sorted. Every pass must come back sorted.
	for i := 0; i < 20; i++ {
		got := m.List()
		names := make([]string, len(got))
		for j, r := range got {
			names[j] = r.Name
		}
		if !slices.Equal(names, want) {
			t.Fatalf("pass %d: List() order = %v, want %v", i, names, want)
		}
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
