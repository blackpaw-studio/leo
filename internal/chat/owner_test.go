package chat

import (
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

func ownerCfg(tasks map[string]config.TaskConfig) *config.Config {
	return &config.Config{HomePath: "/home/leo", Tasks: tasks}
}

func TestResolveOwnerExplicitOwnerThatExists(t *testing.T) {
	cfg := ownerCfg(map[string]config.TaskConfig{"daily": {Owner: "web"}})
	got := ResolveOwner(cfg, "daily", []AgentRef{{Name: "web"}, {Name: "assistant"}})
	if got.Agent != "web" || got.Missing != "" {
		t.Fatalf("got %+v", got)
	}
}

func TestResolveOwnerMissingExplicitOwnerFallsBackWithNote(t *testing.T) {
	cfg := ownerCfg(map[string]config.TaskConfig{"daily": {Owner: "gone"}})
	got := ResolveOwner(cfg, "daily", []AgentRef{{Name: "assistant"}})
	if got.Agent != DefaultAgent || got.Missing != "gone" {
		t.Fatalf("got %+v", got)
	}
}

func TestResolveOwnerPersistentTaskDefaultsToItsAgent(t *testing.T) {
	cfg := ownerCfg(map[string]config.TaskConfig{
		"implicit": {Runtime: "persistent"},
		"tmpl":     {Runtime: "persistent", Template: "coder"},
	})
	cfg.Templates = map[string]config.TemplateConfig{"coder": {}}
	agents := []AgentRef{{Name: "implicit"}, {Name: "coder"}, {Name: "assistant"}}
	if got := ResolveOwner(cfg, "implicit", agents); got.Agent != "implicit" {
		t.Fatalf("implicit persistent owner = %+v", got)
	}
	if got := ResolveOwner(cfg, "tmpl", agents); got.Agent != "coder" {
		t.Fatalf("template persistent owner = %+v", got)
	}
}

func TestResolveOwnerOneshotMatchesAgentWorkspace(t *testing.T) {
	cfg := ownerCfg(map[string]config.TaskConfig{
		"build":  {Workspace: "/src/web/"},
		"orphan": {Workspace: "/src/none"},
	})
	agents := []AgentRef{{Name: "assistant", Workspace: "/home/leo/workspace"}, {Name: "web", Workspace: "/src/web"}}
	if got := ResolveOwner(cfg, "build", agents); got.Agent != "web" {
		t.Fatalf("workspace owner = %+v", got)
	}
	if got := ResolveOwner(cfg, "orphan", agents); got.Agent != DefaultAgent || got.Missing != "" {
		t.Fatalf("no workspace match defaults silently to assistant: %+v", got)
	}
}

func TestResolveOwnerUnknownTaskGoesToAssistant(t *testing.T) {
	got := ResolveOwner(ownerCfg(nil), "ghost", nil)
	if got.Agent != DefaultAgent {
		t.Fatalf("got %+v", got)
	}
}

func TestResolveOwnerSharedWorkspacePrefersAssistantThenName(t *testing.T) {
	cfg := ownerCfg(map[string]config.TaskConfig{"t": {Workspace: "/ws"}})
	if got := ResolveOwner(cfg, "t", []AgentRef{{Name: "zed", Workspace: "/ws"}, {Name: "assistant", Workspace: "/ws"}}); got.Agent != "assistant" {
		t.Fatalf("got %+v", got)
	}
	if got := ResolveOwner(cfg, "t", []AgentRef{{Name: "zed", Workspace: "/ws"}, {Name: "bob", Workspace: "/ws"}}); got.Agent != "bob" {
		t.Fatalf("got %+v", got)
	}
}
