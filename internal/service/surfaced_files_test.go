package service

import (
	"context"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/observe"
)

var surfaceOldIncarnation = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

func seededSurfaceStore(t *testing.T, name string) *observe.SurfacedFileStore {
	t.Helper()
	store := observe.NewSurfacedFileStore(nil, nil)
	if _, err := store.Add(name, surfaceOldIncarnation, observe.SurfaceInput{Path: "old", AbsPath: "/old"}); err != nil {
		t.Fatal(err)
	}
	return store
}

// assertIncarnationReset checks the store dropped the old incarnation and now
// accepts exactly the supervisor's current StartedAt.
func assertIncarnationReset(t *testing.T, sv *Supervisor, store *observe.SurfacedFileStore, name string) {
	t.Helper()
	if got := store.Get(name); len(got) != 0 {
		t.Fatalf("files from the previous incarnation survived: %+v", got)
	}
	sv.mu.RLock()
	startedAt := sv.states[name].StartedAt
	sv.mu.RUnlock()
	if _, err := store.Add(name, surfaceOldIncarnation, observe.SurfaceInput{Path: "late", AbsPath: "/late"}); err == nil {
		t.Fatal("store still accepts the previous incarnation")
	}
	if _, err := store.Add(name, startedAt, observe.SurfaceInput{Path: "new", AbsPath: "/new"}); err != nil {
		t.Fatalf("store rejects the current incarnation: %v", err)
	}
}

func TestInitStateResetsSurfacedFiles(t *testing.T) {
	sv := NewSupervisor(context.Background())
	store := seededSurfaceStore(t, "a")
	sv.SetSurfacedFiles(store)

	sv.initState("a")

	assertIncarnationReset(t, sv, store, "a")
}

func TestIncrementRestartsResetsSurfacedFiles(t *testing.T) {
	sv := NewSupervisor(context.Background())
	store := seededSurfaceStore(t, "a")
	sv.SetSurfacedFiles(store)
	id := newProcIdentity("a", nil)
	sv.mu.Lock()
	sv.states["a"] = &ProcessState{Name: "a", Status: "running", StartedAt: surfaceOldIncarnation}
	sv.identities["a"] = id
	sv.mu.Unlock()

	sv.incrementRestarts("a", id)

	assertIncarnationReset(t, sv, store, "a")
}

func TestSpawnAgentResetsSurfacedFiles(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sv := NewSupervisor(ctx)
	store := seededSurfaceStore(t, "spawned")
	sv.SetSurfacedFiles(store)

	spawnFakehook(t, sv, "spawned", false)
	defer func() { _ = sv.StopAgent("spawned", false) }()

	if got := store.Get("spawned"); len(got) != 0 {
		t.Fatalf("files from the previous incarnation survived a spawn: %+v", got)
	}
	if _, err := store.Add("spawned", surfaceOldIncarnation, observe.SurfaceInput{Path: "late", AbsPath: "/late"}); err == nil {
		t.Fatal("store still accepts the pre-spawn incarnation")
	}
}

func TestWireObservabilitySharesSurfacedFileStore(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sv := NewSupervisor(ctx)
	sv.homePath = t.TempDir()

	obs := wireObservability(ctx, sv, writeFakeTmuxScript(t))

	if obs.SurfacedFiles == nil || sv.surfacedFileStore() != obs.SurfacedFiles {
		t.Fatal("supervisor not wired to the returned surfaced-file store")
	}
	events, unsub, _ := obs.Bus.Subscribe(4)
	defer unsub()
	if _, err := obs.SurfacedFiles.Add("a", time.Now(), observe.SurfaceInput{Path: "p", AbsPath: "/p"}); err != nil {
		t.Fatal(err)
	}
	if ev := <-events; ev.Type != observe.EventFileSurfaced {
		t.Fatalf("bus event = %+v", ev)
	}
}
