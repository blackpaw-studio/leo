package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/drain"
)

// gracefulClock advances by d on every After and fires at once, so each
// poll is one poll interval of fake time and nothing really waits.
type gracefulClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *gracefulClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *gracefulClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	c.now = c.now.Add(d)
	now := c.now
	c.mu.Unlock()
	ch := make(chan time.Time, 1)
	ch <- now
	return ch
}

// fakeDrainClient answers Status from the fake clock: the drain is done
// once the clock reaches doneAt (never when zero).
type fakeDrainClient struct {
	mu      sync.Mutex
	clock   *gracefulClock
	log     *[]string
	busy    []drain.Probe
	busyErr error
	// beginErr fails Begin.
	beginErr  error
	doneAt    time.Time
	commitErr error
	// onStatus runs on each Status call (e.g. to press Ctrl-C).
	onStatus    func(calls int)
	statusCalls int
	cancelCtx   []error
}

func (f *fakeDrainClient) record(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	*f.log = append(*f.log, s)
}

func (f *fakeDrainClient) status() drain.Status {
	st := drain.Status{ID: "d1", Phase: drain.PhaseDraining, TimeoutMinutes: 1}
	done := !f.doneAt.IsZero() && !f.clock.Now().Before(f.doneAt)
	for _, p := range f.busy {
		state := drain.StateNotified
		if done {
			state = drain.StateDrained
		}
		st.Members = append(st.Members, drain.MemberStatus{Name: p.Name, State: state})
	}
	st.Done = done
	return st
}

func (f *fakeDrainClient) Busy(context.Context) ([]drain.Probe, error) {
	f.record("busy")
	return f.busy, f.busyErr
}

func (f *fakeDrainClient) Begin(_ context.Context, timeout time.Duration) (drain.Status, error) {
	f.record(fmt.Sprintf("begin %s", timeout))
	if f.beginErr != nil {
		return drain.Status{}, f.beginErr
	}
	return f.status(), nil
}

func (f *fakeDrainClient) Status(context.Context) (drain.Status, error) {
	f.mu.Lock()
	f.statusCalls++
	calls := f.statusCalls
	f.mu.Unlock()
	if f.onStatus != nil {
		f.onStatus(calls)
	}
	return f.status(), nil
}

func (f *fakeDrainClient) Commit(_ context.Context, force bool) error {
	f.record(fmt.Sprintf("commit force=%v", force))
	return f.commitErr
}

func (f *fakeDrainClient) Cancel(ctx context.Context) error {
	f.record("cancel")
	f.mu.Lock()
	f.cancelCtx = append(f.cancelCtx, ctx.Err())
	f.mu.Unlock()
	return nil
}

type gracefulFixture struct {
	clock      *gracefulClock
	drain      *fakeDrainClient
	log        []string
	answers    []string
	asked      []string
	restartErr error
	out        bytes.Buffer
	deps       gracefulDeps
}

func newGracefulFixture(interactive bool, busy ...string) *gracefulFixture {
	f := &gracefulFixture{clock: &gracefulClock{now: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)}}
	f.drain = &fakeDrainClient{clock: f.clock, log: &f.log}
	for _, name := range busy {
		f.drain.busy = append(f.drain.busy, drain.Probe{Name: name, Exists: true, Busy: true})
	}
	f.deps = gracefulDeps{
		drain: f.drain,
		restart: func(context.Context) error {
			f.drain.record("restart")
			return f.restartErr
		},
		isInteractive: func() bool { return interactive },
		ask: func(ctx context.Context, question string) (string, error) {
			f.asked = append(f.asked, question)
			if len(f.answers) == 0 {
				return "", errors.New("unexpected question: " + question)
			}
			answer := f.answers[0]
			f.answers = f.answers[1:]
			return answer, nil
		},
		clock:        f.clock,
		out:          &f.out,
		pollInterval: 2 * time.Second,
	}
	return f
}

func (f *gracefulFixture) run(t *testing.T, opts gracefulOptions) error {
	t.Helper()
	if opts.drainTimeout == 0 {
		opts.drainTimeout = 10 * time.Second
	}
	return gracefulRestart(context.Background(), opts, f.deps)
}

func (f *gracefulFixture) wantLog(t *testing.T, want ...string) {
	t.Helper()
	if !reflect.DeepEqual(f.log, want) {
		t.Fatalf("calls = %q, want %q\noutput:\n%s", f.log, want, f.out.String())
	}
}

func TestGracefulRestartNoBusyAgentsRestartsWithoutAsking(t *testing.T) {
	f := newGracefulFixture(true)
	if err := f.run(t, gracefulOptions{}); err != nil {
		t.Fatal(err)
	}
	f.wantLog(t, "busy", "restart")
	if len(f.asked) != 0 {
		t.Fatalf("asked %q", f.asked)
	}
}

func TestGracefulRestartNoGracefulRestartsImmediately(t *testing.T) {
	f := newGracefulFixture(true, "a")
	if err := f.run(t, gracefulOptions{noGraceful: true}); err != nil {
		t.Fatal(err)
	}
	f.wantLog(t, "busy", "restart")
}

func TestGracefulRestartGracefulDrainsWithoutAsking(t *testing.T) {
	f := newGracefulFixture(true, "a", "b")
	f.drain.doneAt = f.clock.Now().Add(4 * time.Second)
	if err := f.run(t, gracefulOptions{graceful: true}); err != nil {
		t.Fatal(err)
	}
	f.wantLog(t, "busy", "begin 10s", "commit force=false", "restart")
	if len(f.asked) != 0 {
		t.Fatalf("asked %q", f.asked)
	}
	if out := f.out.String(); !strings.Contains(out, "a: drained") || !strings.Contains(out, "b: drained") {
		t.Fatalf("transitions not reported:\n%s", out)
	}
}

func TestGracefulRestartInteractivePrompt(t *testing.T) {
	for _, tc := range []struct {
		answer  string
		wantLog []string
	}{
		{"y", []string{"busy", "begin 10s", "commit force=false", "restart"}},
		{"", []string{"busy", "begin 10s", "commit force=false", "restart"}},
		{"n", []string{"busy", "restart"}},
	} {
		t.Run(fmt.Sprintf("answer %q", tc.answer), func(t *testing.T) {
			f := newGracefulFixture(true, "a", "b", "c")
			f.drain.doneAt = f.clock.Now()
			f.answers = []string{tc.answer}
			if err := f.run(t, gracefulOptions{}); err != nil {
				t.Fatal(err)
			}
			f.wantLog(t, tc.wantLog...)
			want := "3 agents are working (a, b, c). Graceful restart? [Y/n] "
			if len(f.asked) != 1 || f.asked[0] != want {
				t.Fatalf("asked %q, want %q", f.asked, want)
			}
		})
	}
}

func TestGracefulRestartInteractivePromptRepeatsOnNonsense(t *testing.T) {
	f := newGracefulFixture(true, "a")
	f.drain.doneAt = f.clock.Now()
	f.answers = []string{"maybe", "Y"}
	if err := f.run(t, gracefulOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(f.asked) != 2 || f.asked[0] != "1 agent is working (a). Graceful restart? [Y/n] " {
		t.Fatalf("asked %q", f.asked)
	}
	f.wantLog(t, "busy", "begin 10s", "commit force=false", "restart")
}

func TestGracefulRestartNonInteractiveWithoutFlagRestartsImmediately(t *testing.T) {
	f := newGracefulFixture(false, "a")
	if err := f.run(t, gracefulOptions{}); err != nil {
		t.Fatal(err)
	}
	f.wantLog(t, "busy", "restart")
}

func TestGracefulRestartOldDaemonWarnsAndRestarts(t *testing.T) {
	f := newGracefulFixture(true)
	f.drain.busyErr = daemon.ErrDrainUnsupported
	if err := f.run(t, gracefulOptions{graceful: true}); err != nil {
		t.Fatal(err)
	}
	f.wantLog(t, "busy", "restart")
	if !strings.Contains(f.out.String(), "predates graceful restart") {
		t.Fatalf("no warning:\n%s", f.out.String())
	}
}

func TestGracefulRestartTimeoutRestartAnyway(t *testing.T) {
	f := newGracefulFixture(true, "a", "b")
	f.answers = []string{"r"}
	if err := f.run(t, gracefulOptions{graceful: true}); err != nil {
		t.Fatal(err)
	}
	f.wantLog(t, "busy", "begin 10s", "commit force=true", "restart")
	if len(f.asked) != 1 || !strings.Contains(f.asked[0], "[r]estart anyway / [w]ait another 10s / [a]bort") {
		t.Fatalf("asked %q", f.asked)
	}
	if !strings.Contains(f.out.String(), "a, b") {
		t.Fatalf("stragglers not listed:\n%s", f.out.String())
	}
}

func TestGracefulRestartTimeoutWaitAgainThenDone(t *testing.T) {
	f := newGracefulFixture(true, "a")
	f.drain.doneAt = f.clock.Now().Add(14 * time.Second)
	f.answers = []string{"w"}
	if err := f.run(t, gracefulOptions{graceful: true}); err != nil {
		t.Fatal(err)
	}
	f.wantLog(t, "busy", "begin 10s", "commit force=false", "restart")
}

func TestGracefulRestartTimeoutAbort(t *testing.T) {
	f := newGracefulFixture(true, "a")
	f.answers = []string{"a"}
	err := f.run(t, gracefulOptions{graceful: true})
	if !errors.Is(err, errGracefulAborted) {
		t.Fatalf("err = %v, want errGracefulAborted", err)
	}
	f.wantLog(t, "busy", "begin 10s", "cancel")
}

func TestGracefulRestartTimeoutNonInteractive(t *testing.T) {
	t.Run("without force aborts", func(t *testing.T) {
		f := newGracefulFixture(false, "a")
		err := f.run(t, gracefulOptions{graceful: true})
		if err == nil || !strings.Contains(err.Error(), "--force") {
			t.Fatalf("err = %v, want a timeout naming --force", err)
		}
		f.wantLog(t, "busy", "begin 10s", "cancel")
	})
	t.Run("with force restarts", func(t *testing.T) {
		f := newGracefulFixture(false, "a")
		if err := f.run(t, gracefulOptions{graceful: true, force: true}); err != nil {
			t.Fatal(err)
		}
		f.wantLog(t, "busy", "begin 10s", "commit force=true", "restart")
	})
	t.Run("force skips the interactive question", func(t *testing.T) {
		f := newGracefulFixture(true, "a")
		if err := f.run(t, gracefulOptions{graceful: true, force: true}); err != nil {
			t.Fatal(err)
		}
		f.wantLog(t, "busy", "begin 10s", "commit force=true", "restart")
		if len(f.asked) != 0 {
			t.Fatalf("asked %q", f.asked)
		}
	})
}

func TestGracefulRestartCtrlCCancelsOnALiveContext(t *testing.T) {
	f := newGracefulFixture(true, "a")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.drain.onStatus = func(int) { cancel() }
	err := gracefulRestart(ctx, gracefulOptions{graceful: true, drainTimeout: time.Minute}, f.deps)
	if err == nil {
		t.Fatal("interrupted restart returned nil")
	}
	f.wantLog(t, "busy", "begin 1m0s", "cancel")
	if f.drain.cancelCtx[0] != nil {
		t.Fatalf("cancel sent on a dead context: %v", f.drain.cancelCtx[0])
	}
}

func TestGracefulRestartCtrlCDuringTimeoutQuestionCancels(t *testing.T) {
	f := newGracefulFixture(true, "a")
	f.deps.ask = func(ctx context.Context, _ string) (string, error) { return "", context.Canceled }
	err := f.run(t, gracefulOptions{graceful: true})
	if err == nil {
		t.Fatal("interrupted question returned nil")
	}
	f.wantLog(t, "busy", "begin 10s", "cancel")
}

func TestGracefulRestartFailedRestartCancelsTheDrain(t *testing.T) {
	f := newGracefulFixture(true, "a")
	f.drain.doneAt = f.clock.Now()
	f.restartErr = errors.New("kickstart failed")
	err := f.run(t, gracefulOptions{graceful: true})
	if !errors.Is(err, f.restartErr) {
		t.Fatalf("err = %v, want the restart error", err)
	}
	f.wantLog(t, "busy", "begin 10s", "commit force=false", "restart", "cancel")
	if f.drain.cancelCtx[0] != nil {
		t.Fatalf("cancel sent on a dead context: %v", f.drain.cancelCtx[0])
	}
}

func TestGracefulRestartNotReadyIsNotAFailedRestart(t *testing.T) {
	f := newGracefulFixture(true, "a")
	f.drain.doneAt = f.clock.Now()
	f.restartErr = fmt.Errorf("%w: timed out", errDaemonNotReady)
	err := f.run(t, gracefulOptions{graceful: true})
	if !errors.Is(err, errDaemonNotReady) {
		t.Fatalf("err = %v, want errDaemonNotReady", err)
	}
	f.wantLog(t, "busy", "begin 10s", "commit force=false", "restart")
}

func TestGracefulRestartRebusyAtCommitKeepsWaiting(t *testing.T) {
	f := newGracefulFixture(true, "a")
	f.drain.doneAt = f.clock.Now()
	commits := 0
	f.deps.drain = commitOnce{f.drain, &commits}
	if err := f.run(t, gracefulOptions{graceful: true}); err != nil {
		t.Fatal(err)
	}
	f.wantLog(t, "busy", "begin 10s", "commit force=false", "commit force=false", "restart")
}

// commitOnce refuses the first commit as not drained.
type commitOnce struct {
	*fakeDrainClient
	n *int
}

func (c commitOnce) Commit(ctx context.Context, force bool) error {
	*c.n++
	err := c.fakeDrainClient.Commit(ctx, force)
	if *c.n == 1 {
		return drain.ErrNotDrained
	}
	return err
}

// The lease stays renewed while the timeout question waits on a human.
func TestGracefulRestartHeartbeatPollsWhileAsking(t *testing.T) {
	f := newGracefulFixture(true, "a")
	polled := make(chan struct{}, 64)
	f.drain.onStatus = func(int) {
		select {
		case polled <- struct{}{}:
		default:
		}
	}
	f.deps.ask = func(ctx context.Context, _ string) (string, error) {
		for len(polled) > 0 { // polls from before the question
			<-polled
		}
		guard := time.NewTimer(5 * time.Second)
		defer guard.Stop()
		for i := 0; i < 2; i++ {
			select {
			case <-polled:
			case <-guard.C:
				return "", errors.New("no heartbeat while the question was open")
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		return "a", nil
	}
	if err := f.run(t, gracefulOptions{graceful: true}); !errors.Is(err, errGracefulAborted) {
		t.Fatalf("err = %v, want errGracefulAborted", err)
	}
}

func TestGracefulOptionsValidate(t *testing.T) {
	for _, tc := range []struct {
		timeout time.Duration
		ok      bool
	}{
		{10 * time.Minute, true},
		{0, false},
		{-time.Second, false},
		{25 * time.Hour, false},
	} {
		err := gracefulOptions{drainTimeout: tc.timeout}.validate()
		if (err == nil) != tc.ok {
			t.Errorf("validate(%v) = %v, want ok=%v", tc.timeout, err, tc.ok)
		}
	}
}

func TestServiceRestartRejectsBothGracefulFlags(t *testing.T) {
	cmd := newServiceRestartCmd()
	cmd.SetArgs([]string{"--graceful", "--no-graceful"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "graceful") {
		t.Fatalf("err = %v, want a mutually-exclusive flag error", err)
	}
}

func TestServiceRestartRejectsNonPositiveDrainTimeout(t *testing.T) {
	cmd := newServiceRestartCmd()
	cmd.SetArgs([]string{"--drain-timeout", "0s"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--drain-timeout") {
		t.Fatalf("err = %v, want a --drain-timeout error", err)
	}
}

// An unattended update restarts only when a graceful flag was given
// explicitly; with a terminal the existing "Restart it now?" decides.
func TestUpdateRestartConsent(t *testing.T) {
	for _, tc := range []struct {
		name        string
		opts        gracefulOptions
		interactive bool
		confirm     bool
		want        bool
	}{
		{"unattended without a flag", gracefulOptions{}, false, true, false},
		{"unattended with --graceful", gracefulOptions{graceful: true}, false, false, true},
		{"unattended with --no-graceful", gracefulOptions{noGraceful: true}, false, false, true},
		{"interactive yes", gracefulOptions{graceful: true}, true, true, true},
		{"interactive no despite flag", gracefulOptions{graceful: true}, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			asked := false
			got := restartConsent(tc.opts, tc.interactive, func() bool { asked = true; return tc.confirm })
			if got != tc.want {
				t.Fatalf("restartConsent = %v, want %v", got, tc.want)
			}
			if asked != tc.interactive {
				t.Fatalf("asked = %v, want %v", asked, tc.interactive)
			}
		})
	}
}

// A busy check that fails for a reason other than "no daemon" or "old
// daemon" must not silently skip a drain the operator asked for.
func TestGracefulRestartBusyCheckFailure(t *testing.T) {
	probeErr := errors.New("decoding GET /drain/busy response (status 500)")
	for _, tc := range []struct {
		name        string
		err         error
		opts        gracefulOptions
		interactive bool
		answers     []string
		wantLog     []string
		wantErr     bool
	}{
		{"unreachable daemon restarts", fmt.Errorf("%w: refused", daemon.ErrDaemonUnreachable), gracefulOptions{graceful: true}, false, nil, []string{"busy", "restart"}, false},
		{"--graceful aborts", probeErr, gracefulOptions{graceful: true}, false, nil, []string{"busy"}, true},
		{"--graceful interactive aborts", probeErr, gracefulOptions{graceful: true}, true, nil, []string{"busy"}, true},
		{"--graceful --force restarts", probeErr, gracefulOptions{graceful: true, force: true}, false, nil, []string{"busy", "restart"}, false},
		{"--no-graceful restarts", probeErr, gracefulOptions{noGraceful: true}, true, nil, []string{"busy", "restart"}, false},
		{"unattended without a flag restarts as before", probeErr, gracefulOptions{}, false, nil, []string{"busy", "restart"}, false},
		{"interactive without a flag declines by default", probeErr, gracefulOptions{}, true, []string{""}, []string{"busy"}, true},
		{"interactive without a flag restarts on yes", probeErr, gracefulOptions{}, true, []string{"y"}, []string{"busy", "restart"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGracefulFixture(tc.interactive, "a")
			f.drain.busyErr = tc.err
			f.answers = tc.answers
			err := f.run(t, tc.opts)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v\noutput:\n%s", err, tc.wantErr, f.out.String())
			}
			if err != nil && !strings.Contains(err.Error(), "busy") {
				t.Fatalf("err = %v, want it to say the busy check failed", err)
			}
			f.wantLog(t, tc.wantLog...)
			if len(f.answers) != 0 {
				t.Fatalf("unanswered questions left: %q", f.answers)
			}
		})
	}
}

func TestWorkingSummaryListsAgentsAndDispatches(t *testing.T) {
	agent := func(name string) drain.Probe { return drain.Probe{Name: name, Exists: true, Busy: true} }
	dispatch := func(name string) drain.Probe {
		return drain.Probe{Name: name, Exists: true, Busy: true, Kind: drain.KindDispatch}
	}
	for _, tc := range []struct {
		busy []drain.Probe
		want string
	}{
		{[]drain.Probe{agent("a"), agent("b"), dispatch("fix-x")}, "2 agents are working (a, b) and 1 dispatch is running (fix-x)"},
		{[]drain.Probe{agent("a"), dispatch("fix-x"), dispatch("d-1")}, "1 agent is working (a) and 2 dispatches are running (fix-x, d-1)"},
		{[]drain.Probe{agent("a")}, "1 agent is working (a)"},
		{[]drain.Probe{dispatch("fix-x")}, "1 dispatch is running (fix-x)"},
	} {
		if got := workingSummary(tc.busy); got != tc.want {
			t.Errorf("workingSummary = %q, want %q", got, tc.want)
		}
	}
}

// Running dispatches alone are no reason to ask: the restarted daemon
// adopts them. The operator is told so, and the restart goes ahead.
func TestGracefulRestartWithOnlyDispatchesRunningRestartsWithoutAsking(t *testing.T) {
	f := newGracefulFixture(true)
	f.drain.busy = []drain.Probe{
		{Name: "fix-x", Exists: true, Busy: true, Kind: drain.KindDispatch},
		{Name: "d-1", Exists: true, Busy: true, Kind: drain.KindDispatch},
	}
	if err := f.run(t, gracefulOptions{}); err != nil {
		t.Fatal(err)
	}
	f.wantLog(t, "busy", "restart")
	if len(f.asked) != 0 {
		t.Fatalf("asked %q", f.asked)
	}
	want := "2 dispatches are running (fix-x, d-1); they will be adopted across the restart.\n"
	if !strings.Contains(f.out.String(), want) {
		t.Fatalf("output %q lacks %q", f.out.String(), want)
	}
}

func TestGracefulRestartPromptNamesRunningDispatches(t *testing.T) {
	f := newGracefulFixture(true, "a")
	f.drain.busy = append(f.drain.busy, drain.Probe{Name: "fix-x", Exists: true, Busy: true, Kind: drain.KindDispatch})
	f.drain.doneAt = f.clock.Now()
	f.answers = []string{"y"}
	if err := f.run(t, gracefulOptions{}); err != nil {
		t.Fatal(err)
	}
	want := "1 agent is working (a) and 1 dispatch is running (fix-x). Graceful restart? [Y/n] "
	if len(f.asked) != 1 || f.asked[0] != want {
		t.Fatalf("asked %q, want %q", f.asked, want)
	}
}
