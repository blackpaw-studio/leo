package service

import (
	"context"
	"testing"
	"time"
)

// superviseProcess, when handed Adopt=true and a live tmux session, must
// re-attach to the running session instead of killing and recreating it. With
// tmuxPath set to "false", the create path (tmux new-session) would fail and
// flip the agent to "restarting"; the adopt path skips create entirely and the
// agent stays "running". This is the per-agent half of the daemon-restart fix:
// a bounce no longer tears down healthy agent sessions.
func TestSuperviseProcessAdoptsLiveSession(t *testing.T) {
	origHas := tmuxHasSession
	tmuxHasSession = func(_, _ string) bool { return true }
	defer func() { tmuxHasSession = origHas }()

	origPoll := sessionPollInterval
	sessionPollInterval = 10 * time.Millisecond
	defer func() { sessionPollInterval = origPoll }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sv := NewSupervisor(ctx)
	sv.homePath = t.TempDir()

	id := newProcIdentity("leoterm", []string{"--model", "sonnet"})
	spec := ProcessSpec{
		Name:       "leoterm",
		ClaudeArgs: []string{"--model", "sonnet"},
		WorkDir:    t.TempDir(),
		Adopt:      true,
	}

	// tmuxPath/claudePath = "false": any attempt to create a session fails.
	// done closes when the goroutine exits so the deferred global restores
	// above don't race the goroutine's reads of them.
	done := make(chan struct{})
	go func() {
		defer close(done)
		superviseProcess(ctx, "false", "false", spec, sv.homePath, sv, id)
	}()
	defer func() {
		cancel()
		<-done
	}()

	// Let the first iteration settle. An adopted session stays "running"; a
	// recreated one would have hit the failing new-session and gone
	// "restarting".
	deadline := time.After(2 * time.Second)
	for {
		sv.mu.RLock()
		st := sv.states["leoterm"]
		status := ""
		if st != nil {
			status = st.Status
		}
		restarts := 0
		if st != nil {
			restarts = st.Restarts
		}
		sv.mu.RUnlock()

		if status == "restarting" || restarts > 0 {
			t.Fatalf("adopted agent should not recreate its session; status=%q restarts=%d", status, restarts)
		}
		if status == "running" {
			break // adopted as expected
		}
		select {
		case <-deadline:
			t.Fatalf("agent never reached running state; last status=%q", status)
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
}
