package observe

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakeCodexLister struct {
	mu     sync.Mutex
	agents []CodexAgent
}

func (l *fakeCodexLister) CodexAgents() []CodexAgent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]CodexAgent(nil), l.agents...)
}

type harnessSink struct{ sets []map[string]AgentUsage }

func (s *harnessSink) SetHarnessUsage(m map[string]AgentUsage) { s.sets = append(s.sets, m) }

func (s *harnessSink) last(t *testing.T) map[string]AgentUsage {
	t.Helper()
	if len(s.sets) == 0 {
		t.Fatal("sink never set")
	}
	return s.sets[len(s.sets)-1]
}

// codexFixture wires a poller over real temp rollout files, with the locators
// answered from maps so each test decides what "codex's sessions dir" holds.
type codexFixture struct {
	t        *testing.T
	dir      string
	now      time.Time
	lister   *fakeCodexLister
	sink     *harnessSink
	poller   *CodexPoller
	byID     map[string]string // session id → rollout path
	latest   map[string][]string
	idCalls  int
	walkCall int
}

func newCodexFixture(t *testing.T) *codexFixture {
	f := &codexFixture{
		t: t, dir: t.TempDir(), now: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC),
		lister: &fakeCodexLister{}, sink: &harnessSink{},
		byID: map[string]string{}, latest: map[string][]string{},
	}
	f.poller = NewCodexPoller(f.lister, CodexLocators{
		ByID: func(_, id string) string { f.idCalls++; return f.byID[id] },
		Latest: func(_, workspace string, _ time.Time) (string, string, int) {
			f.walkCall++
			m := f.latest[workspace]
			if len(m) == 0 {
				return "", "", 0
			}
			return m[0], "disc-" + workspace, len(m)
		},
	}, f.sink, CodexPollerOptions{Now: func() time.Time { return f.now }})
	return f
}

func (f *codexFixture) rollout(name string, lines ...[]byte) string {
	f.t.Helper()
	path := filepath.Join(f.dir, name+".jsonl")
	for _, l := range lines {
		appendFile(f.t, path, string(l)+"\n")
	}
	return path
}

func (f *codexFixture) tick() map[string]AgentUsage {
	f.t.Helper()
	if !f.poller.Tick(context.Background()) {
		f.t.Fatal("tick did not run")
	}
	return f.sink.last(f.t)
}

func TestCodexPoller(t *testing.T) {
	const at1, at2 = "2026-11-12T10:00:02Z", "2026-11-12T10:05:00Z"
	started := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)

	t.Run("a stored id reads the rollout end to end, tailing appends", func(t *testing.T) {
		f := newCodexFixture(t)
		path := f.rollout("a", tcCtx(at1, "gpt-test-1", "high"), tcCount(at1, 1000, 1000, 10000, tcRate("codex", tcWin(12.5, 300, 1794000000))))
		f.byID["sid-a"] = path
		f.lister.agents = []CodexAgent{{Name: "alice", Home: "/h", SessionID: "sid-a", StartedAt: started}}

		u, ok := f.tick()["alice"]
		if !ok || u.SessionID != "sid-a" || u.Model != "gpt-test-1" || u.Effort != "high" ||
			u.Session.Tokens != 1000 || u.Incarnation.Tokens != 1000 || u.Context == nil || u.Context.Percent != 10 || len(u.RateLimits) != 1 {
			t.Fatalf("usage = %+v", u)
		}

		// Nothing appended: nothing is counted twice.
		if got := f.tick()["alice"]; got.Session.Tokens != 1000 {
			t.Fatalf("unchanged file tokens = %d", got.Session.Tokens)
		}
		appendFile(t, path, string(tcCount(at2, 1700, 700, 10000, ""))+"\n")
		if got := f.tick()["alice"]; got.Session.Tokens != 1700 || got.Context.Tokens != 700 {
			t.Fatalf("after append = %+v", got)
		}
		if f.idCalls != 1 {
			t.Fatalf("id lookups = %d, want the path cached after the first", f.idCalls)
		}
	})

	t.Run("a changed StartedAt starts the incarnation over from the rollout", func(t *testing.T) {
		f := newCodexFixture(t)
		path := f.rollout("a", tcCount(at1, 1000, 1000, 10000, ""), tcCount(at2, 1600, 600, 10000, ""))
		f.byID["sid-a"] = path
		f.lister.agents = []CodexAgent{{Name: "alice", Home: "/h", SessionID: "sid-a", StartedAt: started}}
		if got := f.tick()["alice"]; got.Incarnation.Tokens != 1600 {
			t.Fatalf("incarnation = %d", got.Incarnation.Tokens)
		}
		// The agent restarted between the two token_count lines.
		f.lister.agents[0].StartedAt = started.Add(time.Minute)
		got := f.tick()["alice"]
		if got.Incarnation.Tokens != 600 || got.Session.Tokens != 1600 {
			t.Fatalf("after restart: incarnation=%d session=%d", got.Incarnation.Tokens, got.Session.Tokens)
		}
	})

	t.Run("a rollout that cannot be found omits the agent", func(t *testing.T) {
		f := newCodexFixture(t)
		f.lister.agents = []CodexAgent{{Name: "alice", Home: "/h", SessionID: "gone"}}
		if _, ok := f.tick()["alice"]; ok {
			t.Fatal("agent reported with no rollout")
		}
	})

	t.Run("without an id one matching rollout is used, and the id is not persisted", func(t *testing.T) {
		f := newCodexFixture(t)
		f.latest["/ws"] = []string{f.rollout("d", tcCount(at1, 500, 500, 10000, ""))}
		f.lister.agents = []CodexAgent{{Name: "alice", Home: "/h", Workspace: "/ws", StartedAt: started}}
		u, ok := f.tick()["alice"]
		if !ok || u.Session.Tokens != 500 || u.SessionID != "disc-/ws" {
			t.Fatalf("usage = %+v", u)
		}
	})

	t.Run("without an id two matching rollouts are ambiguous: omitted", func(t *testing.T) {
		f := newCodexFixture(t)
		f.latest["/ws"] = []string{f.rollout("d1", tcCount(at1, 500, 500, 10000, "")), f.rollout("d2")}
		f.lister.agents = []CodexAgent{{Name: "alice", Home: "/h", Workspace: "/ws", StartedAt: started}}
		if _, ok := f.tick()["alice"]; ok {
			t.Fatal("ambiguous fallback reported")
		}
	})

	t.Run("the fallback walks at most once a minute", func(t *testing.T) {
		f := newCodexFixture(t)
		f.lister.agents = []CodexAgent{{Name: "alice", Home: "/h", Workspace: "/ws", StartedAt: started}}
		f.tick()
		f.now = f.now.Add(30 * time.Second)
		f.tick()
		if f.walkCall != 1 {
			t.Fatalf("walks = %d within 30s, want 1", f.walkCall)
		}
		f.latest["/ws"] = []string{f.rollout("d", tcCount(at1, 500, 500, 10000, ""))}
		f.now = f.now.Add(31 * time.Second)
		if _, ok := f.tick()["alice"]; !ok || f.walkCall != 2 {
			t.Fatalf("after a minute: ok=%v walks=%d", ok, f.walkCall)
		}
		// A pin is re-checked on each rewalk (to notice a second match), not
		// on every tick.
		f.now = f.now.Add(30 * time.Second)
		f.tick()
		if f.walkCall != 2 {
			t.Fatalf("a pinned rollout was walked for again within the interval: walks=%d", f.walkCall)
		}
		f.now = f.now.Add(31 * time.Second)
		f.tick()
		if f.walkCall != 3 {
			t.Fatalf("walks = %d after the interval, want 3", f.walkCall)
		}
	})

	t.Run("a rollout with nothing to report yet is omitted", func(t *testing.T) {
		f := newCodexFixture(t)
		f.byID["sid-a"] = f.rollout("a", []byte(`{"type":"session_meta","payload":{}}`))
		f.lister.agents = []CodexAgent{{Name: "alice", Home: "/h", SessionID: "sid-a"}}
		if _, ok := f.tick()["alice"]; ok {
			t.Fatal("empty usage reported")
		}
	})

	t.Run("a new rollout resets the session but keeps rate limits", func(t *testing.T) {
		f := newCodexFixture(t)
		f.byID["sid-a"] = f.rollout("a", tcCtx(at1, "gpt-test-1", ""), tcCount(at1, 1000, 1000, 10000, tcRate("codex", tcWin(12.5, 300, 1794000000))))
		f.byID["sid-b"] = f.rollout("b", tcCount(at2, 40, 40, 10000, ""))
		f.lister.agents = []CodexAgent{{Name: "alice", Home: "/h", SessionID: "sid-a"}}
		f.tick()
		f.lister.agents[0].SessionID = "sid-b"
		u := f.tick()["alice"]
		if u.SessionID != "sid-b" || u.Session.Tokens != 40 || u.Model != "" || len(u.RateLimits) != 1 {
			t.Fatalf("usage = %+v", u)
		}
	})

	t.Run("an agent that leaves the list leaves the snapshot", func(t *testing.T) {
		f := newCodexFixture(t)
		f.byID["sid-a"] = f.rollout("a", tcCount(at1, 1000, 1000, 10000, ""))
		f.lister.agents = []CodexAgent{{Name: "alice", Home: "/h", SessionID: "sid-a"}}
		f.tick()
		f.lister.agents = nil
		if got := f.tick(); len(got) != 0 {
			t.Fatalf("snapshot = %+v", got)
		}
	})

	t.Run("a cancelled context publishes nothing", func(t *testing.T) {
		f := newCodexFixture(t)
		f.byID["sid-a"] = f.rollout("a", tcCount(at1, 1000, 1000, 10000, ""))
		f.lister.agents = []CodexAgent{{Name: "alice", Home: "/h", SessionID: "sid-a"}}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if f.poller.Tick(ctx) || len(f.sink.sets) != 0 {
			t.Fatalf("published after cancel: %v", f.sink.sets)
		}
	})
}

func TestCodexPollerFallbackAttribution(t *testing.T) {
	const at1 = "2026-11-12T10:00:02Z"
	started := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)

	t.Run("a rollout another agent holds is never assigned by the fallback", func(t *testing.T) {
		f := newCodexFixture(t)
		path := f.rollout("b", tcCount(at1, 700, 700, 10000, ""))
		f.byID["sid-b"] = path
		f.latest["/ws"] = []string{path} // the only rollout in the shared workspace is B's
		// A is listed first: the id holders still resolve before any fallback.
		f.lister.agents = []CodexAgent{
			{Name: "alice", Home: "/h", Workspace: "/ws", StartedAt: started},
			{Name: "bob", Home: "/h", Workspace: "/ws", SessionID: "sid-b", StartedAt: started},
		}
		got := f.tick()
		if _, ok := got["alice"]; ok {
			t.Fatalf("alice was attributed bob's rollout: %+v", got["alice"])
		}
		if got["bob"].Session.Tokens != 700 {
			t.Fatalf("bob = %+v", got["bob"])
		}
		f.now = f.now.Add(2 * time.Minute)
		if _, ok := f.tick()["alice"]; ok {
			t.Fatal("alice claimed bob's rollout on a later walk")
		}
	})

	t.Run("two fallback agents do not share one rollout", func(t *testing.T) {
		f := newCodexFixture(t)
		f.latest["/ws"] = []string{f.rollout("only", tcCount(at1, 500, 500, 10000, ""))}
		f.lister.agents = []CodexAgent{
			{Name: "alice", Home: "/h", Workspace: "/ws", StartedAt: started},
			{Name: "bob", Home: "/h", Workspace: "/ws", StartedAt: started},
		}
		if got := f.tick(); len(got) != 1 {
			t.Fatalf("agents with usage = %d, want the one rollout held once", len(got))
		}
	})

	t.Run("a fallback pin is dropped once a second rollout matches", func(t *testing.T) {
		f := newCodexFixture(t)
		first := f.rollout("d1", tcCount(at1, 500, 500, 10000, ""))
		f.latest["/ws"] = []string{first}
		f.lister.agents = []CodexAgent{{Name: "alice", Home: "/h", Workspace: "/ws", StartedAt: started}}
		if _, ok := f.tick()["alice"]; !ok {
			t.Fatal("no usage from the single match")
		}
		f.latest["/ws"] = []string{first, f.rollout("d2")}
		f.now = f.now.Add(30 * time.Second)
		if _, ok := f.tick()["alice"]; !ok {
			t.Fatal("pin dropped before the rewalk interval")
		}
		f.now = f.now.Add(31 * time.Second)
		if _, ok := f.tick()["alice"]; ok {
			t.Fatal("pin kept although two rollouts now match")
		}
		// Back to one match: attributed again on a later walk.
		f.latest["/ws"] = []string{first}
		f.now = f.now.Add(61 * time.Second)
		if _, ok := f.tick()["alice"]; !ok {
			t.Fatal("not re-pinned after the ambiguity ended")
		}
	})

	t.Run("a stored id whose rollout is not there yet keeps serving the fallback", func(t *testing.T) {
		f := newCodexFixture(t)
		path := f.rollout("d", tcCount(at1, 500, 500, 10000, ""))
		f.latest["/ws"] = []string{path}
		f.lister.agents = []CodexAgent{{Name: "alice", Home: "/h", Workspace: "/ws", StartedAt: started}}
		f.tick()
		// leo stores the id, but ByID cannot find the file yet.
		f.lister.agents[0].SessionID = "sid-a"
		if got := f.tick()["alice"]; got.Session.Tokens != 500 {
			t.Fatalf("row vanished or changed after the id was stored: %+v", got)
		}
		f.now = f.now.Add(2 * time.Minute)
		if got := f.tick()["alice"]; got.Session.Tokens != 500 {
			t.Fatalf("row vanished on the rewalk: %+v", got)
		}
		// The id resolves to the same file: no double counting.
		f.byID["sid-a"] = path
		f.now = f.now.Add(2 * time.Minute)
		got := f.tick()["alice"]
		if got.Session.Tokens != 500 || got.SessionID != "sid-a" {
			t.Fatalf("after the id resolved = %+v", got)
		}
	})

	t.Run("an id that resolved before keeps its last usage while the file is missing", func(t *testing.T) {
		f := newCodexFixture(t)
		path := f.rollout("a", tcCount(at1, 500, 500, 10000, ""))
		f.byID["sid-a"] = path
		f.lister.agents = []CodexAgent{{Name: "alice", Home: "/h", SessionID: "sid-a", StartedAt: started}}
		f.tick()
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		delete(f.byID, "sid-a")
		for i := range 3 {
			f.now = f.now.Add(2 * time.Minute)
			if got := f.tick()["alice"]; got.Session.Tokens != 500 {
				t.Fatalf("tick %d: row vanished: %+v", i, got)
			}
		}
	})
}
