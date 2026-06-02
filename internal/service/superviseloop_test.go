package service

import (
	"context"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// hasArg reports whether args contains target.
func hasArg(args []string, target string) bool {
	for _, a := range args {
		if a == target {
			return true
		}
	}
	return false
}

func TestRunSuperviseLoopExitsOnCancel(t *testing.T) {
	var calls atomic.Int32
	orig := loopExecCommand
	defer func() { loopExecCommand = orig }()
	loopExecCommand = func(name string, args ...string) *exec.Cmd {
		calls.Add(1)
		// Make has-session always succeed (session "alive") so we never break out
		// of the inner has-session loop on our own.
		if hasArg(args, "has-session") {
			return exec.Command("true")
		}
		return exec.Command("true")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runSuperviseLoop(ctx, "tmux", LoopSpec{
			Name: "x", SessionName: "leo-session-x", Workdir: "/tmp",
			ShellCmd: func(bool) string { return "echo hi" },
		})
		close(done)
	}()
	// Give the loop a moment to enter its inner has-session wait.
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("loop did not exit on cancel; calls=%d", calls.Load())
	}
	if calls.Load() == 0 {
		t.Fatalf("expected at least one loopExecCommand call")
	}
}

func TestRunSuperviseLoopRestartsAndCallsOnSessionEnd(t *testing.T) {
	var (
		mu          sync.Mutex
		endCallback []int
	)
	onEnd := func(n int) { mu.Lock(); endCallback = append(endCallback, n); mu.Unlock() }

	orig := loopExecCommand
	defer func() { loopExecCommand = orig }()
	// First has-session call after new-session returns exit-1 so the inner loop
	// breaks and triggers OnSessionEnd. We let new-session always succeed.
	loopExecCommand = func(name string, args ...string) *exec.Cmd {
		if hasArg(args, "has-session") {
			return exec.Command("false") // session "gone"
		}
		return exec.Command("true")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runSuperviseLoop(ctx, "tmux", LoopSpec{
			Name: "x", SessionName: "leo-session-x", Workdir: "/tmp",
			ShellCmd: func(bool) string { return "echo hi" }, OnSessionEnd: onEnd,
		})
		close(done)
	}()
	// Let it cycle a couple of times.
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("loop did not exit on cancel")
	}
	mu.Lock()
	n := len(endCallback)
	mu.Unlock()
	if n == 0 {
		t.Fatalf("expected at least one OnSessionEnd callback")
	}
}

// TestRunSuperviseLoopStripsResumeOnQuickExit verifies poisoned-session
// recovery: when claude exits almost immediately (suggesting a stale --resume
// id), the loop must respawn WITHOUT resume and clear the stored session,
// rather than crash-looping on the same poisoned id forever.
func TestRunSuperviseLoopStripsResumeOnQuickExit(t *testing.T) {
	var mu sync.Mutex
	var resumeArgs []bool
	var quickExits int

	orig := loopExecCommand
	defer func() { loopExecCommand = orig }()
	loopExecCommand = func(name string, args ...string) *exec.Cmd {
		if hasArg(args, "has-session") {
			return exec.Command("false") // session "gone" immediately → quick exit
		}
		return exec.Command("true")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runSuperviseLoop(ctx, "tmux", LoopSpec{
			Name: "x", SessionName: "leo-session-x", Workdir: "/tmp",
			ShellCmd: func(resume bool) string {
				mu.Lock()
				resumeArgs = append(resumeArgs, resume)
				mu.Unlock()
				return "echo hi"
			},
			OnQuickExit: func() { mu.Lock(); quickExits++; mu.Unlock() },
		})
		close(done)
	}()
	// Backoff between restarts is 1s; wait long enough to see the respawn.
	time.Sleep(1400 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("loop did not exit on cancel")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(resumeArgs) < 2 {
		t.Fatalf("expected at least 2 spawns, got %d (%v)", len(resumeArgs), resumeArgs)
	}
	if resumeArgs[0] != true {
		t.Fatalf("first spawn should attempt resume=true, got %v", resumeArgs[0])
	}
	if resumeArgs[1] != false {
		t.Fatalf("after a quick exit the respawn must drop resume (want false), got %v", resumeArgs[1])
	}
	if quickExits < 1 {
		t.Fatalf("expected OnQuickExit to fire at least once, got %d", quickExits)
	}
}
