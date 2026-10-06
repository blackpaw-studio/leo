package consult

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/blackpaw-studio/leo/internal/bridge"
)

func startBridgedDispatch(t *testing.T) (*Dispatcher, string) {
	t.Helper()
	d := NewDispatcher(newFakeRecorder())
	rt := &bridgedFakeRuntime{fakeInteractiveRuntime: &fakeInteractiveRuntime{arm: true, empty: true}, bridges: true}
	d.SetInteractiveRuntime(rt)
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "hello", Cwd: t.TempDir(), Mode: ModeInteractive})
	if err != nil {
		t.Fatal(err)
	}
	return d, started.ID
}

func recordOf(t *testing.T, d *Dispatcher, id string) Record {
	t.Helper()
	rec, err := d.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func wantTokens(t *testing.T, rec Record, in, out int64, incomplete bool) {
	t.Helper()
	if rec.InputTokens == nil || *rec.InputTokens != in || rec.OutputTokens == nil || *rec.OutputTokens != out || rec.UsageIncomplete != incomplete {
		t.Fatalf("usage in=%v out=%v incomplete=%v, want in=%d out=%d incomplete=%v",
			deref(rec.InputTokens), deref(rec.OutputTokens), rec.UsageIncomplete, in, out, incomplete)
	}
}

func deref(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

// Each bridged turn reports its own tokens: they add up across turns, input
// counting cached reads and writes as the headless path does, and with the
// session cost reported too the usage is complete.
func TestBridgeTurnTokensAccumulateAndCompleteTheUsage(t *testing.T) {
	d, id := startBridgedDispatch(t)
	d.ApplyBridgeTokens(id, "turn.complete:t1", &bridge.TurnTokens{Input: 10, Output: 20, CacheRead: 1000, CacheCreation: 5, Model: "m"})
	d.ApplyBridgeUsage(id, json.RawMessage(`{"cost":{"usd":0.25}}`))
	wantTokens(t, recordOf(t, d, id), 1015, 20, false)

	d.ApplyBridgeTokens(id, "turn.complete:t2", &bridge.TurnTokens{Input: 1, Output: 2, CacheRead: 3, CacheCreation: 4})
	d.ApplyBridgeUsage(id, json.RawMessage(`{"cost":{"usd":0.3}}`))
	rec := recordOf(t, d, id)
	wantTokens(t, rec, 1023, 22, false)
	if rec.CostUSD == nil || *rec.CostUSD != 0.3 {
		t.Fatalf("cost = %v, want the session's running total 0.3", rec.CostUSD)
	}
}

// The mod retries a report the daemon did not take; a replay of a turn's
// tokens must not count them twice.
func TestBridgeTurnTokensReplayIsCountedOnce(t *testing.T) {
	d, id := startBridgedDispatch(t)
	tokens := &bridge.TurnTokens{Input: 10, Output: 20}
	d.ApplyBridgeTokens(id, "turn.complete:t1", tokens)
	d.ApplyBridgeTokens(id, "turn.complete:t1", tokens)
	d.ApplyBridgeUsage(id, json.RawMessage(`{"cost":{"usd":0.1}}`))
	wantTokens(t, recordOf(t, d, id), 10, 20, false)
}

// A turn.complete without tokens (a mod from before tokens were reported)
// leaves the counts short: the usage stays marked incomplete.
func TestBridgeTurnWithoutTokensKeepsTheUsageIncomplete(t *testing.T) {
	d, id := startBridgedDispatch(t)
	d.ApplyBridgeTokens(id, "turn.complete:t1", &bridge.TurnTokens{Input: 10, Output: 20})
	d.ApplyBridgeTokens(id, "turn.complete:t2", nil)
	d.ApplyBridgeUsage(id, json.RawMessage(`{"cost":{"usd":0.1}}`))
	wantTokens(t, recordOf(t, d, id), 10, 20, true)
}

func TestBridgeTokensForUnknownRunAreIgnored(t *testing.T) {
	d, _ := startBridgedDispatch(t)
	d.ApplyBridgeTokens("d-unknown", "turn.complete:t1", &bridge.TurnTokens{Input: 1})
}

type tokenLog struct {
	reportLog
	mu     sync.Mutex
	order  []string
	tokens []*bridge.TurnTokens
}

func (l *tokenLog) ApplyBridgeTokens(id, eventID string, tokens *bridge.TurnTokens) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.order = append(l.order, "tokens:"+eventID)
	l.tokens = append(l.tokens, tokens)
}

func (l *tokenLog) ApplyBridgeUsage(id string, raw json.RawMessage) {
	l.mu.Lock()
	l.order = append(l.order, "usage")
	l.mu.Unlock()
	l.reportLog.ApplyBridgeUsage(id, raw)
}

// The subscriber hands a sink that takes tokens each turn.complete's
// tokens (nil when the mod sent none), ahead of the session usage, so the
// cost lands on counts that are already up to date.
func TestDispatchBridgeSubscriberForwardsTurnTokens(t *testing.T) {
	g := newBridgeRig(t, "2.1.289")
	g.launch(t, "d-tok", "claude", "brief")
	var log tokenLog
	g.hub.AddSubscriber(g.r.DispatchBridgeSubscriber(&log))
	key := DispatchBridgeKey("d-tok")
	g.connectAndAckOpening(t, key)
	tokens := &bridge.TurnTokens{Input: 1, Output: 2, CacheRead: 3, CacheCreation: 4, Model: "m"}
	for _, r := range []bridge.Report{
		{Type: bridge.ReportEvent, Name: bridge.EventTurnComplete, EventID: "turn.complete:t1", Tokens: tokens, Usage: json.RawMessage(`{"cost":{"usd":0.1}}`)},
		{Type: bridge.ReportEvent, Name: bridge.EventTurnComplete, EventID: "turn.complete:t2"},
	} {
		if err := g.apply(t, key, r); err != nil {
			t.Fatal(err)
		}
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	want := []string{"tokens:turn.complete:t1", "usage", "tokens:turn.complete:t2"}
	if len(log.order) != len(want) {
		t.Fatalf("calls = %v, want %v", log.order, want)
	}
	for i := range want {
		if log.order[i] != want[i] {
			t.Fatalf("calls = %v, want %v", log.order, want)
		}
	}
	if log.tokens[0] == nil || *log.tokens[0] != *tokens || log.tokens[1] != nil {
		t.Fatalf("tokens = %+v, want %+v then nil", log.tokens, *tokens)
	}
}
