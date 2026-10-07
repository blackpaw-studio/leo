package consult

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
)

func startEffortDispatch(t *testing.T, effort string) (*Dispatcher, string) {
	t.Helper()
	d := NewDispatcher(newFakeRecorder())
	d.liveUsage.poll = time.Hour
	rt := &bridgedFakeRuntime{fakeInteractiveRuntime: &fakeInteractiveRuntime{arm: true, empty: true}, bridges: true}
	d.SetInteractiveRuntime(rt)
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "hello", Cwd: t.TempDir(), Mode: ModeInteractive, Effort: effort})
	if err != nil {
		t.Fatal(err)
	}
	return d, started.ID
}

// The bridge's observed effort lands on the record and the working turn,
// and never replaces the requested one.
func TestObservedEffortIsRecordedBesideTheRequestedOne(t *testing.T) {
	d, id := startEffortDispatch(t, "xhigh")
	mustReport(t, d, id, liveHook(t, "s1", "UserPromptSubmit", "hello", ""))
	d.ApplyObservedEffort(id, "high")

	rec := recordOf(t, d, id)
	if rec.Effort != "xhigh" || rec.ObservedEffort != "high" {
		t.Fatalf("effort=%q observed=%q, want xhigh and high", rec.Effort, rec.ObservedEffort)
	}
	if got := rec.Turns[len(rec.Turns)-1].ObservedEffort; got != "high" {
		t.Fatalf("turn observed effort = %q, want high", got)
	}
	states := BridgeDispatchStates([]Record{rec}, rec.CallerBridgeKey, time.Now())
	if rec.CallerBridgeKey != "" && (len(states) != 1 || states[0].ObservedEffort != "high") {
		t.Fatalf("bridge state = %+v, want observed_effort high", states)
	}
}

// Claude's shell hooks carry effort as {"level": ...}.
func TestObservedEffortFromAShellHookPayload(t *testing.T) {
	d, id := startEffortDispatch(t, "")
	payload, _ := json.Marshal(map[string]any{"hook_event_name": "UserPromptSubmit", "prompt": "hello", "effort": map[string]string{"level": "medium"}})
	mustReport(t, d, id, HookReport{EventID: "s1", Payload: payload})
	if got := recordOf(t, d, id).ObservedEffort; got != "medium" {
		t.Fatalf("observed effort = %q, want medium", got)
	}
}

// A level outside claude's set is ignored.
func TestObservedEffortIgnoresUnknownLevels(t *testing.T) {
	d, id := startEffortDispatch(t, "")
	d.ApplyObservedEffort(id, "turbo")
	if got := recordOf(t, d, id).ObservedEffort; got != "" {
		t.Fatalf("observed effort = %q, want none", got)
	}
}

func TestBridgeDispatchStateCarriesObservedEffort(t *testing.T) {
	rec := Record{ID: "d1", Kind: "dispatch", CallerBridgeKey: "k", Status: StatusRunning, Effort: "", ObservedEffort: "medium"}
	states := BridgeDispatchStates([]Record{rec}, "k", time.Now())
	if len(states) != 1 || states[0].ObservedEffort != "medium" {
		t.Fatalf("states = %+v", states)
	}
}

func TestObservedDispatchCarriesEfforts(t *testing.T) {
	got := observedDispatch(Record{ID: "d1", Kind: "dispatch", Effort: "xhigh", ObservedEffort: "high"}, time.Now())
	if got.Effort != "xhigh" || got.ObservedEffort != "high" {
		t.Fatalf("observed dispatch = %+v", got)
	}
}

func TestEffortLabel(t *testing.T) {
	for _, tt := range []struct{ requested, observed, want string }{
		{"", "", ""},
		{"high", "", "high"},
		{"high", "high", "high"},
		{"", "medium", "~medium"},
		{"xhigh", "high", "xhigh→high"},
	} {
		if got := (Record{Effort: tt.requested, ObservedEffort: tt.observed}).EffortLabel(); got != tt.want {
			t.Errorf("EffortLabel(%q, %q) = %q, want %q", tt.requested, tt.observed, got, tt.want)
		}
	}
}

type effortLog struct {
	reportLog
	efforts []string
}

func (l *effortLog) ApplyObservedEffort(id, level string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.efforts = append(l.efforts, id+":"+level)
}

// The subscriber hands an owned dispatch's effort event to the sink, and
// reports no hook for it.
func TestDispatchBridgeSubscriberForwardsObservedEffort(t *testing.T) {
	g := newBridgeRig(t, "2.1.289")
	g.launch(t, "d-eff", "claude", "brief")
	var log effortLog
	g.hub.AddSubscriber(g.r.DispatchBridgeSubscriber(&log))
	key := DispatchBridgeKey("d-eff")
	g.connectAndAckOpening(t, key)
	if err := g.apply(t, key, bridge.Report{Type: bridge.ReportEvent, Name: bridge.EventEffort, Effort: "max"}); err != nil {
		t.Fatal(err)
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	if len(log.efforts) != 1 || log.efforts[0] != "d-eff:max" || len(log.got["d-eff"]) != 0 {
		t.Fatalf("efforts=%v reports=%v", log.efforts, log.got)
	}
}
