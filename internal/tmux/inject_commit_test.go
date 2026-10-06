package tmux

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"
)

// liveCall is a tmux call as a ctx-honoring execCommand saw it: live reports
// whether its ctx was still open, i.e. whether the command would have run.
type liveCall struct {
	sub  string
	last string
	live bool
}

// commitRig fakes tmux for injectPrompt with a ctx-honoring execCommand (a
// command whose ctx is done fails to start, as exec.CommandContext's does)
// and lets a test end the call's ctx at a chosen tmux call. captures is the
// pane each capture-pane returns, by 1-based call number; past its end the
// last entry repeats.
type commitRig struct {
	calls    []liveCall
	captures []string
	cancelAt func(sub string, captureN int) bool
	cancel   context.CancelFunc
}

func (r *commitRig) install(t *testing.T) {
	t.Helper()
	origExec := execCommand
	origAttempts, origPoll := submitConfirmAttempts, submitConfirmPoll
	submitConfirmAttempts, submitConfirmPoll = 10, time.Millisecond
	t.Cleanup(func() {
		execCommand = origExec
		submitConfirmAttempts, submitConfirmPoll = origAttempts, origPoll
	})
	captureN := 0
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		sub := ""
		if len(args) >= 3 {
			sub = args[2]
		}
		if sub == "capture-pane" {
			captureN++
		}
		if r.cancelAt(sub, captureN) {
			r.cancel()
		}
		r.calls = append(r.calls, liveCall{sub: sub, last: args[len(args)-1], live: ctx.Err() == nil})
		switch sub {
		case "list-panes":
			return exec.CommandContext(ctx, "printf", "%s", paneListOutput(testResolvedPane))
		case "capture-pane":
			return exec.CommandContext(ctx, "printf", "%s", r.captures[min(captureN, len(r.captures))-1])
		}
		return exec.CommandContext(ctx, "true")
	}
}

func (r *commitRig) liveCount(sub, last string) int {
	n := 0
	for _, c := range r.calls {
		if c.live && c.sub == sub && (last == "" || c.last == last) {
			n++
		}
	}
	return n
}

// TestInjectPromptSubmitsAPasteWhoseCallEnds proves that once the body has
// been pasted, the call's ctx ending (a task timeout, a caller giving up)
// no longer stops the confirm wait or the Enter: a pasted prompt is always
// submitted, never left sitting in the composer for the next paste to land
// on top of.
func TestInjectPromptSubmitsAPasteWhoseCallEnds(t *testing.T) {
	const longBody = "a body long enough to be confirmed by its tail"
	cases := []struct {
		name     string
		body     string
		captures []string
		cancelAt func(sub string, captureN int) bool
	}{
		{
			name:     "ends as the paste is issued",
			body:     longBody,
			captures: []string{paneWithInput(inputProbe), paneWithInput(""), paneWithInput(longBody)},
			cancelAt: func(sub string, _ int) bool { return sub == "paste-buffer" },
		},
		{
			name: "ends while the paste is being confirmed",
			body: longBody,
			captures: []string{
				paneWithInput(inputProbe), paneWithInput(""), paneWithInput(""), paneWithInput(""), paneWithInput(longBody),
			},
			cancelAt: func(sub string, captureN int) bool { return sub == "capture-pane" && captureN == 3 },
		},
		{
			name:     "ends during a short body's settle beat",
			body:     "ok",
			captures: []string{paneWithInput(inputProbe)},
			cancelAt: func(sub string, _ int) bool { return sub == "paste-buffer" },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			rig := &commitRig{captures: tc.captures, cancelAt: tc.cancelAt, cancel: cancel}
			rig.install(t)

			err := injectPrompt(ctx, "tmux", "leo-agent-foo", tc.body, 1, time.Millisecond)

			if err != nil {
				t.Fatalf("injectPrompt = %v, want nil: the body was pasted, so it must be submitted", err)
			}
			if ctx.Err() == nil {
				t.Fatal("the rig never ended the call's ctx")
			}
			if n := rig.liveCount("paste-buffer", ""); n != 1 {
				t.Fatalf("paste-buffer ran %d times, want 1: %+v", n, rig.calls)
			}
			if n := rig.liveCount("send-keys", "Enter"); n != 1 {
				t.Fatalf("Enter ran %d times, want 1: %+v", n, rig.calls)
			}
		})
	}
}

// TestInjectPromptPastesNothingForACallEndedBeforeThePaste proves the
// commit point is the paste itself: a call whose ctx ends before it stages
// nothing, pastes nothing and submits nothing.
func TestInjectPromptPastesNothingForACallEndedBeforeThePaste(t *testing.T) {
	const body = "a body long enough to be confirmed by its tail"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rig := &commitRig{
		captures: []string{paneWithInput(inputProbe), paneWithInput("")},
		// Capture 2 is the baseline, the last step before the paste.
		cancelAt: func(sub string, captureN int) bool { return sub == "capture-pane" && captureN == 2 },
		cancel:   cancel,
	}
	rig.install(t)

	err := injectPrompt(ctx, "tmux", "leo-agent-foo", body, 1, time.Millisecond)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("injectPrompt = %v, want context.Canceled", err)
	}
	for _, sub := range []string{"load-buffer", "paste-buffer"} {
		if n := rig.liveCount(sub, ""); n != 0 {
			t.Fatalf("%s ran %d times for an ended call: %+v", sub, n, rig.calls)
		}
	}
	if n := rig.liveCount("send-keys", "Enter"); n != 0 {
		t.Fatalf("Enter ran %d times for an ended call: %+v", n, rig.calls)
	}
}

// TestInjectPromptBoundsASubmitAgainstAWedgedTmux proves the submit that
// outlives its call is still bounded: a tmux command that never returns is
// given up at submitCommitTimeout, so the session's paste lock is never
// held forever.
func TestInjectPromptBoundsASubmitAgainstAWedgedTmux(t *testing.T) {
	origTimeout := submitCommitTimeout
	submitCommitTimeout = 50 * time.Millisecond
	t.Cleanup(func() { submitCommitTimeout = origTimeout })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rig := &commitRig{
		captures: []string{paneWithInput(inputProbe)},
		cancelAt: func(sub string, _ int) bool { return sub == "paste-buffer" },
		cancel:   cancel,
	}
	rig.install(t)
	installed := execCommand
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		cmd := installed(ctx, name, args...)
		if len(args) >= 3 && args[2] == "paste-buffer" {
			return exec.CommandContext(ctx, "sleep", "30")
		}
		return cmd
	}

	start := time.Now()
	err := injectPrompt(ctx, "tmux", "leo-agent-foo", "ok", 1, time.Millisecond)

	if err == nil {
		t.Fatal("injectPrompt = nil for a paste that never returned")
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("injectPrompt took %v against a wedged tmux, want it bounded by submitCommitTimeout", took)
	}
}
