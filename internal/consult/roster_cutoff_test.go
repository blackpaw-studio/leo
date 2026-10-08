package consult

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
)

func finishedRecord(id, key string, endedAgo time.Duration) Record {
	rec := callerRecord(id, key, StatusDone)
	rec.EndedAt = stateNow.Add(-endedAgo)
	return rec
}

func clearEvent(key, reason string) bridge.Event {
	return bridge.Event{Agent: key, Name: bridge.EventSessionEnd, Reason: reason}
}

func TestRosterCutoffsHideOnlyDispatchesFinishedBeforeTheClear(t *testing.T) {
	now := stateNow
	c := NewRosterCutoffs(func() time.Time { return now })
	c.OnBridgeEvent(clearEvent("alpha", "clear"))

	stillRunning := callerRecord("running", "alpha", StatusRunning)
	// Finished after the clear: ages out by the normal grace.
	now = stateNow.Add(10 * time.Second)
	finishedLater := finishedRecord("later", "alpha", 0)
	finishedLater.EndedAt = now
	records := []Record{
		finishedRecord("before", "alpha", 5*time.Second),
		finishedRecord("at", "alpha", 0),
		finishedRecord("other-caller", "beta", 5*time.Second),
		stillRunning, finishedLater,
	}
	got := []string{}
	for _, rec := range c.Visible(records) {
		got = append(got, rec.ID)
	}
	if want := []string{"other-caller", "running", "later"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("visible = %v, want %v", got, want)
	}
}

func TestRosterCutoffsIgnoreSessionEndsThatAreNotAClear(t *testing.T) {
	c := NewRosterCutoffs(func() time.Time { return stateNow })
	c.OnBridgeEvent(clearEvent("alpha", "resume"))
	c.OnBridgeEvent(clearEvent("alpha", "logout"))
	c.OnBridgeEvent(bridge.Event{Agent: "alpha", Name: bridge.EventTurnComplete, Reason: "clear"})
	if got := c.Visible([]Record{finishedRecord("d", "alpha", time.Second)}); len(got) != 1 {
		t.Fatalf("a non-clear event hid a dispatch: %v", got)
	}
}

func TestRosterCutoffsLaterClearMovesTheCutoffForward(t *testing.T) {
	now := stateNow
	c := NewRosterCutoffs(func() time.Time { return now })
	c.OnBridgeEvent(clearEvent("alpha", "clear"))
	rec := finishedRecord("d", "alpha", 0)
	rec.EndedAt = stateNow.Add(5 * time.Second)
	now = stateNow.Add(10 * time.Second)
	if len(c.Visible([]Record{rec})) != 1 {
		t.Fatal("finished after the first clear, hidden")
	}
	c.OnBridgeEvent(clearEvent("alpha", "clear"))
	if len(c.Visible([]Record{rec})) != 0 {
		t.Fatal("finished before the second clear, still visible")
	}
}

func TestNilRosterCutoffsHideNothing(t *testing.T) {
	var c *RosterCutoffs
	records := []Record{finishedRecord("d", "alpha", time.Second)}
	if got := c.Visible(records); len(got) != 1 {
		t.Fatalf("nil cutoffs hid %v", got)
	}
}

func TestStatePusherBandDropsDispatchesFinishedBeforeAClear(t *testing.T) {
	records := []Record{finishedRecord("old", "alpha", 5*time.Second), callerRecord("live", "alpha", StatusRunning)}
	deleg := bridge.DelegationState{}
	now := stateNow
	p, hub := newPusherFixture(&records, &deleg, &now)
	p.Cutoffs = NewRosterCutoffs(func() time.Time { return stateNow })
	p.Cutoffs.OnBridgeEvent(clearEvent("alpha", "clear"))

	p.Tick()
	got := hub.sets["alpha"]
	if len(got) != 1 || !reflect.DeepEqual(ids(got[0].Dispatches), []string{"live"}) {
		t.Fatalf("band = %+v, want only the live dispatch", got)
	}
}

func TestViewerRosterDropsDispatchesFinishedBeforeACallerClear(t *testing.T) {
	run, pane := isolatedTmux(t, dispatchViewerSession)
	now := time.Now()
	c := NewRosterCutoffs(func() time.Time { return now })
	v := &Viewer{TmuxPath: "tmux", ExecCommand: run, Cutoffs: c}
	old := Record{ID: "d-a", Kind: "dispatch", Name: "stale-run", Status: StatusDone, StartedAt: now.Add(-time.Minute), EndedAt: now.Add(-time.Second), ViewerPaneID: pane, CallerBridgeKey: "worker"}
	live := Record{ID: "d-b", Kind: "dispatch", Name: "live-run", Status: StatusRunning, StartedAt: now, ViewerPaneID: pane, CallerBridgeKey: "worker"}
	v.UpdateRoster([]Record{old, live}, now)
	if got := showSessionOption(t, run, dispatchViewerSession, "@leo_roster"); !strings.Contains(got, "stale-run") {
		t.Fatalf("before the clear, roster = %q, want the finished dispatch", got)
	}
	c.OnBridgeEvent(clearEvent("worker", "clear"))
	v.UpdateRoster([]Record{old, live}, now)
	got := showSessionOption(t, run, dispatchViewerSession, "@leo_roster")
	if strings.Contains(got, "stale-run") || !strings.Contains(got, "live-run") {
		t.Fatalf("after the clear, roster = %q, want only the live dispatch", got)
	}
}
