package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stubLiveTmux writes a tmux stub that logs every invocation and reports a
// live session for has-session, so a supervise loop parks in
// waitForSessionEnd instead of recreating the session.
func stubLiveTmux(t *testing.T, dir, logPath string) string {
	t.Helper()
	stub := filepath.Join(dir, "tmux")
	script := "#!/bin/sh\necho \"$@\" >> " + logPath + "\nexit 0\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return stub
}

// waitForStatus blocks until name's supervised status equals want, failing the
// test if it never does.
func waitForStatus(t *testing.T, sv *Supervisor, name, want string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		sv.mu.RLock()
		got := ""
		if st := sv.states[name]; st != nil {
			got = st.Status
		}
		sv.mu.RUnlock()
		if got == want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("status never reached %q; last was %q", want, got)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// A stop-then-respawn (leo agent restart / reset / set-template, or an
// idle-suspend immediately followed by an auto-resume) cancels the old
// supervise goroutine's context and immediately registers a NEW generation
// under the same name. The old goroutine is still parked in waitForSessionEnd
// at that moment: when it finally observes the cancellation it must NOT touch
// the successor's state or tmux session, both of which are keyed by the same
// agent name.
//
// Without a generation guard the stale goroutine marks a live agent "stopped"
// (the attach picker, `leo agent list`, the web UI and /api/v1/state all then
// report a running agent as stopped) and kills the successor's freshly created
// tmux session out from under it.
func TestStaleSuperviseGoroutineDoesNotStompSuccessor(t *testing.T) {
	origPoll := sessionPollInterval
	sessionPollInterval = 10 * time.Millisecond
	defer func() { sessionPollInterval = origPoll }()

	dir := t.TempDir()
	logPath := filepath.Join(dir, "tmux-args.log")
	tmuxStub := stubLiveTmux(t, dir, logPath)

	root, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	sv := NewSupervisor(root)
	sv.homePath = t.TempDir()

	const name = "gen"

	// Generation 1: what SpawnAgent registers, then supervises.
	id1 := newProcIdentity(name, []string{"--model", "sonnet"})
	sv.mu.Lock()
	sv.states[name] = &ProcessState{Name: name, Status: "starting", Ephemeral: true}
	sv.identities[name] = id1
	sv.mu.Unlock()

	childCtx, childCancel := context.WithCancel(root)
	spec := ProcessSpec{Name: name, ClaudeArgs: []string{"--model", "sonnet"}, WorkDir: t.TempDir(), Adopt: true}
	done := make(chan struct{})
	go func() {
		defer close(done)
		superviseProcess(childCtx, tmuxStub, "false", spec, sv.homePath, sv, id1)
	}()
	waitForStatus(t, sv, name, "running")

	// Generation 2: stopAgentProcess drops generation 1's bookkeeping, then
	// SpawnAgent files a fresh state + identity under the same name.
	sv.mu.Lock()
	delete(sv.states, name)
	delete(sv.identities, name)
	sv.states[name] = &ProcessState{Name: name, Status: "running", Ephemeral: true}
	sv.identities[name] = newProcIdentity(name, []string{"--model", "sonnet"})
	sv.mu.Unlock()

	// Only tmux calls made after the cancellation matter for the kill check.
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	childCancel()
	<-done

	sv.mu.RLock()
	got := sv.states[name].Status
	sv.mu.RUnlock()
	if got != "running" {
		t.Errorf("successor status = %q, want %q (stale goroutine stomped the live generation)", got, "running")
	}

	logged, _ := os.ReadFile(logPath)
	if strings.Contains(string(logged), "kill-session") {
		t.Errorf("stale goroutine killed the successor's tmux session; tmux calls after cancel:\n%s", logged)
	}
}

// The flip side of the guard above: when the DAEMON shuts down (the parent
// context every supervise goroutine derives from is cancelled) nobody else
// tears the tmux sessions down, so each goroutine must still kill its own.
func TestShutdownStillKillsOwnSession(t *testing.T) {
	origPoll := sessionPollInterval
	sessionPollInterval = 10 * time.Millisecond
	defer func() { sessionPollInterval = origPoll }()

	dir := t.TempDir()
	logPath := filepath.Join(dir, "tmux-args.log")
	tmuxStub := stubLiveTmux(t, dir, logPath)

	root, rootCancel := context.WithCancel(context.Background())
	sv := NewSupervisor(root)
	sv.homePath = t.TempDir()

	const name = "shut"
	id := newProcIdentity(name, nil)
	sv.mu.Lock()
	sv.states[name] = &ProcessState{Name: name, Status: "starting", Ephemeral: true}
	sv.identities[name] = id
	sv.mu.Unlock()

	spec := ProcessSpec{Name: name, WorkDir: t.TempDir(), Adopt: true}
	done := make(chan struct{})
	go func() {
		defer close(done)
		superviseProcess(root, tmuxStub, "false", spec, sv.homePath, sv, id)
	}()
	waitForStatus(t, sv, name, "running")

	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	rootCancel()
	<-done

	logged, _ := os.ReadFile(logPath)
	if !strings.Contains(string(logged), "kill-session") {
		t.Errorf("daemon shutdown must kill the agent's tmux session; tmux calls after cancel:\n%s", logged)
	}
}

// The generation guard must also silence the event stream: a stale
// setState/incrementRestarts that published would tell every observability
// consumer (web UI, /api/v1/events) that a live agent stopped, even though the
// supervisor's own state was left untouched.
func TestStaleGenerationNeitherMutatesNorPublishes(t *testing.T) {
	sv := NewSupervisor(context.Background())
	pub := &recordingPublisher{}
	sv.SetPublisher(pub)

	stale := newProcIdentity("agent-a", nil)
	live := newProcIdentity("agent-a", nil)
	sv.mu.Lock()
	sv.states["agent-a"] = &ProcessState{Name: "agent-a", Status: "running"}
	sv.identities["agent-a"] = live
	sv.mu.Unlock()

	sv.setState("agent-a", stale, "stopped")
	sv.incrementRestarts("agent-a", stale)

	sv.mu.RLock()
	st := *sv.states["agent-a"]
	sv.mu.RUnlock()
	if st.Status != "running" || st.Restarts != 0 {
		t.Errorf("stale generation mutated live state: %+v", st)
	}
	if len(pub.events) != 0 {
		t.Errorf("stale generation published %d event(s), want 0", len(pub.events))
	}
}

// stopAgentProcess is the only thing that tears an agent's tmux session down
// on a stop/suspend — the supervise goroutine deliberately no longer kills on
// a per-agent cancel (see TestStaleSuperviseGoroutineDoesNotStompSuccessor).
// So a kill that silently fails would strand a live claude: leo drops every
// trace of the agent while the session keeps running and answering on its
// channels. The kill must be verified and retried.
func TestStopAgentProcessRetriesKillUntilSessionIsGone(t *testing.T) {
	origHas := tmuxHasSession
	defer func() { tmuxHasSession = origHas }()

	dir := t.TempDir()
	logPath := filepath.Join(dir, "tmux-args.log")
	tmuxStub := stubLiveTmux(t, dir, logPath)

	tests := []struct {
		name        string
		alive       func(calls int) bool
		wantKills   int
		wantWarning bool
	}{
		{
			name:      "kill succeeds first time",
			alive:     func(int) bool { return false },
			wantKills: 1,
		},
		{
			name:      "session survives the first kill",
			alive:     func(calls int) bool { return calls < 2 },
			wantKills: 2,
		},
		{
			name:        "session never dies",
			alive:       func(int) bool { return true },
			wantKills:   killSessionAttempts,
			wantWarning: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(logPath, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			calls := 0
			tmuxHasSession = func(_, _ string) bool {
				calls++
				return tc.alive(calls)
			}

			sv := NewSupervisor(context.Background())
			sv.tmuxPath = tmuxStub
			sv.mu.Lock()
			sv.states["doomed"] = &ProcessState{Name: "doomed", Status: "running", Ephemeral: true}
			sv.mu.Unlock()

			var warnings strings.Builder
			stopWarnLog = &warnings
			defer func() { stopWarnLog = os.Stderr }()

			if err := sv.stopAgentProcess("doomed"); err != nil {
				t.Fatalf("stopAgentProcess: %v", err)
			}

			logged, _ := os.ReadFile(logPath)
			if got := strings.Count(string(logged), "kill-session"); got != tc.wantKills {
				t.Errorf("kill-session attempts = %d, want %d; tmux calls:\n%s", got, tc.wantKills, logged)
			}
			if warned := strings.Contains(warnings.String(), "doomed"); warned != tc.wantWarning {
				t.Errorf("warned = %v, want %v (log: %q)", warned, tc.wantWarning, warnings.String())
			}

			// The agent must leave the live-state maps either way: a wedged
			// tmux cannot be allowed to make an agent unmanageable.
			sv.mu.RLock()
			_, stillTracked := sv.states["doomed"]
			sv.mu.RUnlock()
			if stillTracked {
				t.Error("agent still tracked after stop")
			}
		})
	}
}
