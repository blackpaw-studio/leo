package service

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestWaitForSessionEnd_FollowsLiveRename proves the zero-restart mechanism:
// while the agent's tmux session is renamed live, waitForSessionEnd re-reads the
// session name from the procIdentity handle each poll, so it keeps following the
// session (never returning false / "session ended") and begins polling the NEW
// name. It only returns false once the (renamed) session actually disappears.
func TestWaitForSessionEnd_FollowsLiveRename(t *testing.T) {
	// Speed up the poll for the test.
	origInterval := sessionPollInterval
	sessionPollInterval = 5 * time.Millisecond
	defer func() { sessionPollInterval = origInterval }()

	id := newProcIdentity("leo-old", []string{"--name", "leo-old"})

	// Derive the concrete session names from the identity itself so the fake
	// stays correct regardless of the agent.SessionName convention.
	oldSession := id.SessionName()

	// The fake reports a session as "alive" iff it matches the current live
	// name. It records every queried name and signals each query so the test
	// can advance deterministically.
	var mu sync.Mutex
	liveSession := oldSession
	queries := make(chan string, 64)
	origHas := tmuxHasSession
	tmuxHasSession = func(tmuxPath, session string) bool {
		mu.Lock()
		alive := session == liveSession
		mu.Unlock()
		queries <- session
		return alive
	}
	defer func() { tmuxHasSession = origHas }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan bool, 1)
	go func() { done <- waitForSessionEnd(ctx, "tmux", id, ProcessSpec{}, time.Now()) }()

	// waitForQuery blocks until the watcher polls the given session name (or the
	// test times out), draining intervening polls deterministically.
	waitForQuery := func(want string) {
		t.Helper()
		deadline := time.After(2 * time.Second)
		for {
			select {
			case q := <-queries:
				if q == want {
					return
				}
			case <-deadline:
				t.Fatalf("timed out waiting for a poll of %q", want)
			}
		}
	}

	// Wait until the watcher has polled the OLD session at least once (alive).
	waitForQuery(oldSession)

	// Simulate a live rename: tmux session renamed old->new AND the identity
	// handle swapped (this is what Supervisor.RenameAgent does atomically).
	id.rename("leo-new")
	newSession := id.SessionName()
	mu.Lock()
	liveSession = newSession
	mu.Unlock()

	// The watcher must now poll the NEW name and still see it alive (no false
	// return). Confirm it queries the new session.
	waitForQuery(newSession)

	// The watcher must still be running (not returned false) at this point.
	select {
	case v := <-done:
		t.Fatalf("watcher returned %v before session ended; it should still be polling", v)
	default:
	}

	// Now end the (new) session; the watcher should return false (session ended).
	mu.Lock()
	liveSession = ""
	mu.Unlock()

	select {
	case v := <-done:
		if v != false {
			t.Fatalf("expected false (session ended), got %v", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not return after session ended")
	}
}

// TestWaitForSessionEnd_ContextCancelReturnsTrue verifies the ctx.Done() path
// returns true ("stop"). The tmuxPath is "false" so the best-effort kill-session
// exec returns immediately (non-zero exit, ignored) without depending on a real
// tmux being installed.
func TestWaitForSessionEnd_ContextCancelReturnsTrue(t *testing.T) {
	origInterval := sessionPollInterval
	sessionPollInterval = 5 * time.Millisecond
	defer func() { sessionPollInterval = origInterval }()

	origHas := tmuxHasSession
	tmuxHasSession = func(tmuxPath, session string) bool { return true }
	defer func() { tmuxHasSession = origHas }()

	id := newProcIdentity("leo-x", []string{"--name", "leo-x"})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bool, 1)
	go func() { done <- waitForSessionEnd(ctx, "false", id, ProcessSpec{}, time.Now()) }()
	cancel()
	select {
	case v := <-done:
		if v != true {
			t.Fatalf("expected true on ctx cancel, got %v", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not return on ctx cancel")
	}
}
