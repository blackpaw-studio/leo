package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/leomcp"
)

func expanderOf(vars map[string]string) config.Expander {
	return config.Expander{Lookup: func(k string) (string, bool) { v, ok := vars[k]; return v, ok }}
}

func TestSpawnExpandsNamedAndLiteralEnvValues(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home, ws := t.TempDir(), t.TempDir()
	cfg := envTestConfig(home, ws, "${ROOT}/.claude-a", "/b")
	cfg.Templates["coding"] = config.TemplateConfig{Workspace: ws, Environments: []string{"acct-a"}, Env: config.LiteralEnv(map[string]string{"LITERAL": "$ROOT/lit", "ESC": "$$ROOT"})}
	sup := &capturingSupervisor{}
	m := envManager(cfg, sup)
	m.env = expanderOf(map[string]string{"ROOT": "/r"})

	if _, err := m.Spawn(context.Background(), SpawnSpec{Template: "coding", Repo: "x", Env: map[string]string{"SPAWN": "$ROOT"}}); err != nil {
		t.Fatal(err)
	}
	got := sup.spawnCall.Env
	for k, want := range map[string]string{"CLAUDE_CONFIG_DIR": "/r/.claude-a", "LITERAL": "/r/lit", "ESC": "$ROOT", "SPAWN": "$ROOT"} {
		if got[k] != want {
			t.Errorf("env[%s] = %q, want %q", k, got[k], want)
		}
	}
	if cfg.Environments["acct-a"]["CLAUDE_CONFIG_DIR"] != config.Literal("${ROOT}/.claude-a") {
		t.Fatal("config was mutated")
	}
}

func TestSpawnUnsetVariableFailsBeforeAnySideEffect(t *testing.T) {
	home, ws := t.TempDir(), t.TempDir()
	cfg := envTestConfig(home, ws, "$NOPE/.claude-a", "/b")
	sup := &capturingSupervisor{}
	m := envManager(cfg, sup)
	m.env = expanderOf(nil)

	_, err := m.Spawn(context.Background(), SpawnSpec{Template: "coding", Repo: "x"})
	var unset *config.UnsetVariableError
	if !errors.As(err, &unset) || unset.Var != "NOPE" || unset.Scope != "environments.acct-a" || unset.Key != "CLAUDE_CONFIG_DIR" {
		t.Fatalf("err = %v, want UnsetVariableError for environments.acct-a CLAUDE_CONFIG_DIR $NOPE", err)
	}
	if sup.spawnCall != nil || len(sup.reservations) != 0 {
		t.Fatalf("side effects: spawn %v reservations %v", sup.spawnCall, sup.reservations)
	}
	if recs, _ := agentstore.Load(agentstore.FilePath(home)); len(recs) != 0 {
		t.Fatalf("records written: %v", recs)
	}
}

func TestResolveRestartArgsExpandsAndReportsUnset(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	cfg := envTestConfig(t.TempDir(), t.TempDir(), "${ROOT}/.claude-a", "/b")
	rec := agentstore.Record{Name: "leo-x", Template: "coding", Harness: "claude", Workspace: t.TempDir(), EnvLayered: true,
		ClaudeArgs: []string{"--model", "sonnet"}}

	_, env, err := resolveRestartArgs(cfg, rec, "tok", leomcp.Server{}, expanderOf(map[string]string{"ROOT": "/r"}))
	if err != nil || env["CLAUDE_CONFIG_DIR"] != "/r/.claude-a" {
		t.Fatalf("env = %v, err = %v", env, err)
	}
	_, _, err = resolveRestartArgs(cfg, rec, "tok", leomcp.Server{}, expanderOf(nil))
	if err == nil || !strings.Contains(err.Error(), "$ROOT") {
		t.Fatalf("err = %v, want unset $ROOT (restart must not run on a wrong dir)", err)
	}
}

func TestLegacyRecordRestartUsesExpandedTemplateEnv(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home, tws := t.TempDir(), t.TempDir()
	cfg := envTestConfig(home, tws, "/a", "/b")
	cfg.Templates["coding"] = config.TemplateConfig{Workspace: tws, Model: "sonnet", Env: config.LiteralEnv(map[string]string{"TOOLS": "~/tools", "REF": "$ROOT/x"})}
	rec := agentstore.Record{
		Name: "leo-old", Template: "coding", Harness: "claude", Workspace: t.TempDir(),
		ClaudeArgs: []string{"--model", "sonnet", "--session-id", "s"},
		// Stored by an older leo: raw template values plus a hand-set key.
		Env: map[string]string{"TOOLS": "~/tools", "REF": "$ROOT/x", "HAND_SET": "1"},
	}
	_ = agentstore.Save(home, rec)
	sup := &capturingSupervisor{agents: map[string]ProcessState{"leo-old": {Name: "leo-old", Status: "running"}}}
	m := envManager(cfg, sup)
	m.env = config.Expander{
		Lookup: func(k string) (string, bool) { return "/r", k == "ROOT" },
		Home:   func() (string, error) { return "/home/evan", nil },
	}
	if err := m.Restart("leo-old"); err != nil {
		t.Fatal(err)
	}
	got := sup.spawnCall.Env
	if got["TOOLS"] != "/home/evan/tools" || got["REF"] != "/r/x" || got["HAND_SET"] != "1" {
		t.Fatalf("legacy restart env = %v", got)
	}
}
