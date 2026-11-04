package web

import (
	"net/http"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/observe"
)

// manualClock is an observe.FeedClock the test advances by hand.
type manualClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*manualTimer
}

type manualTimer struct {
	at      time.Time
	fn      func()
	stopped bool
}

func (c *manualClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

func (c *manualClock) AfterFunc(d time.Duration, fn func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &manualTimer{at: c.now.Add(d), fn: fn}
	c.timers = append(c.timers, t)
	return func() bool { c.mu.Lock(); defer c.mu.Unlock(); was := !t.stopped; t.stopped = true; return was }
}

func (c *manualClock) Advance(d time.Duration) {
	c.mu.Lock()
	end := c.now.Add(d)
	sort.SliceStable(c.timers, func(i, j int) bool { return c.timers[i].at.Before(c.timers[j].at) })
	due := []*manualTimer{}
	for _, t := range c.timers {
		if !t.stopped && !t.at.After(end) {
			t.stopped = true
			due = append(due, t)
		}
	}
	c.now = end
	c.mu.Unlock()
	for _, t := range due {
		t.fn()
	}
}

type autoRoute struct {
	*Server
	clock *manualClock
}

func newAutoRouteServer(t *testing.T) autoRoute {
	t.Helper()
	s := newTokenSplitServer(t)
	svc := s.agentSvc.(*mockAgentService)
	svc.handles = map[string]resolvedHandle{
		"leo-a": {harnessName: "claude", handle: harness.SessionHandle{Name: "leo-a", IDs: staticIDs("sid-a")}},
		"leo-b": {harnessName: "codex", handle: harness.SessionHandle{Name: "leo-b", IDs: staticIDs("")}},
	}
	s.attention = observe.NewAttentionStore(nil)
	s.attention.RegisterToken("tok-a", "leo-a")
	s.attention.RegisterToken("tok-b", "leo-b")
	clock := &manualClock{now: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)}
	s.progress = observe.NewProgressStore(clock, nil)
	s.autoCmds = observe.NewAutoCommands(clock, s.progress, s.attention.AgentForToken)
	return autoRoute{Server: s, clock: clock}
}

const bashPre = `{"hook_event_name":"PreToolUse","session_id":"sid-a","tool_name":"Bash","tool_use_id":"toolu_1","tool_input":{"command":"make","description":"Build it"}}`

// hook posts a hook payload as the agent would, with its bearer token.
func (r autoRoute) hook(t *testing.T, token, payload string) {
	t.Helper()
	w := requestAs(t, r.Server, http.MethodPost, "/api/agent/hook", testAgentToken, `{"token":"`+token+`","payload":`+payload+`}`)
	if w.Code != http.StatusOK {
		t.Fatalf("hook status %d: %s", w.Code, w.Body.String())
	}
}

func (r autoRoute) autoTasks(agent string) []observe.AgentTask {
	var out []observe.AgentTask
	for _, task := range r.progress.Tasks(agent) {
		if task.Source == observe.SourceAuto {
			out = append(out, task)
		}
	}
	return out
}

func TestAgentHookArmsAnAutoCommandTask(t *testing.T) {
	r := newAutoRouteServer(t)

	r.hook(t, "tok-a", bashPre)
	if len(r.autoTasks("leo-a")) != 0 {
		t.Fatal("published before the 20 s threshold")
	}
	r.clock.Advance(observe.AutoCommandDelay)

	tasks := r.autoTasks("leo-a")
	if len(tasks) != 1 || tasks[0].Title != "Build it" || tasks[0].State != observe.TaskRunning {
		t.Fatalf("got %+v", tasks)
	}
	r.hook(t, "tok-a", `{"hook_event_name":"PostToolUse","session_id":"sid-a","tool_name":"Bash","tool_use_id":"toolu_1"}`)
	if got := r.autoTasks("leo-a"); got[0].State != observe.TaskSucceeded {
		t.Fatalf("got %+v", got)
	}
}

func TestAgentHookForAStaleTokenOrOtherSessionTracksNothing(t *testing.T) {
	r := newAutoRouteServer(t)
	r.hook(t, "tok-ghost", bashPre) // a token no launch holds
	r.hook(t, "tok-a", `{"hook_event_name":"PreToolUse","session_id":"sid-other","tool_name":"Bash","tool_use_id":"toolu_2","tool_input":{"command":"make"}}`)

	r.clock.Advance(time.Hour)

	if len(r.progress.AllTasks()) != 0 {
		t.Fatalf("got %+v", r.progress.AllTasks())
	}
}

func TestLeoProgressRunningSuppressesAutoCommandsButNotDispatchTasks(t *testing.T) {
	r := newAutoRouteServer(t)
	r.hook(t, "tok-a", bashPre)
	r.clock.Advance(observe.AutoCommandDelay)
	if _, err := r.progress.UpsertAuto("leo-a", observe.AutoTask{ID: "auto.dispatch.d1", Title: "impl · implement"}); err != nil {
		t.Fatal(err)
	}

	w := postProgress(t, r.Server, testAgentToken, `{"token":"tok-a","id":"build","title":"Build"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	tasks := r.autoTasks("leo-a")
	if len(tasks) != 1 || tasks[0].ID != "auto.dispatch.d1" {
		t.Fatalf("auto tasks after the agent reported: %+v", tasks)
	}
	if len(r.progress.Tasks("leo-a")) != 2 {
		t.Fatalf("the agent's own task must stay: %+v", r.progress.Tasks("leo-a"))
	}
}

func TestLeoProgressEndingATaskDoesNotSuppress(t *testing.T) {
	r := newAutoRouteServer(t)
	r.hook(t, "tok-a", bashPre)
	r.clock.Advance(observe.AutoCommandDelay)

	postProgress(t, r.Server, testAgentToken, `{"token":"tok-a","id":"build","title":"Build","state":"succeeded"}`)

	if len(r.autoTasks("leo-a")) != 1 {
		t.Fatalf("a finishing report leaves the command task alone: %+v", r.autoTasks("leo-a"))
	}
}

func TestProgressRouteRejectsTheReservedAutoPrefixAndDismissOfAutoTasks(t *testing.T) {
	r := newAutoRouteServer(t)

	w := postProgress(t, r.Server, testAgentToken, `{"token":"tok-a","id":"auto.cmd.x","title":"T"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("reserved prefix: status %d", w.Code)
	}

	r.progress.UpsertAuto("leo-a", observe.AutoTask{ID: "auto.cmd.abc", Title: "T", State: observe.TaskRunning})
	r.progress.UpsertAuto("leo-a", observe.AutoTask{ID: "auto.cmd.abc", State: observe.TaskFailed})
	w = dismissTask(t, r.Server, testAPIToken, "leo-a", "auto.cmd.abc")
	if w.Code != http.StatusConflict {
		t.Fatalf("dismiss auto: status %d: %s", w.Code, w.Body.String())
	}
}
