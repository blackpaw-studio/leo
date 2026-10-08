package consult

import (
	"reflect"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/bridge"
)

func parentRec(id, key string, status Status) Record {
	return callerRecord(id, key, status)
}

func childRec(id, parent, key string, status Status) Record {
	rec := callerRecord(id, key, status)
	rec.ParentDispatchID = parent
	return rec
}

func bridgeState(t *testing.T, states []bridge.DispatchState, id string) bridge.DispatchState {
	t.Helper()
	for _, s := range states {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("no state for %s in %v", id, ids(states))
	return bridge.DispatchState{}
}

func TestWithChildWaitShowsAnIdleParentWithRunningChildrenAsWaiting(t *testing.T) {
	cases := []struct {
		name     string
		parent   Status
		children []Status
		want     Status
		pending  string
	}{
		{"idle with one running child", StatusIdle, []Status{StatusRunning}, StatusWaiting, "1 dispatch"},
		{"settling with two live children", StatusSettling, []Status{StatusRunning, StatusQueued}, StatusWaiting, "2 dispatches"},
		{"idle with only a finished child", StatusIdle, []Status{StatusDone}, StatusIdle, ""},
		{"idle with a canceled child", StatusIdle, []Status{StatusCanceled}, StatusIdle, ""},
		{"idle with an idle child", StatusIdle, []Status{StatusIdle}, StatusIdle, ""},
		{"running parent stays running", StatusRunning, []Status{StatusRunning}, StatusRunning, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			records := []Record{parentRec("p", "alpha", tc.parent)}
			for i, s := range tc.children {
				records = append(records, childRec("c"+string(rune('0'+i)), "p", DispatchBridgeKey("p"), s))
			}
			got := WithChildWait(records)
			if got[0].Status != tc.want || got[0].PendingWork.Summary() != tc.pending {
				t.Fatalf("parent = %s %q, want %s %q", got[0].Status, got[0].PendingWork.Summary(), tc.want, tc.pending)
			}
		})
	}
}

func TestWithChildWaitKeepsTheParentsOwnPendingWorkAndInputUntouched(t *testing.T) {
	parent := parentRec("p", "alpha", StatusIdle)
	parent.PendingWork = &PendingWork{Tasks: map[string]int{"shell": 1}}
	records := []Record{parent, childRec("c", "p", DispatchBridgeKey("p"), StatusRunning)}
	got := WithChildWait(records)
	if s := got[0].PendingWork.Summary(); s != "1 shell · 1 dispatch" {
		t.Fatalf("pending = %q", s)
	}
	if records[0].Status != StatusIdle || records[0].PendingWork.Summary() != "1 shell" {
		t.Fatalf("input mutated: %s %q", records[0].Status, records[0].PendingWork.Summary())
	}
}

func TestWithChildWaitFollowsNestingAndFallsBackToTheCallersBridgeKey(t *testing.T) {
	records := []Record{
		parentRec("root", "alpha", StatusIdle),
		childRec("mid", "root", DispatchBridgeKey("root"), StatusIdle),
		childRec("leaf", "mid", DispatchBridgeKey("mid"), StatusRunning),
	}
	got := WithChildWait(records)
	if got[0].Status != StatusWaiting || got[1].Status != StatusWaiting {
		t.Fatalf("root=%s mid=%s, want both waiting on the running leaf", got[0].Status, got[1].Status)
	}

	legacy := childRec("legacy", "", DispatchBridgeKey("root"), StatusRunning)
	got = WithChildWait([]Record{parentRec("root", "alpha", StatusIdle), legacy})
	if got[0].Status != StatusWaiting {
		t.Fatalf("a record without ParentDispatchID should match by its bridge key; root=%s", got[0].Status)
	}
}

func TestBridgeDispatchStatesShowsAnIdleParentWaitingOnAChildOfAnotherKey(t *testing.T) {
	records := []Record{parentRec("p", "alpha", StatusIdle), childRec("c", "p", "other", StatusRunning)}
	got := bridgeState(t, BridgeDispatchStates(records, "alpha", stateNow), "p")
	if got.Status != "waiting" || got.Pending != "1 dispatch" {
		t.Fatalf("parent = %s %q", got.Status, got.Pending)
	}

	records[1].Status = StatusDone
	records[1].EndedAt = stateNow
	got = bridgeState(t, BridgeDispatchStates(records, "alpha", stateNow), "p")
	if got.Status != "idle" || got.Pending != "" {
		t.Fatalf("after the child finished parent = %s %q", got.Status, got.Pending)
	}
}

func TestRenderRosterShowsAnIdleParentWaitingOnItsChild(t *testing.T) {
	records := []Record{parentRec("p", "", StatusIdle), childRec("c", "p", "", StatusRunning)}
	records[0].Name, records[1].Name = "parent", "child"
	got := RenderRoster(records, stateNow)
	if !strings.Contains(got, "⧗ parent 0:00 · 1 dispatch") {
		t.Fatalf("RenderRoster = %q", got)
	}
}

func TestStatePusherRepushesTheParentWhenAChildOfAnotherKeyChanges(t *testing.T) {
	now := stateNow
	records := []Record{parentRec("p", "alpha", StatusIdle)}
	deleg := bridge.DelegationState{}
	p, hub := newPusherFixture(&records, &deleg, &now)
	p.Tick()
	base := len(hub.sets["alpha"])

	records = []Record{parentRec("p", "alpha", StatusIdle), childRec("c", "p", "other", StatusRunning)}
	p.Tick()
	pushes := hub.sets["alpha"]
	if len(pushes) != base+1 || bridgeState(t, pushes[base].Dispatches, "p").Status != "waiting" {
		t.Fatalf("pushes = %d, want a waiting push after the child started", len(pushes))
	}

	records[1].Status, records[1].EndedAt = StatusFailed, now
	p.Tick()
	pushes = hub.sets["alpha"]
	if len(pushes) != base+2 || bridgeState(t, pushes[base+1].Dispatches, "p").Status != "idle" {
		t.Fatalf("pushes = %d, want an idle push after the child failed", len(pushes))
	}
}

func TestDispatchObserverReportsAnIdleParentAsWaitingWithItsRunningChild(t *testing.T) {
	records := []Record{parentRec("p", "alpha", StatusIdle), childRec("c", "p", "other", StatusRunning)}
	o := NewDispatchObserver(func() []Record { return records }, nil)
	var parent *string
	for _, d := range o.Dispatches(stateNow) {
		if d.ID == "p" {
			s := d.Status + "|" + d.Pending
			parent = &s
		}
	}
	if parent == nil || *parent != "waiting|1 dispatch" {
		t.Fatalf("parent = %v", parent)
	}
}

func TestDispatchObserverTakesCapabilitiesFromTheRecordNotTheProjection(t *testing.T) {
	interactive := func(id string, status Status) Record {
		rec := parentRec(id, "alpha", status)
		rec.Mode, rec.PaneID, rec.ViewerKind = ModeInteractive, "%1", "window"
		return rec
	}
	records := []Record{
		interactive("settling", StatusSettling), childRec("c1", "settling", "other", StatusRunning),
		interactive("idle", StatusIdle), childRec("c2", "idle", "other", StatusRunning),
	}
	o := NewDispatchObserver(func() []Record { return records }, nil)
	attachable := map[string]bool{}
	for _, d := range o.Dispatches(stateNow) {
		if d.Status != "waiting" && d.ID != "c1" && d.ID != "c2" {
			t.Fatalf("%s status = %s, want waiting", d.ID, d.Status)
		}
		attachable[d.ID] = d.Attachable
	}
	if attachable["settling"] {
		t.Fatal("a settling parent shown as waiting must not be advertised as attachable")
	}
	if !attachable["idle"] {
		t.Fatal("an idle parent keeps the attachability its record has")
	}
}

func TestWithChildWaitIsIndependentOfRecordOrderInACycle(t *testing.T) {
	a, b := parentRec("a", "alpha", StatusIdle), parentRec("b", "alpha", StatusIdle)
	a.ParentDispatchID, b.ParentDispatchID = "b", "a"
	c := childRec("c", "a", "other", StatusRunning)
	summary := func(records []Record) map[string]string {
		out := map[string]string{}
		for _, r := range WithChildWait(records) {
			out[r.ID] = string(r.Status) + "|" + r.PendingWork.Summary()
		}
		return out
	}
	forward, reverse := summary([]Record{a, b, c}), summary([]Record{c, b, a})
	if !reflect.DeepEqual(forward, reverse) {
		t.Fatalf("forward %v != reverse %v", forward, reverse)
	}
	if forward["a"] != "waiting|1 dispatch" {
		t.Fatalf("a = %q, want waiting on its running child", forward["a"])
	}
}

func TestTickAnnouncesAParentLeavingWaitingWhenItsChildFinishes(t *testing.T) {
	parent := agentDispatch("p", "alpha", StatusIdle)
	child := agentDispatch("c", "other", StatusRunning)
	child.ParentDispatchID = "p"
	h := newObserveHarness(parent, child)

	h.obs.Tick()
	if got := h.pub.take(t); !reflect.DeepEqual(got, []string{"p:waiting", "c:running"}) {
		t.Fatalf("first tick published %v", got)
	}
	h.records.recs[1].Status, h.records.recs[1].EndedAt = StatusDone, h.now
	h.obs.Tick()
	got := h.pub.dispatches(t)
	if len(got) != 2 || got[0].ID != "p" || got[0].Status != "idle" || got[0].Pending != "" || got[1].ID != "c" {
		t.Fatalf("child finishing published %+v, want p to go idle then c done", got)
	}
}
