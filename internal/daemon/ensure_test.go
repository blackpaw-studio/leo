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
	live      map[string]bool
	suspended map[string]bool
	resumed   []string
	spawned   []string
	resumeErr error
	spawnErr  error
	// spawnName, when set, is returned as the spawned Record.Name instead of
	// the requested name — simulating a reservation collision that suffixed
	// the name (e.g. "foo" -> "foo-2").
	spawnName string
}

func (f *fakeEnsureMgr) Live(name string) bool      { return f.live[name] }
func (f *fakeEnsureMgr) Suspended(name string) bool { return f.suspended[name] }

func (f *fakeEnsureMgr) Resume(name string) (agent.Record, error) {
	f.resumed = append(f.resumed, name)
	if f.resumeErr != nil {
		return agent.Record{}, f.resumeErr
	}
	return agent.Record{Name: name}, nil
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
	if len(f.resumed) != 0 {
		t.Fatalf("expected no Resume calls, got %v", f.resumed)
	}
	if len(f.spawned) != 0 {
		t.Fatalf("expected no Spawn calls, got %v", f.spawned)
	}
}

func TestEnsureSuspendedResumes(t *testing.T) {
	f := &fakeEnsureMgr{suspended: map[string]bool{"foo": true}}
	e := NewAgentEnsurer(f)

	if err := e.Ensure(context.Background(), EnsureSpec{Name: "foo"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(f.resumed) != 1 || f.resumed[0] != "foo" {
		t.Fatalf("expected Resume(foo) once, got %v", f.resumed)
	}
	if len(f.spawned) != 0 {
		t.Fatalf("expected no Spawn calls, got %v", f.spawned)
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
	if len(f.resumed) != 0 {
		t.Fatalf("expected no Resume calls, got %v", f.resumed)
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

func TestEnsureResumeFailurePropagates(t *testing.T) {
	wantErr := errors.New("boom")
	f := &fakeEnsureMgr{suspended: map[string]bool{"foo": true}, resumeErr: wantErr}
	e := NewAgentEnsurer(f)

	err := e.Ensure(context.Background(), EnsureSpec{Name: "foo"})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected wrapped %v, got %v", wantErr, err)
	}
}
