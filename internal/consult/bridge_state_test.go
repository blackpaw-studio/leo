package consult

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
)

var stateNow = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

func callerRecord(id, key string, status Status) Record {
	return Record{ID: id, Kind: "dispatch", CallerBridgeKey: key, Status: status, Name: "n-" + id, Role: "implement", Template: "codex", Model: "gpt", StartedAt: stateNow.Add(-time.Hour)}
}

func ids(states []bridge.DispatchState) []string {
	out := []string{}
	for _, s := range states {
		out = append(out, s.ID)
	}
	return out
}

func TestBridgeDispatchStatesKeepsOnlyTheCallersLiveAndRecentDispatches(t *testing.T) {
	recent := callerRecord("recent", "alpha", StatusDone)
	recent.EndedAt = stateNow.Add(-59 * time.Second)
	old := callerRecord("old", "alpha", StatusFailed)
	old.EndedAt = stateNow.Add(-61 * time.Second)
	consultRec := callerRecord("consult", "alpha", StatusRunning)
	consultRec.Kind = "consult"
	records := []Record{
		callerRecord("b-run", "alpha", StatusRunning),
		callerRecord("a-idle", "alpha", StatusIdle),
		callerRecord("other", "beta", StatusRunning),
		callerRecord("nokey", "", StatusRunning),
		callerRecord("released", "alpha", StatusReleased),
		recent, old, consultRec,
	}
	records[1].StartedAt = stateNow.Add(-2 * time.Hour)
	got := ids(BridgeDispatchStates(records, "alpha", stateNow))
	if want := []string{"a-idle", "b-run", "recent"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v (oldest first)", got, want)
	}
	if got := BridgeDispatchStates(records, "", stateNow); len(got) != 0 {
		t.Fatalf("an empty key matched %v", ids(got))
	}
}

func TestBridgeDispatchStatesMapsTheRecord(t *testing.T) {
	since := stateNow.Add(-5 * time.Second)
	rec := callerRecord("d1", "alpha", StatusRunning)
	rec.ActiveSeconds, rec.RunningSince = 10, &since
	rec.InputTokens, rec.OutputTokens, rec.CostUSD = ptr(int64(100)), ptr(int64(20)), ptr(0.25)
	got := BridgeDispatchStates([]Record{rec}, "alpha", stateNow)
	want := []bridge.DispatchState{{ID: "d1", Name: "n-d1", Role: "implement", Template: "codex", Model: "gpt", Status: "running", ActiveSeconds: 15, TokensIn: ptr(int64(100)), TokensOut: ptr(int64(20)), CostUSD: ptr(0.25)}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got[0], want[0])
	}
}

func TestBridgeDispatchStatesMarksAQuietOpenTurnStalled(t *testing.T) {
	rec := callerRecord("d1", "alpha", StatusRunning)
	rec.Mode = ModeInteractive
	rec.Turns = []Turn{{TurnID: "d1#1", StartedAt: stateNow.Add(-stalledAfter)}}
	if got := BridgeDispatchStates([]Record{rec}, "alpha", stateNow); !got[0].Stalled {
		t.Fatal("an open turn quiet for stalledAfter must be stalled")
	}
	rec.HookActivity = stateNow.Add(-time.Minute)
	if got := BridgeDispatchStates([]Record{rec}, "alpha", stateNow); got[0].Stalled {
		t.Fatal("recent activity is not stalled")
	}
	rec.Turns[0].Outcome = TurnFinished
	rec.HookActivity = time.Time{}
	if got := BridgeDispatchStates([]Record{rec}, "alpha", stateNow); got[0].Stalled {
		t.Fatal("a finished turn is not stalled")
	}
}

// fakeStateHub records SetState calls.
type fakeStateHub struct {
	connected []string
	gens      map[string]uint64
	sets      map[string][]bridge.StateSnapshot
}

func (h *fakeStateHub) ConnectedKeys() []string { return h.connected }
func (h *fakeStateHub) State(key string) bridge.State {
	return bridge.State{Agent: key, Gen: h.gens[key]}
}
func (h *fakeStateHub) SetState(key string, s bridge.StateSnapshot) error {
	if h.sets == nil {
		h.sets = map[string][]bridge.StateSnapshot{}
	}
	h.sets[key] = append(h.sets[key], s)
	return nil
}

func newPusherFixture(records *[]Record, deleg *bridge.DelegationState, now *time.Time) (*StatePusher, *fakeStateHub) {
	hub := &fakeStateHub{connected: []string{"alpha"}, gens: map[string]uint64{"alpha": 1}}
	p := &StatePusher{
		Hub:        hub,
		Records:    func() []Record { return *records },
		Delegation: func() bridge.DelegationState { return *deleg },
		Now:        func() time.Time { return *now },
	}
	return p, hub
}

func TestStatePusherPushesOnlyWhatChanged(t *testing.T) {
	since := stateNow
	run := callerRecord("d1", "alpha", StatusRunning)
	run.RunningSince = &since
	records := []Record{run}
	deleg := bridge.DelegationState{Enabled: true, Section: "roles", HideAgents: []string{"Explore"}}
	now := stateNow
	p, hub := newPusherFixture(&records, &deleg, &now)

	p.Tick()
	if n := len(hub.sets["alpha"]); n != 1 {
		t.Fatalf("first tick pushed %d snapshots, want 1", n)
	}
	first := hub.sets["alpha"][0]
	if !first.Delegation.Enabled || first.Delegation.Section != "roles" || len(first.Dispatches) != 1 {
		t.Fatalf("snapshot = %+v", first)
	}

	// Working time alone moves on: the mod counts it locally.
	now = now.Add(3 * time.Second)
	p.Tick()
	if n := len(hub.sets["alpha"]); n != 1 {
		t.Fatalf("an elapsed-only change pushed (%d snapshots)", n)
	}

	records[0].Status = StatusIdle
	p.Tick()
	if n := len(hub.sets["alpha"]); n != 2 {
		t.Fatalf("a status change was not pushed (%d snapshots)", n)
	}
	if got := hub.sets["alpha"][1].Dispatches[0].ActiveSeconds; got != 3 {
		t.Fatalf("pushed active_seconds = %v, want the live 3", got)
	}

	deleg.Enabled = false
	p.Tick()
	if n := len(hub.sets["alpha"]); n != 3 || hub.sets["alpha"][2].Delegation.Enabled {
		t.Fatalf("a delegation change was not pushed: %+v", hub.sets["alpha"])
	}
}

func TestStatePusherRepushesToANewGeneration(t *testing.T) {
	var records []Record
	deleg := bridge.DelegationState{Enabled: true}
	now := stateNow
	p, hub := newPusherFixture(&records, &deleg, &now)
	p.Tick()
	hub.gens["alpha"] = 2
	p.Tick()
	if n := len(hub.sets["alpha"]); n != 2 {
		t.Fatalf("a relaunched key got %d snapshots, want 2", n)
	}
}

func TestStatePusherDropsAFinishedDispatchAfterAMinute(t *testing.T) {
	done := callerRecord("d1", "alpha", StatusDone)
	done.EndedAt = stateNow
	records := []Record{done}
	deleg := bridge.DelegationState{}
	now := stateNow
	p, hub := newPusherFixture(&records, &deleg, &now)
	p.Tick()
	now = now.Add(61 * time.Second)
	p.Tick()
	sets := hub.sets["alpha"]
	if len(sets) != 2 || len(sets[0].Dispatches) != 1 || len(sets[1].Dispatches) != 0 {
		t.Fatalf("snapshots = %+v", sets)
	}
}

// A dispatch's claude does not delegate (it gets no delegation block today
// either), so its mod is told delegation is off.
func TestStatePusherTellsADispatchDelegationIsOff(t *testing.T) {
	var records []Record
	deleg := bridge.DelegationState{Enabled: true, Section: "roles"}
	now := stateNow
	p, hub := newPusherFixture(&records, &deleg, &now)
	key := DispatchBridgeKey("d9")
	hub.connected = []string{key}
	p.Tick()
	if got := hub.sets[key]; len(got) != 1 || got[0].Delegation.Enabled {
		t.Fatalf("dispatch snapshot = %+v", got)
	}
}

func TestStatePusherSkipsWhenNothingIsConnected(t *testing.T) {
	calls := 0
	hub := &fakeStateHub{}
	p := &StatePusher{Hub: hub, Records: func() []Record { calls++; return nil }, Delegation: func() bridge.DelegationState { calls++; return bridge.DelegationState{} }, Now: func() time.Time { return stateNow }}
	p.Tick()
	if calls != 0 || len(hub.sets) != 0 {
		t.Fatalf("idle tick read %d inputs and pushed %v", calls, hub.sets)
	}
}

func seedRun(d *Dispatcher, rec Record) {
	d.mu.Lock()
	d.runs[rec.ID] = &runState{record: rec, handle: nopHandle{}, done: make(chan struct{}), cancel: func() {}}
	d.mu.Unlock()
}

func TestCancelForCallerCancelsOnlyTheCallersOwnDispatch(t *testing.T) {
	d := NewDispatcher(nil)
	mine := callerRecord("mine", "alpha", StatusRunning)
	mine.Mode = ModeHeadless
	theirs := callerRecord("theirs", "beta", StatusRunning)
	theirs.Mode = ModeHeadless
	seedRun(d, mine)
	seedRun(d, theirs)

	if err := d.CancelForCaller("alpha", "theirs"); !errors.Is(err, bridge.ErrRequestDenied) {
		t.Fatalf("another agent's dispatch: err = %v, want ErrRequestDenied", err)
	}
	if rec, _ := d.Get("theirs"); rec.Status != StatusRunning {
		t.Fatalf("denied cancel changed the dispatch: %s", rec.Status)
	}
	if err := d.CancelForCaller("alpha", "missing"); !errors.Is(err, bridge.ErrRequestDenied) {
		t.Fatalf("unknown dispatch: err = %v, want ErrRequestDenied", err)
	}
	if err := d.CancelForCaller("", "theirs"); !errors.Is(err, bridge.ErrRequestDenied) {
		t.Fatalf("empty key: err = %v, want ErrRequestDenied", err)
	}
	if err := d.CancelForCaller("alpha", "mine"); err != nil {
		t.Fatal(err)
	}
	if rec, _ := d.Get("mine"); rec.Status != StatusCanceled {
		t.Fatalf("own dispatch status = %s, want canceled", rec.Status)
	}
}

func TestDispatchRequestHandlerServesCancelOnly(t *testing.T) {
	d := NewDispatcher(nil)
	rec := callerRecord("mine", "alpha", StatusRunning)
	rec.Mode = ModeHeadless
	seedRun(d, rec)
	serve := d.BridgeRequestHandler()
	if err := serve("alpha", "dispatch.nuke", "mine"); err == nil {
		t.Fatal("an unknown op was served")
	}
	if err := serve("alpha", bridge.RequestDispatchCancel, "mine"); err != nil {
		t.Fatal(err)
	}
}
