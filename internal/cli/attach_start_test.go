package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// stubAgentStart replaces agentStartFn for the duration of the test, tracking
// whether it fired and letting the test control the outcome.
func stubAgentStart(t *testing.T, err error) *bool {
	t.Helper()
	called := false
	old := agentStartFn
	agentStartFn = func(_ context.Context, _, _ string) error {
		called = true
		return err
	}
	t.Cleanup(func() { agentStartFn = old })
	return &called
}

// stubAgentSessionReady replaces agentSessionReadyFn so waitForAgentSession
// resolves immediately without touching a real tmux binary.
func stubAgentSessionReady(t *testing.T, ready bool) {
	t.Helper()
	old := agentSessionReadyFn
	agentSessionReadyFn = func(string) bool { return ready }
	t.Cleanup(func() { agentSessionReadyFn = old })
}

// TestEnsureAgentRunningLiveAgentIsNoOp verifies a live agent (stopped ==
// false) never prompts and never calls AgentStart — the fast path used on
// every ordinary attach.
func TestEnsureAgentRunningLiveAgentIsNoOp(t *testing.T) {
	oldTTY := agentIsTTY
	agentIsTTY = func() bool { t.Fatal("agentIsTTY should not be consulted for a live agent"); return false }
	t.Cleanup(func() { agentIsTTY = oldTTY })
	called := stubAgentStart(t, nil)

	ok, err := ensureAgentRunning(context.Background(), &cobra.Command{}, t.TempDir(), "scratch", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true for a live agent")
	}
	if *called {
		t.Fatal("AgentStart should not fire for a live agent")
	}
}

// TestEnsureAgentRunningDormantNonTTYErrors verifies a dormant agent off a
// TTY fails fast with the exact command to run, instead of blocking on an
// unanswerable prompt.
func TestEnsureAgentRunningDormantNonTTYErrors(t *testing.T) {
	oldTTY := agentIsTTY
	agentIsTTY = func() bool { return false }
	t.Cleanup(func() { agentIsTTY = oldTTY })
	called := stubAgentStart(t, nil)

	ok, err := ensureAgentRunning(context.Background(), &cobra.Command{}, t.TempDir(), "scratch", true)
	if err == nil {
		t.Fatal("expected an error for a dormant agent off a TTY")
	}
	if !strings.Contains(err.Error(), "leo agent start") {
		t.Errorf("error %q should mention 'leo agent start'", err.Error())
	}
	if ok {
		t.Fatal("expected ok=false")
	}
	if *called {
		t.Fatal("AgentStart should not fire without confirmation")
	}
}

// TestEnsureAgentRunningDormantTTYPrompt covers both the accept and decline
// branches of the interactive Y/n prompt.
func TestEnsureAgentRunningDormantTTYPrompt(t *testing.T) {
	cases := []struct {
		name       string
		input      string
		wantCalled bool
		wantOK     bool
	}{
		{"enter defaults to yes", "\n", true, true},
		{"y answers yes", "y\n", true, true},
		{"uppercase Y answers yes", "Y\n", true, true},
		{"n declines", "n\n", false, false},
		{"no declines", "no\n", false, false},
		{"eof declines", "", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			oldTTY := agentIsTTY
			agentIsTTY = func() bool { return true }
			t.Cleanup(func() { agentIsTTY = oldTTY })

			oldIn := agentStdin
			agentStdin = strings.NewReader(tc.input)
			t.Cleanup(func() { agentStdin = oldIn })
			withStubStdio(t)

			called := stubAgentStart(t, nil)
			stubAgentSessionReady(t, true)

			ok, err := ensureAgentRunning(context.Background(), &cobra.Command{}, t.TempDir(), "scratch", true)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ok != tc.wantOK {
				t.Errorf("ok = %v, want %v", ok, tc.wantOK)
			}
			if *called != tc.wantCalled {
				t.Errorf("AgentStart called = %v, want %v", *called, tc.wantCalled)
			}
		})
	}
}

// TestEnsureAgentRunningPromptsExactText verifies the prompt matches the
// agreed copy exactly.
func TestEnsureAgentRunningPromptsExactText(t *testing.T) {
	oldTTY := agentIsTTY
	agentIsTTY = func() bool { return true }
	t.Cleanup(func() { agentIsTTY = oldTTY })

	oldIn := agentStdin
	agentStdin = strings.NewReader("n\n")
	t.Cleanup(func() { agentStdin = oldIn })
	_, errBuf := withStubStdio(t)
	stubAgentStart(t, nil)

	if _, err := ensureAgentRunning(context.Background(), &cobra.Command{}, t.TempDir(), "foo", true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `agent "foo" is stopped. Start it? [Y/n] `
	if errBuf.String() != want {
		t.Errorf("prompt = %q, want %q", errBuf.String(), want)
	}
}

// TestEnsureAgentRunningGatesBeforePrompt verifies a denied leo_stop_agent
// refuses before the Y/n prompt is even written — a user who cannot start
// agents shouldn't be walked through a prompt only to be refused after
// answering yes.
func TestEnsureAgentRunningGatesBeforePrompt(t *testing.T) {
	t.Setenv("LEO_PERMISSIONS", `{"deny_tools":["leo_stop_agent"]}`)
	oldTTY := agentIsTTY
	agentIsTTY = func() bool { t.Fatal("agentIsTTY should not be consulted once the gate refuses"); return false }
	t.Cleanup(func() { agentIsTTY = oldTTY })
	_, errBuf := withStubStdio(t)
	called := stubAgentStart(t, nil)

	ok, err := ensureAgentRunning(context.Background(), cmdFor("attach"), t.TempDir(), "scratch", true)
	if err == nil {
		t.Fatal("expected the gate to refuse")
	}
	if ok {
		t.Fatal("expected ok=false")
	}
	if *called {
		t.Fatal("AgentStart should not fire when the gate refuses")
	}
	if errBuf.String() != "" {
		t.Errorf("expected no prompt written before the gate refuses, got %q", errBuf.String())
	}
}

// TestWaitForAgentSessionRespectsCancelledContext verifies a cancelled ctx
// cuts the wait short instead of spinning out the full timeout.
func TestWaitForAgentSessionRespectsCancelledContext(t *testing.T) {
	stubAgentSessionReady(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	err := waitForAgentSession(ctx, "scratch")
	elapsed := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if elapsed > time.Second {
		t.Fatalf("waitForAgentSession took %s, want it to return promptly on a cancelled ctx", elapsed)
	}
}

// TestWaitForAgentSessionTimesOut verifies the bounded poll surfaces a clear
// error naming the agent once agentStartTimeout elapses, without waiting the
// real production timeout — agentStartTimeout/agentStartPollInterval are
// package vars for exactly this.
func TestWaitForAgentSessionTimesOut(t *testing.T) {
	oldTimeout, oldInterval := agentStartTimeout, agentStartPollInterval
	agentStartTimeout = 30 * time.Millisecond
	agentStartPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { agentStartTimeout, agentStartPollInterval = oldTimeout, oldInterval })
	stubAgentSessionReady(t, false)

	err := waitForAgentSession(context.Background(), "scratch")
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if !strings.Contains(err.Error(), "scratch") || !strings.Contains(err.Error(), "did not become ready") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestEnsureAgentRunningForPickerGatesViaLabel verifies the picker's entry
// point gates through a caller-supplied label (gateToolFor) instead of a
// cobra command — the picker has no command to name — while sharing the
// same prompt/start/wait behavior as the cmd-based door.
func TestEnsureAgentRunningForPickerGatesViaLabel(t *testing.T) {
	t.Run("denied permission refuses before prompting", func(t *testing.T) {
		t.Setenv("LEO_PERMISSIONS", `{"deny_tools":["leo_stop_agent"]}`)
		_, errBuf := withStubStdio(t)
		called := stubAgentStart(t, nil)

		ok, err := ensureAgentRunningForPicker(context.Background(), "leo attach: start agent", t.TempDir(), "scratch", true)
		if err == nil {
			t.Fatal("expected the gate to refuse")
		}
		if ok {
			t.Fatal("expected ok=false")
		}
		if *called {
			t.Fatal("AgentStart should not fire when the gate refuses")
		}
		if errBuf.String() != "" {
			t.Errorf("expected no prompt before the gate refuses, got %q", errBuf.String())
		}
	})

	t.Run("allowed permission proceeds to start", func(t *testing.T) {
		oldTTY := agentIsTTY
		agentIsTTY = func() bool { return true }
		t.Cleanup(func() { agentIsTTY = oldTTY })
		oldIn := agentStdin
		agentStdin = strings.NewReader("y\n")
		t.Cleanup(func() { agentStdin = oldIn })
		withStubStdio(t)
		called := stubAgentStart(t, nil)
		stubAgentSessionReady(t, true)

		ok, err := ensureAgentRunningForPicker(context.Background(), "leo attach: start agent", t.TempDir(), "scratch", true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !ok {
			t.Fatal("expected ok=true")
		}
		if !*called {
			t.Fatal("expected AgentStart to fire")
		}
	})
}
