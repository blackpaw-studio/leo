package daemon

import (
	"context"
	"errors"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/config"
)

// fakeEnsureMgr is a tiny fake satisfying EnsureAgentManager, capturing calls
// for assertion instead of touching a real supervisor/agentstore.
type fakeEnsureMgr struct {
	live     map[string]bool
	wakeable map[string]bool
	stopped  map[string]bool
	started  []string
	spawned  []string
	startErr error
	spawnErr error
	// spawnName, when set, is returned as the spawned Record.Name instead of
	// the requested name — simulating a reservation collision that suffixed
	// the name (e.g. "foo" -> "foo-2").
	spawnName string
}

func (f *fakeEnsureMgr) Live(name string) bool     { return f.live[name] }
func (f *fakeEnsureMgr) Wakeable(name string) bool { return f.wakeable[name] }
func (f *fakeEnsureMgr) Stopped(name string) bool  { return f.stopped[name] }

func (f *fakeEnsureMgr) Start(name string) error {
	f.started = append(f.started, name)
	return f.startErr
}

func (f *fakeEnsureMgr) SpawnFromTemplate(_ context.Context, name string, _ config.TemplateConfig) (agent.Record, error) {
	f.spawned = append(f.spawned, name)
	if f.spawnErr != nil {
		return agent.Record{}, f.spawnErr
	}
	if f.spawnName != "" {
		return agent.Record{Name: f.spawnName}, nil
	}
	return agent.Record{Name: name}, nil
}

func TestEnsureRunningIsNoop(t *testing.T) {
	f := &fakeEnsureMgr{live: map[string]bool{"foo": true}}
	e := NewAgentEnsurer(f)

	if err := e.Ensure(context.Background(), EnsureSpec{Name: "foo"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(f.started) != 0 {
		t.Fatalf("expected no Start calls, got %v", f.started)
	}
	if len(f.spawned) != 0 {
		t.Fatalf("expected no Spawn calls, got %v", f.spawned)
	}
}

func TestEnsureWakeableStarts(t *testing.T) {
	f := &fakeEnsureMgr{wakeable: map[string]bool{"foo": true}}
	e := NewAgentEnsurer(f)

	if err := e.Ensure(context.Background(), EnsureSpec{Name: "foo"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(f.started) != 1 || f.started[0] != "foo" {
		t.Fatalf("expected Start(foo) once, got %v", f.started)
	}
	if len(f.spawned) != 0 {
		t.Fatalf("expected no Spawn calls, got %v", f.spawned)
	}
}

// TestEnsureStoppedNotWakeableRefuses is the load-bearing auto-wake guard on
// the persistent-task injection path: a dormant record with WakeOnMessage=
// false (Stopped=true, Wakeable=false — an operator-initiated stop) must be
// refused, never started and never respawned over.
func TestEnsureStoppedNotWakeableRefuses(t *testing.T) {
	f := &fakeEnsureMgr{stopped: map[string]bool{"foo": true}}
	e := NewAgentEnsurer(f)

	err := e.Ensure(context.Background(), EnsureSpec{Name: "foo"})
	if err == nil {
		t.Fatal("expected an error for a stopped, non-wakeable agent")
	}
	if len(f.started) != 0 {
		t.Fatalf("expected no Start calls, got %v", f.started)
	}
	if len(f.spawned) != 0 {
		t.Fatalf("expected no Spawn calls (must not respawn over a manually stopped agent), got %v", f.spawned)
	}
}

func TestEnsureMissingSpawns(t *testing.T) {
	f := &fakeEnsureMgr{}
	e := NewAgentEnsurer(f)
	tmpl := config.TemplateConfig{Model: "sonnet"}

	if err := e.Ensure(context.Background(), EnsureSpec{Name: "foo", Template: tmpl}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(f.spawned) != 1 || f.spawned[0] != "foo" {
		t.Fatalf("expected SpawnFromTemplate(foo) once, got %v", f.spawned)
	}
	if len(f.started) != 0 {
		t.Fatalf("expected no Start calls, got %v", f.started)
	}
}

func TestEnsureSpawnFailurePropagates(t *testing.T) {
	wantErr := errors.New("boom")
	f := &fakeEnsureMgr{spawnErr: wantErr}
	e := NewAgentEnsurer(f)

	err := e.Ensure(context.Background(), EnsureSpec{Name: "foo"})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected wrapped %v, got %v", wantErr, err)
	}
}

func TestEnsureSpawnNameCollisionFails(t *testing.T) {
	f := &fakeEnsureMgr{spawnName: "foo-2"}
	e := NewAgentEnsurer(f)

	err := e.Ensure(context.Background(), EnsureSpec{Name: "foo"})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if len(f.spawned) != 1 || f.spawned[0] != "foo" {
		t.Fatalf("expected SpawnFromTemplate(foo) once, got %v", f.spawned)
	}
}

func TestEnsureStartFailurePropagates(t *testing.T) {
	wantErr := errors.New("boom")
	f := &fakeEnsureMgr{wakeable: map[string]bool{"foo": true}, startErr: wantErr}
	e := NewAgentEnsurer(f)

	err := e.Ensure(context.Background(), EnsureSpec{Name: "foo"})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected wrapped %v, got %v", wantErr, err)
	}
}
