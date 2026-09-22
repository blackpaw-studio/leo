package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/observe"
)

func TestSpawnedAgentViewCarriesAttentionOnlyWhenPresent(t *testing.T) {
	sv := NewSupervisor(context.Background())
	sv.homePath = t.TempDir()
	store := observe.NewAttentionStore(nil)
	store.Set("with", observe.AttentionErrored)
	sv.SetAttention(store)

	with := sv.spawnedAgentView(daemon.AgentSpawnSpec{Name: "with"}, time.Now())
	without := sv.spawnedAgentView(daemon.AgentSpawnSpec{Name: "without"}, time.Now())

	if with.Attention == nil || *with.Attention != (observe.AgentAttention{State: observe.AttentionErrored, Revision: 1}) {
		t.Errorf("with.Attention = %+v", with.Attention)
	}
	if without.Attention != nil {
		t.Errorf("without.Attention = %+v, want absent", without.Attention)
	}
}

func TestWireObservabilitySharesAttentionStore(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sv := NewSupervisor(ctx)
	sv.homePath = t.TempDir()

	obs := wireObservability(ctx, sv, writeFakeTmuxScript(t))

	if obs.Attention == nil || sv.attentionStore() != obs.Attention {
		t.Fatal("supervisor not wired to the returned attention store")
	}
	events, unsub, _ := obs.Bus.Subscribe(4)
	defer unsub()
	obs.Attention.Set("a", observe.AttentionWorking)
	ev := <-events
	p, ok := ev.Payload.(*observe.AgentActivityPayload)
	if !ok || p.Attention == nil || p.Attention.State != observe.AttentionWorking {
		t.Fatalf("bus event = %+v", ev)
	}
}

// spawnFakehook spawns name through SpawnAgent on the fakehook harness with a
// tmux stub whose has-session always fails, so every launch looks like an
// immediate unexpected exit. hooked selects whether the launch carries
// attention hooks (and so starts tracked).
func spawnFakehook(t *testing.T, sv *Supervisor, name string, hooked bool) {
	t.Helper()
	testFakeDriver = &fakeAttentionDriver{supported: hooked}
	t.Cleanup(func() { testFakeDriver = nil })
	origPoll, origBackoff := sessionPollInterval, initialBackoff
	sessionPollInterval, initialBackoff = time.Millisecond, time.Hour
	t.Cleanup(func() { sessionPollInterval, initialBackoff = origPoll, origBackoff })
	sv.tmuxPath = exitingTmuxStub(t)
	sv.homePath = t.TempDir()
	if err := agentstore.Save(sv.homePath, agentstore.Record{Name: name}); err != nil {
		t.Fatal(err)
	}
	if err := sv.SpawnAgent(daemon.AgentSpawnSpec{Name: name, WorkDir: t.TempDir(), Harness: "fakehook"}); err != nil {
		t.Fatalf("SpawnAgent: %v", err)
	}
}

// exitingTmuxStub launches successfully (new-session reports a pane id) but
// has-session always fails, so the session appears to die on its own.
func exitingTmuxStub(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tmux")
	script := "#!/bin/sh\nfor a in \"$@\"; do case \"$a\" in new-session) echo '%1'; exit 0;; has-session) exit 1;; esac; done\nexit 0\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil { //nolint:gosec // test fixture, needs +x
		t.Fatal(err)
	}
	return path
}

func waitAttention(t *testing.T, store *observe.AttentionStore, name string, want observe.AttentionState) observe.AgentAttention {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if att, ok := store.Get(name); ok && att.State == want {
			return att
		}
		time.Sleep(5 * time.Millisecond)
	}
	att, ok := store.Get(name)
	t.Fatalf("attention = %+v (present=%v), want %s", att, ok, want)
	return att
}

func TestUnexpectedExitMarksTrackedAgentErrored(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sv := NewSupervisor(ctx)
	store := observe.NewAttentionStore(nil)
	sv.SetAttention(store)

	spawnFakehook(t, sv, "tracked", true)

	waitAttention(t, store, "tracked", observe.AttentionErrored)
	// The dead launch's token no longer routes, so its late hooks can't
	// overwrite errored.
	if all := store.All(); len(all) != 1 {
		t.Fatalf("attention = %+v", all)
	}
	tok := storedToken(t, sv.homePath, "tracked")
	if name, ok := store.AgentForToken(tok); tok == "" || ok {
		t.Fatalf("launch token %q still routes to %q after the launch exited", tok, name)
	}
	_ = sv.StopAgent("tracked", false)
}

func TestUnexpectedExitLeavesUntrackedAgentAbsent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sv := NewSupervisor(ctx)
	store := observe.NewAttentionStore(nil)
	sv.SetAttention(store)

	spawnFakehook(t, sv, "untracked", false)
	waitForRestarts(t, sv, "untracked")

	if att, ok := store.Get("untracked"); ok {
		t.Fatalf("attention = %+v, want absent", att)
	}
	_ = sv.StopAgent("untracked", false)
}

func TestStopAgentMarksTrackedAgentUnknown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sv := NewSupervisor(ctx)
	store := observe.NewAttentionStore(nil)
	sv.SetAttention(store)
	spawnFakehook(t, sv, "tracked", true)
	waitAttention(t, store, "tracked", observe.AttentionErrored)
	store.Set("tracked", observe.AttentionWorking)

	if err := sv.StopAgent("tracked", true); err != nil {
		t.Fatalf("StopAgent: %v", err)
	}

	att, ok := store.Get("tracked")
	if !ok || att.State != observe.AttentionUnknown {
		t.Fatalf("attention after stop = %+v, %v; want unknown", att, ok)
	}
	// The dead goroutine must not overwrite unknown with errored afterwards.
	time.Sleep(50 * time.Millisecond)
	if att2, _ := store.Get("tracked"); att2 != att {
		t.Fatalf("attention changed after stop: %+v -> %+v", att, att2)
	}
}

func TestStopAgentLeavesUntrackedAgentAbsent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sv := NewSupervisor(ctx)
	store := observe.NewAttentionStore(nil)
	sv.SetAttention(store)
	spawnFakehook(t, sv, "untracked", false)

	if err := sv.StopAgent("untracked", false); err != nil {
		t.Fatalf("StopAgent: %v", err)
	}
	if att, ok := store.Get("untracked"); ok {
		t.Fatalf("attention = %+v, want absent", att)
	}
}

func TestDaemonShutdownDoesNotMarkErrored(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	sv := NewSupervisor(ctx)
	store := observe.NewAttentionStore(nil)
	sv.SetAttention(store)
	origPoll := sessionPollInterval
	sessionPollInterval = time.Hour // park in waitForSessionEnd until cancel
	t.Cleanup(func() { sessionPollInterval = origPoll })
	testFakeDriver = &fakeAttentionDriver{supported: true}
	t.Cleanup(func() { testFakeDriver = nil })
	tmuxPath, logPath := liveTmuxStub(t)
	sv.tmuxPath = tmuxPath
	sv.homePath = t.TempDir()
	if err := sv.SpawnAgent(daemon.AgentSpawnSpec{Name: "tracked", WorkDir: t.TempDir(), Harness: "fakehook"}); err != nil {
		t.Fatal(err)
	}
	waitForLog(t, logPath, "new-session")
	before := waitAttention(t, store, "tracked", observe.AttentionUnknown)

	cancel()
	waitForState(t, sv, "tracked", "stopped")

	if att, _ := store.Get("tracked"); att != before {
		t.Fatalf("attention after shutdown = %+v, want %+v untouched", att, before)
	}
}

func waitForState(t *testing.T, sv *Supervisor, name, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		sv.mu.RLock()
		st, ok := sv.states[name]
		status := ""
		if ok {
			status = st.Status
		}
		sv.mu.RUnlock()
		if status == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s never reached status %q", name, want)
}

// waitForRestarts waits until the supervise loop has counted an unexpected
// exit for name — the point at which errored would have been recorded.
func waitForRestarts(t *testing.T, sv *Supervisor, name string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		sv.mu.RLock()
		st, ok := sv.states[name]
		n := 0
		if ok {
			n = st.Restarts
		}
		sv.mu.RUnlock()
		if n > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s never restarted", name)
}

func TestSessionEnvArgsCarriesLaunchAttentionToken(t *testing.T) {
	hooked := strings.Join(sessionEnvArgs("/tmux", ProcessSpec{Name: "leo-a", Kind: harness.KindAgent, attentionToken: "tok"}, nil), " ")
	unhooked := strings.Join(sessionEnvArgs("/tmux", ProcessSpec{Name: "leo-a", Kind: harness.KindAgent}, nil), " ")

	if !strings.Contains(hooked, "-e LEO_ATTENTION_TOKEN=tok") {
		t.Errorf("hooked env = %s, want LEO_ATTENTION_TOKEN", hooked)
	}
	if strings.Contains(unhooked, "LEO_ATTENTION") {
		t.Errorf("unhooked env = %s, want no attention token", unhooked)
	}
}

func TestSessionEnvArgsIgnoresSpecEnvAttentionVars(t *testing.T) {
	env := map[string]string{"LEO_ATTENTION_TOKEN": "spoof"}
	for _, tok := range []string{"", "tok"} {
		args := strings.Join(sessionEnvArgs("/tmux", ProcessSpec{Name: "leo-a", Kind: harness.KindAgent, Env: env, attentionToken: tok}, nil), " ")
		if strings.Contains(args, "spoof") {
			t.Errorf("env = %s, want leo's value to win", args)
		}
	}
}

// laterHookedDriver launches unhooked the first time and hooked after.
type laterHookedDriver struct {
	fakeHookDriver
	calls atomic.Int32
}

func (d *laterHookedDriver) AttentionLaunch(_ harness.SessionHandle, args, _ []string) ([]string, bool, error) {
	if d.calls.Add(1) == 1 {
		return args, false, nil
	}
	return args, true, nil
}

// A resumed spawn is working only on its first launch: a later in-loop
// restart that is the first hooked launch starts unknown, not working.
func TestResumedSpawnIsWorkingOnlyOnFirstLaunch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sv := NewSupervisor(ctx)
	pub := &recordingPublisher{}
	store := observe.NewAttentionStore(pub)
	sv.SetAttention(store)
	testFakeDriver = &laterHookedDriver{}
	t.Cleanup(func() { testFakeDriver = nil })
	origPoll, origBackoff := sessionPollInterval, initialBackoff
	sessionPollInterval, initialBackoff = time.Millisecond, time.Millisecond
	t.Cleanup(func() { sessionPollInterval, initialBackoff = origPoll, origBackoff })
	sv.tmuxPath = exitingTmuxStub(t)
	sv.homePath = t.TempDir()
	if err := sv.SpawnAgent(daemon.AgentSpawnSpec{Name: "resumed", WorkDir: t.TempDir(), Harness: "fakehook", Resumed: true}); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sv.StopAgent("resumed", false) }()

	deadline := time.Now().Add(5 * time.Second)
	for len(pub.Events()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	events := pub.Events()
	if len(events) == 0 {
		t.Fatal("no attention published")
	}
	first := events[0].Payload.(*observe.AgentActivityPayload).Attention
	if first == nil || first.State != observe.AttentionUnknown {
		t.Fatalf("first hooked launch (a restart) attention = %+v, want unknown", first)
	}
}
