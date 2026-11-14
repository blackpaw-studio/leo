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
	if rec.Name != "renamed-agent" {
		t.Fatalf("returned record name = %q", rec.Name)
	}
	if sup.renamedOld != "leo-old" || sup.renamedNew != "renamed-agent" {
		t.Fatalf("supervisor not called: %q -> %q", sup.renamedOld, sup.renamedNew)
	}
	recs, _ := agentstore.Load(agentstore.FilePath(home))
	got, ok := recs["renamed-agent"]
	if !ok {
		t.Fatal("store not re-keyed")
	}
	if strings.Join(got.ClaudeArgs, " ") != "--name renamed-agent --model opus" {
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

// Drivers acting on an agent's handle talk to the daemon's resolved tmux, so
// they never need one on PATH.
func TestHandleForRecordCarriesTheManagersTmux(t *testing.T) {
	loader := func() (*config.Config, error) { return &config.Config{HomePath: t.TempDir()}, nil }
	m := New(loader, &fakeSupervisor{}, "/managed/3.5/tmux", "")

	h := m.handleForRecord(t.TempDir(), agentstore.Record{Name: "leo-a"})

	if h.TmuxPath != "/managed/3.5/tmux" {
		t.Errorf("TmuxPath = %q, want /managed/3.5/tmux", h.TmuxPath)
	}
}

// Regression: rename used to force a leo- prefix onto the record name, so
// `rename leo-claude-documentary documentary` produced "leo-documentary" and
// a repeat of the same command failed with "agent name unchanged".
func TestManagerRename_KeepsNameAsGiven(t *testing.T) {
	home := t.TempDir()
	_ = agentstore.Save(home, agentstore.Record{
		Name:       "leo-claude-documentary",
		ClaudeArgs: []string{"--name", "leo-claude-documentary"},
	})
	sup := &fakeSupervisor{ephemeral: map[string]ProcessState{"leo-claude-documentary": {Name: "leo-claude-documentary", Status: "running"}}}
	m := newTestManager(t, home, sup)

	rec, err := m.Rename("leo-claude-documentary", "documentary")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if rec.Name != "documentary" || sup.renamedNew != "documentary" {
		t.Fatalf("name = %q, supervisor got %q; want documentary", rec.Name, sup.renamedNew)
	}
	recs, _ := agentstore.Load(agentstore.FilePath(home))
	if _, ok := recs["documentary"]; !ok {
		t.Fatalf("store not keyed by documentary: %v", recs)
	}
	if _, err := m.Rename("documentary", "Documentary"); !errors.Is(err, ErrAgentNameUnchanged) {
		t.Fatalf("renaming to same name: err = %v, want ErrAgentNameUnchanged", err)
	}
}

func TestManagerRename_RejectsSessionNameClash(t *testing.T) {
	home := t.TempDir()
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-foo", ClaudeArgs: []string{"--name", "leo-foo"}})
	_ = agentstore.Save(home, agentstore.Record{Name: "x", ClaudeArgs: []string{"--name", "x"}})
	sup := &fakeSupervisor{ephemeral: map[string]ProcessState{"x": {Name: "x", Status: "running"}}}
	m := newTestManager(t, home, sup)

	if _, err := m.Rename("x", "foo"); !errors.Is(err, ErrAgentNameTaken) {
		t.Fatalf("err = %v, want ErrAgentNameTaken", err)
	}
	if sup.renamedNew != "" {
		t.Fatalf("supervisor renamed despite clash: %q", sup.renamedNew)
	}
}

func TestManagerRename_LiveStateSessionClash(t *testing.T) {
	home := t.TempDir()
	_ = agentstore.Save(home, agentstore.Record{Name: "x"})
	sup := &fakeSupervisor{ephemeral: map[string]ProcessState{
		"x":       {Name: "x", Status: "running"},
		"leo-foo": {Name: "leo-foo", Status: "running"},
	}}
	m := newTestManager(t, home, sup)
	if _, err := m.Rename("x", "foo"); !errors.Is(err, ErrAgentNameTaken) {
		t.Fatalf("err = %v, want ErrAgentNameTaken", err)
	}
}

func TestManagerRename_SelfSessionNotAClash(t *testing.T) {
	home := t.TempDir()
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-foo", ClaudeArgs: []string{"--name", "leo-foo"}})
	sup := &fakeSupervisor{ephemeral: map[string]ProcessState{"leo-foo": {Name: "leo-foo", Status: "running"}}}
	m := newTestManager(t, home, sup)
	rec, err := m.Rename("leo-foo", "foo")
	if err != nil || rec.Name != "foo" {
		t.Fatalf("rec=%q err=%v, want foo/nil", rec.Name, err)
	}
}
