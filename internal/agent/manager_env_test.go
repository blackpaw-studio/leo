package agent

import (
	"context"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

// newEnvTestManager builds a Manager+capturingSupervisor pair for a single
// template, following the same shape as newWorktreeTestManager but for the
// non-worktree spawnShared path (plain repo name, no git needed). Web is
// enabled with a live token so the opencode LeoMCP bridge (and therefore
// OPENCODE_CONFIG_CONTENT) is wired in, matching a real daemon's supervised
// spawn.
func newEnvTestManager(t *testing.T, tmplName string, tmpl config.TemplateConfig) (*Manager, *capturingSupervisor, string) {
	t.Helper()
	home := t.TempDir()
	cfg := &config.Config{
		HomePath:  home,
		Web:       config.WebConfig{Enabled: true},
		Templates: map[string]config.TemplateConfig{tmplName: tmpl},
	}
	sup := &capturingSupervisor{}
	loader := func() (*config.Config, error) { return cfg, nil }
	return New(loader, sup, "", "test-token"), sup, home
}

// TestSpawnSharedOpencodeEnvOverlay verifies Bug A's fix: an opencode
// template's spawn env carries the harness's Env() overlay
// (OPENCODE_CONFIG_CONTENT), not just the template's own configured env.
// Previously SpawnAgent's SpawnRequest.Env only ever contained
// mergeEnv(tmpl.Env, spec.Env), silently dropping the harness overlay
// entirely.
func TestSpawnSharedOpencodeEnvOverlay(t *testing.T) {
	mgr, sup, _ := newEnvTestManager(t, "coding", config.TemplateConfig{
		Workspace: t.TempDir(),
		Harness:   "opencode",
		Env:       map[string]string{"MY_VAR": "1"},
	})

	rec, err := mgr.Spawn(context.Background(), SpawnSpec{
		Template: "coding",
		Repo:     "myagent",
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	if sup.spawnCall == nil {
		t.Fatal("SpawnAgent not called")
	}
	env := sup.spawnCall.Env
	content, ok := env["OPENCODE_CONFIG_CONTENT"]
	if !ok || content == "" {
		t.Fatalf("expected non-empty OPENCODE_CONFIG_CONTENT in spawn env, got %v", env)
	}
	if env["MY_VAR"] != "1" {
		t.Fatalf("expected template env MY_VAR to survive the merge, got %v", env)
	}

	// rec.Env (the Manager's own return value) must also carry the overlay —
	// it's what List()/Resolve() surface back to callers.
	if rec.Env["OPENCODE_CONFIG_CONTENT"] == "" {
		t.Errorf("Record.Env missing OPENCODE_CONFIG_CONTENT: %v", rec.Env)
	}
}

// TestSpawnSharedOpencodeTemplateEnvWinsOnCollision verifies the merge order
// mandated by the harness Env() doc comment: harness env is the BASE layer,
// so a template env key colliding with a harness-provided key wins.
func TestSpawnSharedOpencodeTemplateEnvWinsOnCollision(t *testing.T) {
	mgr, sup, _ := newEnvTestManager(t, "coding", config.TemplateConfig{
		Workspace: t.TempDir(),
		Harness:   "opencode",
		Env:       map[string]string{"OPENCODE_CONFIG_CONTENT": "template-override"},
	})

	if _, err := mgr.Spawn(context.Background(), SpawnSpec{Template: "coding", Repo: "myagent"}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if sup.spawnCall == nil {
		t.Fatal("SpawnAgent not called")
	}
	if got := sup.spawnCall.Env["OPENCODE_CONFIG_CONTENT"]; got != "template-override" {
		t.Errorf("OPENCODE_CONFIG_CONTENT = %q, want template's own override to win", got)
	}
}

// TestSpawnSharedClaudeNoOpencodeEnvKeys is the byte-identity guard: a claude
// template's spawn env must contain no OPENCODE_* keys — Claude.Env() returns
// nil, so the merge must be a no-op for claude specs.
func TestSpawnSharedClaudeNoOpencodeEnvKeys(t *testing.T) {
	mgr, sup, _ := newEnvTestManager(t, "coding", config.TemplateConfig{
		Workspace: t.TempDir(),
		Env:       map[string]string{"MY_VAR": "1"},
	})

	if _, err := mgr.Spawn(context.Background(), SpawnSpec{Template: "coding", Repo: "myagent"}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if sup.spawnCall == nil {
		t.Fatal("SpawnAgent not called")
	}
	for k := range sup.spawnCall.Env {
		if len(k) >= 9 && k[:9] == "OPENCODE_" {
			t.Errorf("unexpected OPENCODE_* key %q in claude spawn env: %v", k, sup.spawnCall.Env)
		}
	}
	if sup.spawnCall.Env["MY_VAR"] != "1" {
		t.Errorf("expected template env to survive unchanged, got %v", sup.spawnCall.Env)
	}
}
