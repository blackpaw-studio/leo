package consult

import (
	"reflect"
	"sort"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/observe"
)

type recordingPublisher struct{ events []observe.Event }

func (p *recordingPublisher) Publish(ev observe.Event) { p.events = append(p.events, ev) }

func (p *recordingPublisher) dispatches(t *testing.T) []observe.Dispatch {
	t.Helper()
	out := []observe.Dispatch{}
	for _, ev := range p.events {
		payload, ok := ev.Payload.(*observe.DispatchChangedPayload)
		if ev.Type != observe.EventDispatchChanged || !ok {
			t.Fatalf("unexpected event %s %T", ev.Type, ev.Payload)
		}
		out = append(out, payload.Dispatch)
	}
	return out
}

func (p *recordingPublisher) take(t *testing.T) []string {
	t.Helper()
	got := []string{}
	for _, d := range p.dispatches(t) {
		got = append(got, d.ID+":"+d.Status)
	}
	p.events = nil
	return got
}

type fakeRecords struct{ recs []Record }

func (f *fakeRecords) get() []Record { return append([]Record(nil), f.recs...) }

type observeHarness struct {
	now     time.Time
	records *fakeRecords
	pub     *recordingPublisher
	calls   []string
	obs     *DispatchObserver
}

func newObserveHarness(recs ...Record) *observeHarness {
	h := &observeHarness{now: stateNow, records: &fakeRecords{recs: recs}, pub: &recordingPublisher{}}
	h.obs = NewDispatchObserver(h.records.get, h.pub,
		WithDispatchClock(func() time.Time { return h.now }),
		WithDispatchOwner(ownKey),
		WithOutstandingListener(func(agent string, n int) {
			h.calls = append(h.calls, agent+"="+strconv.Itoa(n))
		}))
	return h
}

// agentDispatch is a dispatch by caller through its bridge, whose key is
// its own name and whose launch is caller-launch (see ownKey).
func agentDispatch(id, caller string, status Status) Record {
	rec := callerRecord(id, caller, status)
	rec.Caller, rec.CallerBridgeKey, rec.CallerBridgeLaunch = caller, caller, caller+"-launch"
	return rec
}

// ownKey resolves each bridge key to the agent of the same name, whatever
// its launch.
func ownKey(key, _ string) (string, bool) { return key, true }

func observedIDs(ds []observe.Dispatch) []string {
	out := []string{}
	for _, d := range ds {
		out = append(out, d.ID)
	}
	sort.Strings(out)
	return out
}

func TestDispatchesKeepsLiveAndRecentlyEndedDispatchesOnly(t *testing.T) {
	recent := agentDispatch("recent", "alpha", StatusDone)
	recent.EndedAt = stateNow.Add(-59 * time.Second)
	old := agentDispatch("old", "alpha", StatusFailed)
	old.EndedAt = stateNow.Add(-61 * time.Second)
	consultRec := agentDispatch("consult", "alpha", StatusRunning)
	consultRec.Kind = "consult"
	h := newObserveHarness(agentDispatch("live", "alpha", StatusRunning), agentDispatch("idle", "beta", StatusIdle), recent, old, consultRec)

	got := observedIDs(h.obs.Dispatches(stateNow))
	if want := []string{"idle", "live", "recent"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got, want := observedIDs(h.obs.Dispatches(stateNow.Add(2*time.Second))), []string{"idle", "live"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("past the linger window got %v, want %v", got, want)
	}
}

func TestDispatchesMapsTheRecord(t *testing.T) {
	ended := stateNow.Add(-10 * time.Second)
	rec := agentDispatch("d1", "alpha", StatusDone)
	rec.EndedAt = ended
	rec.InputTokens, rec.OutputTokens, rec.CostUSD = ptr(int64(100)), ptr(int64(20)), ptr(0.25)
	h := newObserveHarness(rec)

	got := h.obs.Dispatches(stateNow)
	want := []observe.Dispatch{{ID: "d1", Name: "n-d1", Role: "implement", Template: "codex", Model: "gpt", Status: "done", CallerAgent: "alpha", StartedAt: rec.StartedAt, EndedAt: &ended, TokensIn: 100, TokensOut: 20, CostUSD: 0.25}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestDispatchesReportsStalled(t *testing.T) {
	rec := agentDispatch("d1", "alpha", StatusRunning)
	rec.Turns = []Turn{{TurnID: "t1", StartedAt: stateNow.Add(-stalledAfter)}}
	h := newObserveHarness(rec)
	if got := h.obs.Dispatches(stateNow); len(got) != 1 || !got[0].Stalled {
		t.Fatalf("got %+v, want one stalled dispatch", got)
	}
}

func TestDispatchesLinksChildrenOfDispatchCallers(t *testing.T) {
	parent := agentDispatch("p1", "alpha", StatusRunning)
	child := agentDispatch("c1", "alpha", StatusRunning)
	child.CallerBridgeKey = DispatchBridgeKey("p1")
	h := newObserveHarness(parent, child)

	parents := map[string]string{}
	for _, d := range h.obs.Dispatches(stateNow) {
		parents[d.ID] = d.ParentDispatchID
	}
	if want := map[string]string{"p1": "", "c1": "p1"}; !reflect.DeepEqual(parents, want) {
		t.Fatalf("parents %v, want %v", parents, want)
	}
}

func TestOutstandingDispatchesCountsEachAgentsNonTerminalDirectDispatches(t *testing.T) {
	done := agentDispatch("done", "alpha", StatusDone)
	done.EndedAt = stateNow
	nested := agentDispatch("nested", "alpha", StatusRunning)
	nested.CallerBridgeKey = DispatchBridgeKey("a1")
	anonymous := agentDispatch("anon", "", StatusRunning)
	h := newObserveHarness(
		agentDispatch("a1", "alpha", StatusRunning),
		agentDispatch("a2", "alpha", StatusIdle),
		agentDispatch("b1", "beta", StatusQueued),
		done, nested, anonymous,
	)
	got := h.obs.OutstandingDispatches()
	if want := map[string]int{"alpha": 2, "beta": 1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestTickPublishesOnlyChangedDispatches(t *testing.T) {
	old := agentDispatch("old", "alpha", StatusDone)
	old.EndedAt = stateNow.Add(-time.Hour)
	h := newObserveHarness(agentDispatch("d1", "alpha", StatusRunning), old)

	h.obs.Tick()
	if got, want := h.pub.take(t), []string{"d1:running"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first tick published %v, want %v (long-finished ones skipped)", got, want)
	}
	h.now = h.now.Add(time.Second)
	h.obs.Tick()
	if got := h.pub.take(t); len(got) != 0 {
		t.Fatalf("unchanged tick published %v", got)
	}

	h.records.recs[0].Status, h.records.recs[0].EndedAt = StatusDone, h.now
	h.records.recs[0].InputTokens = ptr(int64(5))
	h.obs.Tick()
	got := h.pub.dispatches(t)
	if len(got) != 1 || got[0].Status != "done" || got[0].EndedAt == nil || got[0].TokensIn != 5 {
		t.Fatalf("terminal transition published %+v", got)
	}
	h.pub.events = nil

	h.now = h.now.Add(observe.DispatchLinger + time.Second)
	h.obs.Tick()
	if got := h.pub.take(t); len(got) != 0 {
		t.Fatalf("leaving the linger window published %v", got)
	}
}

func TestTickPublishesNewDispatches(t *testing.T) {
	h := newObserveHarness()
	h.obs.Tick()
	h.records.recs = append(h.records.recs, agentDispatch("d1", "alpha", StatusQueued))
	h.obs.Tick()
	if got, want := h.pub.take(t), []string{"d1:queued"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestTickNotifiesOutstandingTransitions(t *testing.T) {
	h := newObserveHarness(agentDispatch("a1", "alpha", StatusRunning), agentDispatch("a2", "alpha", StatusRunning))
	h.obs.Tick()
	if want := []string{"alpha=2"}; !reflect.DeepEqual(h.calls, want) {
		t.Fatalf("calls %v, want %v", h.calls, want)
	}
	h.calls = nil
	h.obs.Tick()
	if len(h.calls) != 0 {
		t.Fatalf("unchanged count notified %v", h.calls)
	}

	h.records.recs[0].Status, h.records.recs[0].EndedAt = StatusDone, h.now
	h.obs.Tick()
	h.records.recs[1].Status, h.records.recs[1].EndedAt = StatusFailed, h.now
	h.obs.Tick()
	if want := []string{"alpha=1", "alpha=0"}; !reflect.DeepEqual(h.calls, want) {
		t.Fatalf("calls %v, want %v", h.calls, want)
	}
}

func TestTickNotifiesZeroWhenAnAgentsRecordsDisappear(t *testing.T) {
	h := newObserveHarness(agentDispatch("a1", "alpha", StatusRunning))
	h.obs.Tick()
	h.records.recs = nil
	h.calls = nil
	h.obs.Tick()
	if want := []string{"alpha=0"}; !reflect.DeepEqual(h.calls, want) {
		t.Fatalf("calls %v, want %v", h.calls, want)
	}
}

func TestDispatchObserverWithoutListenerOrPublisher(t *testing.T) {
	obs := NewDispatchObserver(func() []Record { return []Record{agentDispatch("a1", "alpha", StatusRunning)} }, nil, WithDispatchOwner(ownKey))
	obs.Tick()
	if got := obs.OutstandingDispatches(); got["alpha"] != 1 {
		t.Fatalf("got %v", got)
	}
}

// A caller's bridge key is fixed at launch, so it names the agent across a
// rename while Record.Caller keeps the old name.
func TestOutstandingDispatchesFollowTheCallersBridgeKeyAcrossARename(t *testing.T) {
	renamed := agentDispatch("d1", "old-name", StatusRunning)
	renamed.CallerBridgeKey, renamed.CallerBridgeLaunch = "k-old", "k-old@1"
	orphaned := agentDispatch("d3", "delta", StatusRunning)
	orphaned.CallerBridgeKey, orphaned.CallerBridgeLaunch = "k-gone", "k-gone@1"
	owners := map[string]string{"k-old@1": "new-name"}
	obs := NewDispatchObserver(func() []Record { return []Record{renamed, orphaned} }, nil,
		WithDispatchOwner(func(_, launch string) (string, bool) { name, ok := owners[launch]; return name, ok }))

	got := obs.OutstandingDispatches()

	if want := map[string]int{"new-name": 1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("OutstandingDispatches = %v, want %v", got, want)
	}
	// The attention store's snapshot stays keyed by the caller's launch:
	// it resolves the owner itself as it applies the counts.
	if _, snap := obs.DispatchSnapshot(); !reflect.DeepEqual(snap, map[string]int{"k-old@1": 1, "k-gone@1": 1}) {
		t.Fatalf("DispatchSnapshot = %v; want counts by caller launch", snap)
	}
}

// Snapshot generations follow the order the records were read: one that
// starts while another is inside records() reads after it and gets the
// higher generation.
func TestDispatchSnapshotGenerationsFollowReadOrder(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	obs := NewDispatchObserver(func() []Record {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return nil
	}, nil)

	genA := make(chan uint64, 1)
	go func() { gen, _ := obs.DispatchSnapshot(); genA <- gen }()
	<-entered
	genB := make(chan uint64, 1)
	go func() { gen, _ := obs.DispatchSnapshot(); genB <- gen }()
	close(release)
	a, b := <-genA, <-genB

	if a >= b {
		t.Fatalf("generations a=%d b=%d; the snapshot read second must be newer", a, b)
	}
	if gen, _ := obs.DispatchSnapshot(); gen <= b {
		t.Fatalf("generation %d did not increase past %d", gen, b)
	}
}

// Only a dispatch whose caller has a bridge key holds that caller's
// attention: a keyless one (a codex caller, the CLI, a claude without the
// mod) still shows its caller but counts toward no agent.
func TestKeylessDispatchesAreListedButNeverOutstanding(t *testing.T) {
	keyless := agentDispatch("d1", "gamma", StatusRunning)
	keyless.CallerBridgeKey = ""
	h := newObserveHarness(keyless)

	if got := h.obs.OutstandingDispatches(); len(got) != 0 {
		t.Fatalf("OutstandingDispatches = %v; want none", got)
	}
	if _, got := h.obs.DispatchSnapshot(); len(got) != 0 {
		t.Fatalf("DispatchSnapshot counts = %v; want none", got)
	}
	ds := h.obs.Dispatches(h.now)
	if len(ds) != 1 || ds[0].CallerAgent != "gamma" {
		t.Fatalf("Dispatches = %+v; want d1 with caller gamma", ds)
	}
}
