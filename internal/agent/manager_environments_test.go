package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/observe"
	"github.com/blackpaw-studio/leo/internal/session"
)

// envTestConfig has one claude template, two accounts and a shared base.
func envTestConfig(home, ws, acctA, acctB string) *config.Config {
	return &config.Config{
		HomePath: home,
		Environments: map[string]map[string]string{
			"base":   {"FOO": "base", "SHARED": "base"},
			"acct-a": {"CLAUDE_CONFIG_DIR": acctA, "SHARED": "a"},
			"acct-b": {"CLAUDE_CONFIG_DIR": acctB, "SHARED": "b"},
		},
		Templates: map[string]config.TemplateConfig{
			"coding": {Workspace: ws, Model: "sonnet", Environments: []string{"base", "acct-a"}, Env: map[string]string{"LITERAL": "tmpl"}},
		},
	}
}

func envManager(cfg *config.Config, sup *capturingSupervisor) *Manager {
	return New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
}

func TestSpawnLayersNamedEnvironmentsLiteralAndSpawnEnv(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home, ws, a, b := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	cfg := envTestConfig(home, ws, a, b)
	cfg.Templates["coding"] = config.TemplateConfig{Workspace: ws, Environments: []string{"base", "acct-a"}, Env: map[string]string{"SHARED": "literal", "LITERAL": "tmpl"}}
	sup := &capturingSupervisor{}
	m := envManager(cfg, sup)

	rec, err := m.Spawn(context.Background(), SpawnSpec{Template: "coding", Repo: "x", Environments: []string{"base", "acct-b"}, Env: map[string]string{"FOO": "spawn"}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"CLAUDE_CONFIG_DIR": b, "SHARED": "literal", "LITERAL": "tmpl", "FOO": "spawn"}
	got := sup.spawnCall.Env
	for k, v := range want {
		if got[k] != v {
			t.Errorf("spawn env[%s] = %q, want %q (full: %v)", k, got[k], v, got)
		}
	}
	stored := loadRec(t, home, rec.Name)
	if !reflect.DeepEqual(stored.Environments, []string{"base", "acct-b"}) || !stored.EnvLayered {
		t.Fatalf("record = environments %v layered %v; want the override names and layered", stored.Environments, stored.EnvLayered)
	}
}

func TestSpawnUsesTemplateEnvironmentsWhenNotOverridden(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home, ws, a, b := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	cfg := envTestConfig(home, ws, a, b)
	sup := &capturingSupervisor{}
	rec, err := envManager(cfg, sup).Spawn(context.Background(), SpawnSpec{Template: "coding", Repo: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if sup.spawnCall.Env["CLAUDE_CONFIG_DIR"] != a {
		t.Fatalf("env = %v, want the template default account %s", sup.spawnCall.Env, a)
	}
	if stored := loadRec(t, home, rec.Name); stored.Environments != nil {
		t.Fatalf("record stored %v; only a spawn override is stored so config edits apply on restart", stored.Environments)
	}
}

func TestSpawnUnknownEnvironmentFailsBeforeAnySideEffect(t *testing.T) {
	home, ws, a, b := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	cfg := envTestConfig(home, ws, a, b)
	sup := &capturingSupervisor{}
	_, err := envManager(cfg, sup).Spawn(context.Background(), SpawnSpec{Template: "coding", Repo: "x", Environments: []string{"nope"}})
	var unknown *config.UnknownEnvironmentError
	if !errors.As(err, &unknown) || unknown.Name != "nope" {
		t.Fatalf("err = %v, want UnknownEnvironmentError{nope}", err)
	}
	if sup.spawnCall != nil || len(sup.reservations) != 0 {
		t.Fatalf("side effects: spawn %v reservations %v", sup.spawnCall, sup.reservations)
	}
	if recs, _ := agentstore.Load(agentstore.FilePath(home)); len(recs) != 0 {
		t.Fatalf("records written: %v", recs)
	}
}

// savedLayeredAgent persists a running, layered agent on acct-a with a
// transcript under a's projects dir.
func savedLayeredAgent(t *testing.T, cfg *config.Config, override []string) (agentstore.Record, *capturingSupervisor) {
	t.Helper()
	ws := t.TempDir()
	tmpl := cfg.Templates["coding"]
	env, err := cfg.ResolveEnv(cfg.EnvironmentNames(override, tmpl.Environments), tmpl.Env, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := agentstore.Record{
		Name: "leo-x", Template: "coding", Harness: "claude", Workspace: ws,
		SessionID: "stored-session", ClaudeArgs: []string{"--model", "sonnet", "--session-id", "stored-session"},
		Env: env, Environments: override, EnvLayered: true,
	}
	if err := agentstore.Save(cfg.HomePath, rec); err != nil {
		t.Fatal(err)
	}
	sup := &capturingSupervisor{agents: map[string]ProcessState{"leo-x": {Name: "leo-x", Status: "running"}}}
	return rec, sup
}

func plantTranscript(t *testing.T, configDir, workspace, id string) {
	t.Helper()
	dir := filepath.Join(configDir, "projects", session.ProjectSlug(workspace))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSetEnvironmentsRestartsOnNewAccountResumingSameSession(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home, tws, a, b := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	cfg := envTestConfig(home, tws, a, b)
	rec, sup := savedLayeredAgent(t, cfg, nil)
	// The live conversation is newer than the stored id and lives in a's dir.
	plantTranscript(t, a, rec.Workspace, "live-session")
	m := envManager(cfg, sup)

	res, err := m.SetEnvironments("leo-x", []string{"base", "acct-b"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "running" {
		t.Fatalf("status = %q", res.Status)
	}
	if !slices.Equal(sup.stopCalls, []string{"leo-x"}) {
		t.Fatalf("stop calls = %v", sup.stopCalls)
	}
	spawn := sup.spawnCall
	if spawn == nil {
		t.Fatal("no respawn")
	}
	if spawn.Env["CLAUDE_CONFIG_DIR"] != b || spawn.Env["SHARED"] != "b" {
		t.Fatalf("respawn env = %v, want account b", spawn.Env)
	}
	if spawn.Env["LITERAL"] != "tmpl" {
		t.Fatalf("literal template env lost: %v", spawn.Env)
	}
	i := slices.Index(spawn.ClaudeArgs, "--resume")
	if i < 0 || spawn.ClaudeArgs[i+1] != "live-session" {
		t.Fatalf("respawn args %v must resume the conversation found under the OLD account", spawn.ClaudeArgs)
	}
	if containsFlag(spawn.ClaudeArgs, "--session-id") {
		t.Fatalf("respawn must not mint a new session: %v", spawn.ClaudeArgs)
	}
	stored := loadRec(t, home, "leo-x")
	if !slices.Equal(stored.Environments, []string{"base", "acct-b"}) || stored.SessionID != "live-session" || stored.Env["CLAUDE_CONFIG_DIR"] != b {
		t.Fatalf("record = %+v", stored)
	}
}

func TestSetEnvironmentsDropsKeysOfTheDepartingEnvironment(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home, tws, a, b := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	cfg := envTestConfig(home, tws, a, b)
	cfg.Environments["acct-a"]["ONLY_A"] = "1"
	rec, sup := savedLayeredAgent(t, cfg, nil)
	if rec.Env["ONLY_A"] != "1" {
		t.Fatalf("fixture env = %v", rec.Env)
	}
	if _, err := envManager(cfg, sup).SetEnvironments("leo-x", []string{"acct-b"}); err != nil {
		t.Fatal(err)
	}
	if _, leaked := sup.spawnCall.Env["ONLY_A"]; leaked {
		t.Fatalf("account a's env survived the switch: %v", sup.spawnCall.Env)
	}
}

func TestSetEnvironmentsEmptyListFallsBackToTemplateDefault(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home, tws, a, b := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	cfg := envTestConfig(home, tws, a, b)
	_, sup := savedLayeredAgent(t, cfg, []string{"acct-b"})
	if _, err := envManager(cfg, sup).SetEnvironments("leo-x", nil); err != nil {
		t.Fatal(err)
	}
	if sup.spawnCall.Env["CLAUDE_CONFIG_DIR"] != a {
		t.Fatalf("env = %v, want template default account a", sup.spawnCall.Env)
	}
	if stored := loadRec(t, home, "leo-x"); stored.Environments != nil {
		t.Fatalf("override not cleared: %v", stored.Environments)
	}
}

func TestSetEnvironmentsUnknownNameLeavesAgentRunning(t *testing.T) {
	home, tws, a, b := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	cfg := envTestConfig(home, tws, a, b)
	_, sup := savedLayeredAgent(t, cfg, nil)
	_, err := envManager(cfg, sup).SetEnvironments("leo-x", []string{"nope"})
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("err = %v", err)
	}
	if len(sup.stopCalls) != 0 || sup.spawnCall != nil {
		t.Fatalf("agent was disturbed: stops %v spawn %v", sup.stopCalls, sup.spawnCall)
	}
}

func TestSetEnvironmentsDormantAgentIsRewrittenNotSpawned(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home, tws, a, b := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	cfg := envTestConfig(home, tws, a, b)
	rec, _ := savedLayeredAgent(t, cfg, nil)
	rec.Stopped = true
	_ = agentstore.Save(home, rec)
	sup := &capturingSupervisor{}
	res, err := envManager(cfg, sup).SetEnvironments("leo-x", []string{"acct-b"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "stopped" || sup.spawnCall != nil || len(sup.stopCalls) != 0 {
		t.Fatalf("status %q spawn %v stops %v", res.Status, sup.spawnCall, sup.stopCalls)
	}
	stored := loadRec(t, home, "leo-x")
	if !stored.Stopped || !slices.Equal(stored.Environments, []string{"acct-b"}) || stored.Env["CLAUDE_CONFIG_DIR"] != b {
		t.Fatalf("record = %+v", stored)
	}
}

func TestSetEnvironmentsRefusesAgentBackingPersistentTask(t *testing.T) {
	home, tws, a, b := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	cfg := envTestConfig(home, tws, a, b)
	cfg.Tasks = map[string]config.TaskConfig{"nightly": {Runtime: "persistent", Template: "coding"}}
	_, sup := savedLayeredAgent(t, cfg, nil)
	rec := loadRec(t, home, "leo-x")
	rec.Name = "coding"
	_ = agentstore.Save(home, rec)
	sup.agents["coding"] = ProcessState{Name: "coding", Status: "running"}
	_, err := envManager(cfg, sup).SetEnvironments("coding", []string{"acct-b"})
	if err == nil || !strings.Contains(err.Error(), "nightly") {
		t.Fatalf("err = %v, want a refusal naming the task", err)
	}
}

func TestRestartReResolvesNamedEnvironmentsFromCurrentConfig(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home, tws, a, b := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	cfg := envTestConfig(home, tws, a, b)
	_, sup := savedLayeredAgent(t, cfg, []string{"acct-b"})
	moved := t.TempDir()
	cfg.Environments["acct-b"] = map[string]string{"CLAUDE_CONFIG_DIR": moved}
	if err := envManager(cfg, sup).Restart("leo-x"); err != nil {
		t.Fatal(err)
	}
	if got := sup.spawnCall.Env["CLAUDE_CONFIG_DIR"]; got != moved {
		t.Fatalf("restart env CLAUDE_CONFIG_DIR = %q, want the edited %q", got, moved)
	}
}

func TestRestartWithRemovedEnvironmentFailsAndLeavesAgentRunning(t *testing.T) {
	home, tws, a, b := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	cfg := envTestConfig(home, tws, a, b)
	_, sup := savedLayeredAgent(t, cfg, []string{"acct-b"})
	delete(cfg.Environments, "acct-b")
	err := envManager(cfg, sup).Restart("leo-x")
	var unknown *config.UnknownEnvironmentError
	if !errors.As(err, &unknown) || unknown.Name != "acct-b" {
		t.Fatalf("err = %v, want UnknownEnvironmentError{acct-b}", err)
	}
	if len(sup.stopCalls) != 0 || sup.spawnCall != nil {
		t.Fatalf("a failed re-resolve must not bounce the agent: stops %v spawn %v", sup.stopCalls, sup.spawnCall)
	}
}

func TestStartWithRemovedEnvironmentFailsAndStaysDormant(t *testing.T) {
	home, tws, a, b := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	cfg := envTestConfig(home, tws, a, b)
	rec, _ := savedLayeredAgent(t, cfg, []string{"acct-b"})
	rec.Stopped = true
	_ = agentstore.Save(home, rec)
	delete(cfg.Environments, "acct-b")
	sup := &capturingSupervisor{}
	err := envManager(cfg, sup).Start("leo-x")
	if err == nil || !strings.Contains(err.Error(), "acct-b") {
		t.Fatalf("err = %v", err)
	}
	if sup.spawnCall != nil || !loadRec(t, home, "leo-x").Stopped {
		t.Fatal("agent must stay dormant")
	}
}

func TestLegacyRecordKeepsStoredEnvKeysOnRestart(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home, tws, a, b := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	cfg := envTestConfig(home, tws, a, b)
	cfg.Templates["coding"] = config.TemplateConfig{Workspace: tws, Model: "sonnet"}
	rec := agentstore.Record{
		Name: "leo-old", Template: "coding", Harness: "claude", Workspace: t.TempDir(),
		ClaudeArgs: []string{"--model", "sonnet", "--session-id", "s"},
		Env:        map[string]string{"HAND_SET": "1"},
	}
	_ = agentstore.Save(home, rec)
	sup := &capturingSupervisor{agents: map[string]ProcessState{"leo-old": {Name: "leo-old", Status: "running"}}}
	if err := envManager(cfg, sup).Restart("leo-old"); err != nil {
		t.Fatal(err)
	}
	if sup.spawnCall.Env["HAND_SET"] != "1" {
		t.Fatalf("legacy env key dropped: %v", sup.spawnCall.Env)
	}
}

func TestStaleAgentsReportsMissingEnvironment(t *testing.T) {
	home, tws, a, b := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	cfg := envTestConfig(home, tws, a, b)
	_, sup := savedLayeredAgent(t, cfg, []string{"acct-b"})
	delete(cfg.Environments, "acct-b")
	got := envManager(cfg, sup).StaleAgents()
	if len(got) != 1 || got[0].Name != "leo-x" || !strings.Contains(got[0].EnvironmentError, "acct-b") {
		t.Fatalf("stale = %+v", got)
	}
}

func TestInheritedFromSource(t *testing.T) {
	layered := agentstore.Record{Name: "leo-src", Environments: []string{"acct-b"}, EnvLayered: true,
		Env: map[string]string{"CLAUDE_CONFIG_DIR": "/b", "X": "1", "Y": "2"}, SpawnEnv: map[string]string{"X": "1"}, InheritedEnv: map[string]string{"Y": "2"}}
	if got := inheritedFromSource(layered); !reflect.DeepEqual(got, map[string]string{"X": "1", "Y": "2"}) {
		t.Fatalf("layered source inherits only its explicit layers, got %v", got)
	}
	legacy := agentstore.Record{Name: "leo-old", Env: map[string]string{"K": "v"}}
	if got := inheritedFromSource(legacy); !reflect.DeepEqual(got, map[string]string{"K": "v"}) {
		t.Fatalf("legacy source inherits its stored env, got %v", got)
	}
}

// An implicit persistent-task agent's template is synthesized from the task, so
// it is not in cfg.Templates; restart must still re-resolve its environments.
func TestRestartReResolvesEnvironmentsOfImplicitPersistentTaskAgent(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home, ws, a, b := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	cfg := envTestConfig(home, ws, a, b)
	cfg.Tasks = map[string]config.TaskConfig{"nightly": {
		Runtime: "persistent", Workspace: ws, Model: "sonnet", Environments: []string{"acct-b"},
	}}
	_, tmpl, implicit, err := cfg.ResolveTaskTarget("nightly")
	if err != nil || !implicit {
		t.Fatalf("setup: implicit=%v err=%v", implicit, err)
	}
	env, err := cfg.ResolveEnv(tmpl.Environments, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := agentstore.Record{
		Name: "nightly", Template: "nightly", Harness: "claude", Workspace: ws, SessionID: "s1",
		ClaudeArgs: []string{"--model", "sonnet", "--session-id", "s1"}, Env: env, EnvLayered: true,
	}
	if err := agentstore.Save(home, rec); err != nil {
		t.Fatal(err)
	}
	sup := &capturingSupervisor{agents: map[string]ProcessState{"nightly": {Name: "nightly", Status: "running"}}}

	moved := t.TempDir()
	cfg.Environments["acct-b"] = map[string]string{"CLAUDE_CONFIG_DIR": moved}
	if err := envManager(cfg, sup).Restart("nightly"); err != nil {
		t.Fatal(err)
	}
	if got := sup.spawnCall.Env["CLAUDE_CONFIG_DIR"]; got != moved {
		t.Fatalf("restart env CLAUDE_CONFIG_DIR = %q, want the edited %q", got, moved)
	}

	delete(cfg.Environments, "acct-b")
	sup.stopCalls, sup.spawnCall = nil, nil
	err = envManager(cfg, sup).Restart("nightly")
	var unknown *config.UnknownEnvironmentError
	if !errors.As(err, &unknown) || len(sup.stopCalls) != 0 {
		t.Fatalf("a removed environment must fail the restart before stopping: err=%v stops=%v", err, sup.stopCalls)
	}
}

func TestSetEnvironmentsRejectsHarnessMismatchBeforeStopping(t *testing.T) {
	home, tws, a, b := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	cfg := envTestConfig(home, tws, a, b)
	_, sup := savedLayeredAgent(t, cfg, nil) // the record was spawned on claude
	tmpl := cfg.Templates["coding"]
	tmpl.Harness = "codex"
	cfg.Templates["coding"] = tmpl

	_, err := envManager(cfg, sup).SetEnvironments("leo-x", []string{"acct-b"})
	if err == nil || !strings.Contains(err.Error(), "harness") || !strings.Contains(err.Error(), "set-template") {
		t.Fatalf("err = %v, want a harness mismatch pointing at set-template/restart", err)
	}
	if len(sup.stopCalls) != 0 || sup.spawnCall != nil {
		t.Fatalf("a rejected switch must not bounce the agent: stops %v spawn %v", sup.stopCalls, sup.spawnCall)
	}
}

// A save that fails after the agent was stopped must not strand it: the old
// launch is restored and the stored record stays start-able.
func TestSetEnvironmentsSaveFailureRelaunchesOnTheOldEnvironment(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home, tws, a, b := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	cfg := envTestConfig(home, tws, a, b)
	rec, sup := savedLayeredAgent(t, cfg, nil)
	plantTranscript(t, a, rec.Workspace, "live-session")
	store := agentstore.FilePath(home)
	sup.onStop = func(string) {
		if err := os.Chmod(store, 0o400); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = os.Chmod(store, 0o600) })

	_, err := envManager(cfg, sup).SetEnvironments("leo-x", []string{"acct-b"})
	if err == nil || !strings.Contains(err.Error(), "saving") {
		t.Fatalf("err = %v, want the save failure", err)
	}
	spawn := sup.spawnCall
	if spawn == nil {
		t.Fatal("agent was left down after the failed save")
	}
	if spawn.Env["CLAUDE_CONFIG_DIR"] != a {
		t.Fatalf("relaunch env = %v, want the OLD account %q", spawn.Env, a)
	}
	if i := slices.Index(spawn.ClaudeArgs, "--resume"); i < 0 || spawn.ClaudeArgs[i+1] != "live-session" || containsFlag(spawn.ClaudeArgs, "--session-id") {
		t.Fatalf("relaunch args %v must resume the live conversation", spawn.ClaudeArgs)
	}
	if got := loadRec(t, home, "leo-x"); got.Stopped || !slices.Equal(got.Environments, rec.Environments) {
		t.Fatalf("stored record changed: %+v", got)
	}
}

func TestResolveAgentEnvironmentsReportsEffectiveNamesAndSource(t *testing.T) {
	cfg := envTestConfig(t.TempDir(), t.TempDir(), "/a", "/b")
	cfg.Defaults.Environments = []string{"base"}

	tests := []struct {
		name     string
		template string
		override []string
		want     []string
		source   string
	}{
		{"override wins", "coding", []string{"acct-b"}, []string{"acct-b"}, "override"},
		{"template default", "coding", nil, []string{"base", "acct-a"}, "default"},
		{"unknown template falls to defaults", "gone", nil, []string{"base"}, "default"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, source, err := ResolveAgentEnvironments(cfg, "leo-x", tt.template, tt.override)
			if err != nil || !slices.Equal(got, tt.want) || source != tt.source {
				t.Fatalf("got %v %q %v, want %v %q", got, source, err, tt.want, tt.source)
			}
		})
	}
}

func TestResolveAgentEnvironmentsReportsAMissingEnvironment(t *testing.T) {
	cfg := envTestConfig(t.TempDir(), t.TempDir(), "/a", "/b")
	got, source, err := ResolveAgentEnvironments(cfg, "leo-x", "coding", []string{"acct-b", "deleted"})
	var unknown *config.UnknownEnvironmentError
	if !errors.As(err, &unknown) || unknown.Name != "deleted" {
		t.Fatalf("err = %v", err)
	}
	if !slices.Equal(got, []string{"acct-b", "deleted"}) || source != "override" {
		t.Fatalf("names must still be reported alongside the error: %v %q", got, source)
	}
}

func TestSetEnvironmentsErrorsAreTyped(t *testing.T) {
	home, tws, a, b := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	cfg := envTestConfig(home, tws, a, b)
	_, sup := savedLayeredAgent(t, cfg, nil)
	m := envManager(cfg, sup)

	var unknown *config.UnknownEnvironmentError
	if _, err := m.SetEnvironments("leo-x", []string{"nope"}); !errors.As(err, &unknown) {
		t.Fatalf("unknown name: err = %v", err)
	}

	tmpl := cfg.Templates["coding"]
	tmpl.Harness = "codex"
	cfg.Templates["coding"] = tmpl
	var mismatch *HarnessMismatchError
	if _, err := m.SetEnvironments("leo-x", []string{"acct-b"}); !errors.As(err, &mismatch) {
		t.Fatalf("harness change: err = %v", err)
	}
	tmpl.Harness = ""
	cfg.Templates["coding"] = tmpl

	cfg.Tasks = map[string]config.TaskConfig{"nightly": {Runtime: "persistent", Template: "coding"}}
	rec := loadRec(t, home, "leo-x")
	rec.Name = "coding"
	_ = agentstore.Save(home, rec)
	sup.agents["coding"] = ProcessState{Name: "coding", Status: "running"}
	var bound *PersistentTaskError
	if _, err := m.SetEnvironments("coding", []string{"acct-b"}); !errors.As(err, &bound) || bound.Task != "nightly" {
		t.Fatalf("persistent task: err = %v", err)
	}
}

// The restart set-environment performs must reach /api/v1/events subscribers
// as a normal agent_state_changed carrying the new environments.
func TestSetEnvironmentsPublishesStateChangeWithNewEnvironments(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home, tws, a, b := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	cfg := envTestConfig(home, tws, a, b)
	_, sup := savedLayeredAgent(t, cfg, nil)
	pub := &recordingObservePublisher{}
	m := envManager(cfg, sup)
	m.SetPublisher(pub)

	if _, err := m.SetEnvironments("leo-x", []string{"base", "acct-b"}); err != nil {
		t.Fatal(err)
	}
	wire := lastEnvironmentsEvent(t, pub)
	if !strings.Contains(wire, `"environments":["base","acct-b"]`) || !strings.Contains(wire, `"environments_source":"override"`) || !strings.Contains(wire, `"environment_error":null`) {
		t.Fatalf("wire form = %s", wire)
	}

	// Clearing the override reports the default list, source "default".
	pub.events = nil
	if _, err := m.SetEnvironments("leo-x", nil); err != nil {
		t.Fatal(err)
	}
	wire = lastEnvironmentsEvent(t, pub)
	if !strings.Contains(wire, `"environments":["base","acct-a"]`) || !strings.Contains(wire, `"environments_source":"default"`) {
		t.Fatalf("cleared wire form = %s", wire)
	}
}

// Every environments update carries all three fields on the wire — [] when the
// effective list is empty, an explicit null error when resolved — so a client
// never has to guess whether an absent field means "unchanged" or "cleared".
func TestEnvironmentsEventAlwaysCarriesAllFields(t *testing.T) {
	cfg := &config.Config{Templates: map[string]config.TemplateConfig{"bare": {}}}
	pub := &recordingObservePublisher{}
	m := envManager(cfg, &capturingSupervisor{})
	m.SetPublisher(pub)

	m.publishEnvironmentsChanged(cfg, agentstore.Record{Name: "leo-x", Template: "bare"}, "starting")
	wire := lastEnvironmentsEvent(t, pub)
	for _, want := range []string{`"environments":[]`, `"environments_source":"default"`, `"environment_error":null`} {
		if !strings.Contains(wire, want) {
			t.Errorf("cleared event %s missing %s", wire, want)
		}
	}

	pub.events = nil
	m.publishEnvironmentsChanged(cfg, agentstore.Record{Name: "leo-x", Template: "bare", Environments: []string{"gone"}}, "starting")
	wire = lastEnvironmentsEvent(t, pub)
	if !strings.Contains(wire, `"environment_error":"`) || !strings.Contains(wire, "gone") {
		t.Errorf("unresolvable event %s must carry the error string", wire)
	}
}

func lastEnvironmentsEvent(t *testing.T, pub *recordingObservePublisher) string {
	t.Helper()
	for i := len(pub.events) - 1; i >= 0; i-- {
		ev := pub.events[i]
		if p, ok := ev.Payload.(*observe.AgentEnvironmentsChangedPayload); ok && ev.Type == observe.EventAgentStateChanged {
			raw, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			return string(raw)
		}
	}
	t.Fatalf("no environments agent_state_changed published: %+v", pub.events)
	return ""
}

// The loader's error can quote a rejected config's contents; Manager errors
// reach API clients, so they carry a generic message and the detail is logged.
func TestManagerConfigLoadErrorsAreSanitized(t *testing.T) {
	const secret = "sk-live-do-not-leak"
	loader := func() (*config.Config, error) { return nil, errors.New("yaml: cannot unmarshal " + secret) }
	m := New(loader, &capturingSupervisor{}, "", "tok")

	_, spawnErr := m.Spawn(context.Background(), SpawnSpec{Template: "coding"})
	_, setErr := m.SetEnvironments("leo-x", []string{"work"})
	for _, err := range []error{spawnErr, setErr} {
		if err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "config unavailable") {
			t.Fatalf("err = %v, want a sanitized config-unavailable error", err)
		}
	}
}

func spawnForValidation(t *testing.T, spec SpawnSpec) (*capturingSupervisor, string, error) {
	t.Helper()
	home, ws, a, b := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	cfg := envTestConfig(home, ws, a, b)
	sup := &capturingSupervisor{}
	_, err := envManager(cfg, sup).Spawn(context.Background(), spec)
	return sup, ws, err
}

// Every entry point (web, /api/v1, daemon IPC, MCP) spawns through
// Manager.Spawn, so a bad name must be refused there, before a reservation or
// any filesystem work.
func TestSpawnRejectsUnsafeNamesBeforeAnySideEffect(t *testing.T) {
	for _, name := range []string{"invalid:name", "../outside", "a/b", "has space", "dot.name", "-", "LEO-"} {
		t.Run(name, func(t *testing.T) {
			sup, ws, err := spawnForValidation(t, SpawnSpec{Template: "coding", Name: name})
			if !errors.Is(err, ErrInvalidAgentName) {
				t.Fatalf("err = %v, want ErrInvalidAgentName", err)
			}
			entries, _ := os.ReadDir(ws)
			if len(sup.reservations) != 0 || sup.spawnCall != nil || len(entries) != 0 {
				t.Fatalf("side effects before validation: reservations=%v spawn=%v dirs=%d", sup.reservations, sup.spawnCall, len(entries))
			}
		})
	}
	if _, _, err := spawnForValidation(t, SpawnSpec{Template: "coding", Name: "scratch-1"}); err != nil {
		t.Fatalf("a valid name was rejected: %v", err)
	}
}

func TestSpawnRejectsPathTraversalInRepo(t *testing.T) {
	for _, repo := range []string{"..", "../outside", "owner/..", "./x", "."} {
		if err := ValidateRepo(repo); err == nil {
			t.Errorf("ValidateRepo(%q) accepted a traversal segment", repo)
		}
	}
	if err := ValidateRepo("owner/repo.git"); err != nil {
		t.Errorf("ValidateRepo rejected a normal repo: %v", err)
	}
}

func TestSpawnValidationErrorsAreTyped(t *testing.T) {
	var unknownTmpl *UnknownTemplateError
	for _, tmpl := range []string{"", "nope"} {
		_, _, err := spawnForValidation(t, SpawnSpec{Template: tmpl})
		if !errors.As(err, &unknownTmpl) || unknownTmpl.Name != tmpl {
			t.Fatalf("template %q: err = %v, want *UnknownTemplateError", tmpl, err)
		}
	}

	sup, _, err := spawnForValidation(t, SpawnSpec{Template: "coding", Environments: []string{"acct-a", "acct-a"}})
	var invalid *config.InvalidEnvironmentsError
	if !errors.As(err, &invalid) || sup.spawnCall != nil || len(sup.reservations) != 0 {
		t.Fatalf("duplicate names: err = %v spawn=%v", err, sup.spawnCall)
	}

	// set-environment shares the validation.
	home, tws, a, b := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	cfg := envTestConfig(home, tws, a, b)
	_, sup2 := savedLayeredAgent(t, cfg, nil)
	if _, err := envManager(cfg, sup2).SetEnvironments("leo-x", []string{"acct-b", "acct-b"}); !errors.As(err, &invalid) {
		t.Fatalf("set duplicate: err = %v", err)
	}
	if len(sup2.stopCalls) != 0 {
		t.Fatal("a rejected set must not stop the agent")
	}
}
