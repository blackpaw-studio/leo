package observe

import (
	"context"
	"errors"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
)

type fakeAgentLister []GitAgent

func (l fakeAgentLister) GitAgents() []GitAgent { return slices.Clone(l) }

type mutableLister struct{ agents []GitAgent }

func (l *mutableLister) GitAgents() []GitAgent { return slices.Clone(l.agents) }

type recordingSink struct {
	mu   sync.Mutex
	sets []map[string]GitStats
}

func (s *recordingSink) SetGitStats(m map[string]GitStats) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sets = append(s.sets, m)
}

func (s *recordingSink) last(t *testing.T) map[string]GitStats {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sets) == 0 {
		t.Fatal("sink never set")
	}
	return s.sets[len(s.sets)-1]
}

// workspaceRunner answers by the -C workspace in argv and counts calls.
type workspaceRunner struct {
	mu     sync.Mutex
	calls  []string
	out    map[string]string
	errFor map[string]error
	// hook runs inside each call, before it answers.
	hook func(ws string)
}

func (r *workspaceRunner) Run(_ context.Context, _ []string, argv ...string) ([]byte, int, error) {
	ws := argv[2]
	r.mu.Lock()
	r.calls = append(r.calls, ws)
	hook, out, err := r.hook, r.out[ws], r.errFor[ws]
	r.mu.Unlock()
	if hook != nil {
		hook(ws)
	}
	return []byte(out), 0, err
}

func (r *workspaceRunner) callsTo(ws string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Count(strings.Join(r.calls, "\n")+"\n", ws+"\n")
}

func TestGitPollerOneCallPerWorkspace(t *testing.T) {
	r := &workspaceRunner{out: map[string]string{"/ws/a": " 1 file changed, 2 insertions(+)\n", "/ws/b": ""}}
	sink := &recordingSink{}
	p := NewGitPoller(fakeAgentLister{{"alice", "/ws/a"}, {"bob", "/ws/a/"}, {"carol", "/ws/b"}}, r, sink, GitPollerOptions{})

	if !p.Tick(context.Background()) {
		t.Fatal("tick did not run")
	}

	if r.callsTo("/ws/a") != 1 || r.callsTo("/ws/b") != 1 || len(r.calls) != 2 {
		t.Fatalf("calls = %v; want one per unique workspace", r.calls)
	}
	got := sink.last(t)
	want := map[string]GitStats{"alice": {Added: 2}, "bob": {Added: 2}, "carol": {}}
	if len(got) != 3 || got["alice"] != want["alice"] || got["bob"] != want["bob"] || got["carol"] != want["carol"] {
		t.Fatalf("stats = %v; want %v", got, want)
	}
}

func TestGitPollerFailedWorkspaceHasNoGit(t *testing.T) {
	var logged []string
	r := &workspaceRunner{
		out:    map[string]string{"/ws/ok": " 1 file changed, 1 insertion(+)\n"},
		errFor: map[string]error{"/ws/bad": errors.New("boom")},
	}
	sink := &recordingSink{}
	p := NewGitPoller(fakeAgentLister{{"ok", "/ws/ok"}, {"bad", "/ws/bad"}, {"relative", "rel"}, {"none", ""}}, r, sink,
		GitPollerOptions{Logf: func(f string, a ...any) { logged = append(logged, f) }})

	p.Tick(context.Background())

	got := sink.last(t)
	if _, ok := got["bad"]; ok || len(got) != 1 || got["ok"].Added != 1 {
		t.Fatalf("stats = %v; want only the healthy workspace", got)
	}
	if len(logged) == 0 {
		t.Fatal("failures were not logged")
	}
}

func TestGitPollerNoAgentsClearsStats(t *testing.T) {
	sink := &recordingSink{}
	p := NewGitPoller(fakeAgentLister{}, &workspaceRunner{}, sink, GitPollerOptions{})
	p.Tick(context.Background())
	if got := sink.last(t); len(got) != 0 {
		t.Fatalf("stats = %v", got)
	}
}

func TestGitPollerSkipsATickWhilePreviousIsInFlight(t *testing.T) {
	entered, release := make(chan string, 4), make(chan struct{})
	r := &workspaceRunner{hook: func(ws string) { entered <- ws; <-release }}
	sink := &recordingSink{}
	p := NewGitPoller(fakeAgentLister{{"alice", "/ws/a"}}, r, sink, GitPollerOptions{})

	done := make(chan bool)
	go func() { done <- p.Tick(context.Background()) }()
	<-entered // the first poll is running

	if p.Tick(context.Background()) {
		t.Fatal("second tick ran while the first was in flight")
	}
	close(release)
	if !<-done {
		t.Fatal("first tick did not run")
	}
	if r.callsTo("/ws/a") != 1 {
		t.Fatalf("calls = %v", r.calls)
	}
	// The slot is free again afterwards.
	if !p.Tick(context.Background()) {
		t.Fatal("tick after completion did not run")
	}
}

func TestGitPollerBoundsConcurrency(t *testing.T) {
	const workspaces = 12
	var agents fakeAgentLister
	for i := 0; i < workspaces; i++ {
		agents = append(agents, GitAgent{Name: "a" + string(rune('a'+i)), Workspace: "/ws/" + string(rune('a'+i))})
	}
	var (
		mu       sync.Mutex
		inFlight int
		peak     int
	)
	entered, release := make(chan struct{}, workspaces), make(chan struct{})
	r := &workspaceRunner{hook: func(string) {
		mu.Lock()
		inFlight++
		peak = max(peak, inFlight)
		mu.Unlock()
		entered <- struct{}{}
		<-release
		mu.Lock()
		inFlight--
		mu.Unlock()
	}}
	p := NewGitPoller(agents, r, &recordingSink{}, GitPollerOptions{})

	done := make(chan struct{})
	go func() { p.Tick(context.Background()); close(done) }()
	for i := 0; i < DefaultGitPollConcurrency; i++ {
		<-entered
	}
	// Give any over-eager extra worker the chance to start before release.
	for i := 0; i < 100; i++ {
		runtime.Gosched()
	}
	close(release)
	<-done

	mu.Lock()
	defer mu.Unlock()
	if peak != DefaultGitPollConcurrency {
		t.Fatalf("peak concurrent git calls = %d; want %d", peak, DefaultGitPollConcurrency)
	}
	if len(r.calls) != workspaces {
		t.Fatalf("calls = %d; want %d", len(r.calls), workspaces)
	}
}

func TestGitPollerRunPollsEachTickUntilCancelled(t *testing.T) {
	r := &workspaceRunner{}
	sink := &recordingSink{}
	ticks := make(chan time.Time)
	var gotInterval time.Duration
	p := NewGitPoller(fakeAgentLister{{"alice", "/ws/a"}}, r, sink, GitPollerOptions{
		NewTicker: func(d time.Duration) (<-chan time.Time, func()) { gotInterval = d; return ticks, func() {} },
	})
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() { p.Run(ctx); close(finished) }()

	ticks <- time.Time{}
	ticks <- time.Time{} // unbuffered: the first poll has completed once this is taken
	cancel()
	<-finished

	if gotInterval != DefaultGitPollInterval {
		t.Fatalf("interval = %v", gotInterval)
	}
	if n := r.callsTo("/ws/a"); n < 1 {
		t.Fatalf("calls = %d; want at least one", n)
	}
}

func TestGitPollerLogsAPersistentFailureOnce(t *testing.T) {
	var logged []string
	r := &workspaceRunner{errFor: map[string]error{"/ws/bad": errors.New("boom")}}
	p := NewGitPoller(fakeAgentLister{{"bad", "/ws/bad"}}, r, &recordingSink{},
		GitPollerOptions{Logf: func(f string, a ...any) { logged = append(logged, f) }})

	for i := 0; i < 3; i++ {
		p.Tick(context.Background())
	}
	if len(logged) != 1 {
		t.Fatalf("logged %d times over 3 ticks; want once", len(logged))
	}

	// Recovery then a new failure logs again.
	r.mu.Lock()
	r.errFor = nil
	r.mu.Unlock()
	p.Tick(context.Background())
	r.mu.Lock()
	r.errFor = map[string]error{"/ws/bad": errors.New("boom")}
	r.mu.Unlock()
	p.Tick(context.Background())
	if len(logged) != 2 {
		t.Fatalf("logged %d times; want the recurrence logged", len(logged))
	}
}

func TestGitPollerKeepsPreviousGitWhenAWorkspaceErrors(t *testing.T) {
	r := &workspaceRunner{
		out: map[string]string{"/ws/a": " 1 file changed, 2 insertions(+)\n", "/ws/b": " 1 file changed, 5 deletions(-)\n"},
	}
	sink := &recordingSink{}
	p := NewGitPoller(fakeAgentLister{{"alice", "/ws/a"}, {"bob", "/ws/b"}}, r, sink, GitPollerOptions{})
	p.Tick(context.Background())

	// /ws/a now errors (timeout, git hiccup); /ws/b is fine.
	r.mu.Lock()
	r.errFor = map[string]error{"/ws/a": errors.New("timed out")}
	r.mu.Unlock()
	p.Tick(context.Background())

	got := sink.last(t)
	if got["alice"] != (GitStats{Added: 2}) || got["bob"] != (GitStats{Removed: 5}) {
		t.Fatalf("stats = %v; want alice's previous value kept through the error", got)
	}
}

func TestGitPollerKeepOnErrorFeedsNoPublish(t *testing.T) {
	h := newFeedHarness(t, nil)
	h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "a", Usage: hostUsage(t, nil)})
	r := &workspaceRunner{out: map[string]string{"/ws/a": " 1 file changed, 2 insertions(+)\n"}}
	p := NewGitPoller(fakeAgentLister{{"alice", "/ws/a"}}, r, h.feed, GitPollerOptions{})
	p.Tick(context.Background())
	h.pub.reset()

	r.mu.Lock()
	r.errFor = map[string]error{"/ws/a": errors.New("timed out")}
	r.mu.Unlock()
	p.Tick(context.Background())

	if n := len(h.pub.snapshot()); n != 0 {
		t.Fatalf("published %d events for a transient git error", n)
	}
	if u := h.usageNow(t); u.Git == nil || u.Git.Added != 2 {
		t.Fatalf("usage git = %+v; want kept", u.Git)
	}
}

func TestGitPollerClearsGitOnCleanNotARepoAndWhenAgentGoes(t *testing.T) {
	r := &workspaceRunner{out: map[string]string{"/ws/a": " 1 file changed, 2 insertions(+)\n"}}
	agents := &mutableLister{agents: []GitAgent{{"alice", "/ws/a"}, {"bob", "/ws/a"}}}
	sink := &recordingSink{}
	p := NewGitPoller(agents, r, sink, GitPollerOptions{})
	p.Tick(context.Background())

	// bob goes away: gone from the map. alice stays.
	agents.agents = []GitAgent{{"alice", "/ws/a"}}
	r.mu.Lock()
	r.errFor = map[string]error{"/ws/a": errors.New("timed out")}
	r.mu.Unlock()
	p.Tick(context.Background())
	got := sink.last(t)
	if _, ok := got["bob"]; ok || got["alice"].Added != 2 {
		t.Fatalf("stats = %v; want only alice, kept", got)
	}

	// A clean "not a repository" (exit 128) clears it.
	r2 := &exitRunner{out: " 1 file changed, 2 insertions(+)\n"}
	p2 := NewGitPoller(fakeAgentLister{{"alice", "/ws/a"}}, r2, sink, GitPollerOptions{})
	p2.Tick(context.Background())
	if got := sink.last(t); got["alice"].Added != 2 {
		t.Fatalf("stats = %v", got)
	}
	r2.code = 128
	p2.Tick(context.Background())
	if got := sink.last(t); len(got) != 0 {
		t.Fatalf("stats = %v; want cleared on a clean not-a-repo", got)
	}
}

// exitRunner answers every call with out and exit code.
type exitRunner struct {
	out  string
	code int
}

func (e *exitRunner) Run(context.Context, []string, ...string) ([]byte, int, error) {
	return []byte(e.out), e.code, nil
}

func TestGitPollerCancelledTickDoesNotPublish(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &workspaceRunner{
		errFor: map[string]error{"/ws/a": context.Canceled},
		hook:   func(string) { cancel() },
	}
	sink := &recordingSink{}
	var logged []string
	p := NewGitPoller(fakeAgentLister{{"alice", "/ws/a"}}, r, sink,
		GitPollerOptions{Logf: func(f string, a ...any) { logged = append(logged, f) }})

	p.Tick(ctx)

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.sets) != 0 || len(logged) != 0 {
		t.Fatalf("sink calls = %d, log lines = %d; a cancelled tick must do neither", len(sink.sets), len(logged))
	}
}
