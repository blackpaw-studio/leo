package config

import (
	"strings"
	"testing"
)

func TestResolveSessionDedicated(t *testing.T) {
	cfg := &Config{
		Tasks: map[string]TaskConfig{
			"t1": {
				Runtime:   "persistent",
				Workspace: "/tmp/t1",
				Model:     "sonnet",
				Channels:  []string{"plugin:slack@official"},
			},
		},
	}
	name, topo, sess, err := cfg.ResolveSession("t1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if topo != TopologyDedicated {
		t.Fatalf("expected TopologyDedicated, got %v", topo)
	}
	if name != "t1" {
		t.Fatalf("expected implicit name 't1', got %q", name)
	}
	if sess.Workspace != "/tmp/t1" || sess.Model != "sonnet" {
		t.Fatalf("inheritance wrong: %+v", sess)
	}
	if len(sess.Channels) != 1 || sess.Channels[0] != "plugin:slack@official" {
		t.Fatalf("channels inheritance wrong: %+v", sess.Channels)
	}
}

func TestResolveSessionShared(t *testing.T) {
	cfg := &Config{
		Sessions: map[string]SessionConfig{
			"daily": {Workspace: "/tmp/d", Channels: []string{"plugin:slack@official"}},
		},
		Tasks: map[string]TaskConfig{
			"t1": {Runtime: "persistent", Session: "daily", Channels: []string{"plugin:slack@official"}},
		},
	}
	name, topo, sess, err := cfg.ResolveSession("t1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if topo != TopologyShared {
		t.Fatalf("expected TopologyShared, got %v", topo)
	}
	if name != "daily" || sess.Workspace != "/tmp/d" {
		t.Fatalf("shared resolution wrong: name=%q sess=%+v", name, sess)
	}
}

func TestResolveSessionMissing(t *testing.T) {
	cfg := &Config{
		Tasks: map[string]TaskConfig{
			"t1": {Runtime: "persistent", Session: "nope"},
		},
	}
	if _, _, _, err := cfg.ResolveSession("t1"); err == nil {
		t.Fatalf("expected error for missing session reference")
	}
}

func TestValidatePersistentChannelsSubset(t *testing.T) {
	cfg := &Config{
		Sessions: map[string]SessionConfig{
			"daily": {Workspace: "/tmp/d", Channels: []string{"plugin:slack@official"}},
		},
		Tasks: map[string]TaskConfig{
			"bad": {
				Runtime: "persistent", Session: "daily",
				Schedule: "0 7 * * *", PromptFile: "p.md",
				Channels: []string{"plugin:telegram@official"},
			},
		},
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "subset") {
		t.Fatalf("expected subset error, got %v", err)
	}
}

func TestValidatePersistentDedicatedNameConflict(t *testing.T) {
	cfg := &Config{
		Sessions: map[string]SessionConfig{
			"t1": {Workspace: "/tmp/x", Channels: []string{"plugin:slack@official"}},
		},
		Tasks: map[string]TaskConfig{
			"t1": {
				Runtime: "persistent", Schedule: "0 * * * *", PromptFile: "p.md",
				Workspace: "/tmp/x", Channels: []string{"plugin:slack@official"},
			},
		},
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "implicit session") {
		t.Fatalf("expected implicit-name conflict error, got %v", err)
	}
}

func TestResolveSessionProcess(t *testing.T) {
	cfg := &Config{
		Processes: map[string]ProcessConfig{
			"bot": {
				Workspace: "/tmp/bot",
				Model:     "sonnet",
				Channels:  []string{"plugin:slack@official"},
				Agent:     "orchestrator",
			},
		},
		Tasks: map[string]TaskConfig{
			"poke": {
				Runtime:  "persistent",
				Session:  "process:bot",
				Channels: []string{"plugin:slack@official"},
			},
		},
	}
	name, topo, sess, err := cfg.ResolveSession("poke")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if topo != TopologyProcess {
		t.Fatalf("expected TopologyProcess, got %v", topo)
	}
	if name != "bot" {
		t.Fatalf("expected name 'bot', got %q", name)
	}
	if sess.Workspace != "/tmp/bot" {
		t.Fatalf("workspace not threaded from process: %+v", sess)
	}
	if sess.Agent != "orchestrator" {
		t.Fatalf("agent not threaded from process: %+v", sess)
	}
	if len(sess.Channels) != 1 || sess.Channels[0] != "plugin:slack@official" {
		t.Fatalf("channels not threaded from process: %+v", sess.Channels)
	}
}

func TestResolveSessionProcessMissing(t *testing.T) {
	cfg := &Config{
		Tasks: map[string]TaskConfig{
			"poke": {Runtime: "persistent", Session: "process:nope"},
		},
	}
	if _, _, _, err := cfg.ResolveSession("poke"); err == nil {
		t.Fatalf("expected error for missing process reference")
	}
}
