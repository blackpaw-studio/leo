package tmux

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// Pastes into one session never interleave: every paste stages through the
// session's one buffer, and each probe ends with a Ctrl-U that would wipe
// another paste's text from the composer. So each holds the session from
// its probe until its Enter.
func TestConcurrentPastesIntoASessionDoNotInterleave(t *testing.T) {
	origPoll := submitConfirmPoll
	submitConfirmPoll = time.Millisecond
	defer func() { submitConfirmPoll = origPoll }()
	var (
		mu  sync.Mutex
		seq []string
	)
	orig := execCommand
	defer func() { execCommand = orig }()
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		mu.Lock()
		seq = append(seq, strings.Join(args[2:], " "))
		mu.Unlock()
		if isListPanes(args) {
			return exec.Command("printf", "%s", paneListOutput(testResolvedPane))
		}
		if args[2] == "capture-pane" {
			return exec.Command("printf", "%s", paneWithInput(inputProbe))
		}
		return exec.Command("true")
	}

	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for _, body := range []string{"one", "two", "three"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- injectPrompt(context.Background(), "tmux", "leo-agent-foo", body, 5, time.Millisecond)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	isPasting := false
	for _, c := range seq {
		switch {
		case strings.HasPrefix(c, "load-buffer"):
			if isPasting {
				t.Fatalf("a paste staged into the session's buffer before the one before it was submitted:\n%s", strings.Join(seq, "\n"))
			}
			isPasting = true
		case strings.HasPrefix(c, "send-keys") && strings.HasSuffix(c, " Enter"):
			isPasting = false
		case strings.HasPrefix(c, "send-keys") && isPasting:
			t.Fatalf("keys %q reached the session between a paste and its submit:\n%s", c, strings.Join(seq, "\n"))
		}
	}
}

// The lock is per session (pastes into other sessions go ahead), and a
// paste waiting for it gives up with its context.
func TestASessionsPasteLockIsItsOwn(t *testing.T) {
	release, err := pasteLocks.acquire(context.Background(), "leo-a")
	if err != nil {
		t.Fatal(err)
	}
	other, err := pasteLocks.acquire(context.Background(), "leo-b")
	if err != nil {
		t.Fatalf("another session's paste waited: %v", err)
	}
	other()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := pasteLocks.acquire(ctx, "leo-a"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a second paste into a held session: err=%v, want it to wait out its context", err)
	}
	release()
	again, err := pasteLocks.acquire(context.Background(), "leo-a")
	if err != nil {
		t.Fatalf("after the release: %v", err)
	}
	again()
	if n := pasteLocks.size(); n != 0 {
		t.Fatalf("%d session locks kept after every paste ended", n)
	}
}
