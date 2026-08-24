package agent

import (
	"errors"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
)

// fakeSupervisor records RenameAgent calls and reports liveness via ephemeral.
type fakeSupervisor struct {
	ephemeral  map[string]ProcessState
	renamedOld string
	renamedNew string
	renameErr  error
}

func (f *fakeSupervisor) ReserveAgent(string) error     { return nil }
func (f *fakeSupervisor) ReleaseAgent(string)           {}
func (f *fakeSupervisor) SpawnAgent(SpawnRequest) error { return nil }
func (f *fakeSupervisor) StopAgent(string, bool) error  { return nil }
func (f *fakeSupervisor) EphemeralAgents() map[string]ProcessState {
	return f.ephemeral
}
func (f *fakeSupervisor) RenameAgent(old, new string) error {
	f.renamedOld, f.renamedNew = old, new
	return f.renameErr
}

func newTestManager(t *testing.T, home string, sup Supervisor) *Manager {
	t.Helper()
	loader := func() (*config.Config, error) {
		return &config.Config{HomePath: home}, nil
	}
	return New(loader, sup, "", "")
}

func TestManagerRename_RunningAgent(t *testing.T) {
	home := t.TempDir()
	_ = agentstore.Save(home, agentstore.Record{
		Name:       "leo-old",
		ClaudeArgs: []string{"--name", "leo-old", "--model", "opus"},
	})
	sup := &fakeSupervisor{ephemeral: map[string]ProcessState{"leo-old": {Name: "leo-old", Status: "running"}}}
	m := newTestManager(t, home, sup)

	rec, err := m.Rename("leo-old", "renamed-agent")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if rec.Name != "leo-renamed-agent" {
		t.Fatalf("returned record name = %q", rec.Name)
	}
	if sup.renamedOld != "leo-old" || sup.renamedNew != "leo-renamed-agent" {
		t.Fatalf("supervisor not called: %q -> %q", sup.renamedOld, sup.renamedNew)
	}
	recs, _ := agentstore.Load(agentstore.FilePath(home))
	got, ok := recs["leo-renamed-agent"]
	if !ok {
		t.Fatal("store not re-keyed")
	}
	if strings.Join(got.ClaudeArgs, " ") != "--name leo-renamed-agent --model opus" {
		t.Fatalf("--name not rewritten: %v", got.ClaudeArgs)
	}
}

func TestManagerRename_StoppedAgentSkipsSupervisor(t *testing.T) {
	home := t.TempDir()
	_ = agentstore.Save(home, agentstore.Record{
		Name:       "leo-stopped",
		Branch:     "feature",
		Stopped:    true,
		ClaudeArgs: []string{"--name", "leo-stopped"},
	})
	sup := &fakeSupervisor{ephemeral: map[string]ProcessState{}} // not live
	m := newTestManager(t, home, sup)

	if _, err := m.Rename("leo-stopped", "leo-revived"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if sup.renamedNew != "" {
		t.Fatal("supervisor RenameAgent should not be called for a stopped agent")
	}
	recs, _ := agentstore.Load(agentstore.FilePath(home))
	if _, ok := recs["leo-revived"]; !ok {
		t.Fatal("store not re-keyed for stopped agent")
	}
}

func TestManagerRename_Errors(t *testing.T) {
	home := t.TempDir()
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-a", ClaudeArgs: []string{"--name", "leo-a"}})
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-b", ClaudeArgs: []string{"--name", "leo-b"}})
	sup := &fakeSupervisor{ephemeral: map[string]ProcessState{}}
	m := newTestManager(t, home, sup)

	if _, err := m.Rename("leo-a", "leo-b"); err == nil {
		t.Fatal("expected collision error")
	} else if !errors.Is(err, ErrAgentNameTaken) {
		t.Fatalf("collision error should wrap ErrAgentNameTaken, got: %v", err)
	}
	if _, err := m.Rename("leo-a", "leo-a"); err == nil {
		t.Fatal("expected unchanged-name error")
	} else if !errors.Is(err, ErrAgentNameUnchanged) {
		t.Fatalf("unchanged error should wrap ErrAgentNameUnchanged, got: %v", err)
	}
	if _, err := m.Rename("leo-a", "bad name!"); err == nil {
		t.Fatal("expected invalid-name error")
	} else if !errors.Is(err, ErrInvalidAgentName) {
		t.Fatalf("invalid-name error should wrap ErrInvalidAgentName, got: %v", err)
	}
}

func TestManagerRename_LiveIntoStoppedNameCollision(t *testing.T) {
	home := t.TempDir()
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-a", ClaudeArgs: []string{"--name", "leo-a"}})
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-b", Stopped: true, ClaudeArgs: []string{"--name", "leo-b"}})
	// leo-a is LIVE, leo-b is a stopped store record (not live).
	sup := &fakeSupervisor{ephemeral: map[string]ProcessState{"leo-a": {Name: "leo-a", Status: "running"}}}
	m := newTestManager(t, home, sup)

	if _, err := m.Rename("leo-a", "leo-b"); err == nil {
		t.Fatal("expected collision error renaming live agent into a stopped agent's name")
	} else if !errors.Is(err, ErrAgentNameTaken) {
		t.Fatalf("live-into-stopped collision should wrap ErrAgentNameTaken, got: %v", err)
	}
	// The supervisor must NOT have been renamed (pre-check rejects before the call).
	if sup.renamedNew != "" {
		t.Fatalf("supervisor was renamed despite store collision: %q", sup.renamedNew)
	}
	// Store unchanged: leo-a still present, leo-b still present.
	recs, _ := agentstore.Load(agentstore.FilePath(home))
	if _, ok := recs["leo-a"]; !ok {
		t.Fatal("leo-a record lost")
	}
}
