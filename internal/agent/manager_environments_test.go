package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
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
