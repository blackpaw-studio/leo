package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func testSnapshot(enabled bool) StateSnapshot {
	in, out, cost := int64(10), int64(20), 0.5
	return StateSnapshot{
		Delegation: DelegationState{Enabled: enabled, Section: "roles", HideAgents: []string{"Explore"}},
		Dispatches: []DispatchState{{ID: "d1", Name: "impl", Role: "implement", Template: "codex", Model: "gpt", Status: "running", ActiveSeconds: 3, TokensIn: &in, TokensOut: &out, CostUSD: &cost}},
	}
}

// nextLine reads one line off s, failing the test when none is ready.
func nextLine(t *testing.T, s *Stream) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	line, err := s.NextLine(ctx)
	if err != nil {
		t.Fatalf("NextLine: %v", err)
	}
	if len(line) == 0 || line[len(line)-1] != '\n' {
		t.Fatalf("line is not newline-terminated: %q", line)
	}
	var v map[string]any
	if err := json.Unmarshal(line, &v); err != nil {
		t.Fatalf("line is not JSON: %q: %v", line, err)
	}
	return v
}

func noLine(t *testing.T, s *Stream) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if line, err := s.NextLine(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want no line, got %q, %v", line, err)
	}
}

func TestStateLineWireShape(t *testing.T) {
	h := newTestHub(newFakeClock())
	s, err := h.Connect(agentA, testLaunch)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := h.SetState(agentA, testSnapshot(true)); err != nil {
		t.Fatal(err)
	}
	got := nextLine(t, s)
	if got["op"] != "state" {
		t.Fatalf("op = %v", got["op"])
	}
	if _, hasID := got["id"]; hasID {
		t.Fatalf("a state line is no command and carries no id: %v", got)
	}
	deleg := got["delegation"].(map[string]any)
	if deleg["enabled"] != true || deleg["section"] != "roles" || deleg["hide_agents"].([]any)[0] != "Explore" {
		t.Fatalf("delegation = %v", deleg)
	}
	d := got["dispatches"].([]any)[0].(map[string]any)
	for key, want := range map[string]any{"id": "d1", "name": "impl", "role": "implement", "template": "codex", "model": "gpt", "status": "running", "stalled": false, "active_seconds": 3.0, "tokens_in": 10.0, "tokens_out": 20.0, "cost_usd": 0.5} {
		if d[key] != want {
			t.Errorf("dispatch[%s] = %v, want %v", key, d[key], want)
		}
	}
}

func TestStateLatestWinsAndIsResentOnReconnect(t *testing.T) {
	h := newTestHub(newFakeClock())
	s, err := h.Connect(agentA, testLaunch)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.SetState(agentA, testSnapshot(true)); err != nil {
		t.Fatal(err)
	}
	if err := h.SetState(agentA, testSnapshot(false)); err != nil {
		t.Fatal(err)
	}
	if got := nextLine(t, s)["delegation"].(map[string]any)["enabled"]; got != false {
		t.Fatalf("the stream must see only the latest state, got enabled=%v", got)
	}
	noLine(t, s)
	s.Close()

	again, err := h.Connect(agentA, testLaunch)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if got := nextLine(t, again)["delegation"].(map[string]any)["enabled"]; got != false {
		t.Fatalf("a reconnect starts with the latest state, got enabled=%v", got)
	}
}

func TestStateGoesOutAheadOfQueuedCommandsButNeverBlocksThem(t *testing.T) {
	h := newTestHub(newFakeClock())
	if _, err := h.Enqueue(agentA, Deliver("hi", true)); err != nil {
		t.Fatal(err)
	}
	if err := h.SetState(agentA, testSnapshot(true)); err != nil {
		t.Fatal(err)
	}
	s, err := h.Connect(agentA, testLaunch)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if op := nextLine(t, s)["op"]; op != "state" {
		t.Fatalf("first line op = %v, want state", op)
	}
	if op := nextLine(t, s)["op"]; op != "deliver" {
		t.Fatalf("second line op = %v, want deliver", op)
	}
	// State is not a command: it is never pending and never acked.
	if got := h.State(agentA).Pending; got != 1 {
		t.Fatalf("pending = %d, want only the deliver", got)
	}
}

func TestNextSkipsStateLines(t *testing.T) {
	h := newTestHub(newFakeClock())
	s, err := h.Connect(agentA, testLaunch)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := h.SetState(agentA, testSnapshot(true)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Enqueue(agentA, Clear()); err != nil {
		t.Fatal(err)
	}
	cmd, err := s.Next(context.Background())
	if err != nil || cmd.Op != OpClear {
		t.Fatalf("Next = %+v, %v; want the clear", cmd, err)
	}
}

func TestSetStateNeedsAnOpenKey(t *testing.T) {
	h := New(Options{Clock: newFakeClock()})
	if err := h.SetState("nobody", testSnapshot(true)); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("err = %v, want ErrNotOpen", err)
	}
}

func TestConnectedKeys(t *testing.T) {
	h := newTestHub(newFakeClock())
	mustOpen(t, h, "other")
	if got := h.ConnectedKeys(); len(got) != 0 {
		t.Fatalf("no stream yet, got %v", got)
	}
	s, err := h.Connect(agentA, testLaunch)
	if err != nil {
		t.Fatal(err)
	}
	if got := h.ConnectedKeys(); len(got) != 1 || got[0] != agentA {
		t.Fatalf("got %v, want [%s]", got, agentA)
	}
	s.Close()
	if got := h.ConnectedKeys(); len(got) != 0 {
		t.Fatalf("closed stream still listed: %v", got)
	}
}

func TestParseRequestReport(t *testing.T) {
	r, err := ParseReport([]byte(`{"type":"request","op":"dispatch.cancel","dispatch_id":"d1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if r.Type != ReportRequest || r.Op != RequestDispatchCancel || r.DispatchID != "d1" {
		t.Fatalf("report = %+v", r)
	}
	for _, body := range []string{
		`{"type":"request","op":"dispatch.cancel"}`,
		`{"type":"request","op":"dispatch.delete","dispatch_id":"d1"}`,
		`{"type":"request","dispatch_id":"d1"}`,
		`{"type":"request","op":"dispatch.cancel","dispatch_id":"d1","id":"x"}`,
	} {
		if _, err := ParseReport([]byte(body)); !errors.Is(err, ErrInvalidReport) {
			t.Errorf("%s: err = %v, want ErrInvalidReport", body, err)
		}
	}
}

func TestApplyRequestCallsTheHandlerForTheCurrentLaunch(t *testing.T) {
	h := newTestHub(newFakeClock())
	var gotAgent, gotOp, gotID string
	h.SetRequestHandler(func(agent, op, dispatchID string) error {
		gotAgent, gotOp, gotID = agent, op, dispatchID
		return nil
	})
	req := Report{Type: ReportRequest, Op: RequestDispatchCancel, DispatchID: "d1"}
	if err := h.Apply(agentA, testLaunch, req); err != nil {
		t.Fatal(err)
	}
	if gotAgent != agentA || gotOp != RequestDispatchCancel || gotID != "d1" {
		t.Fatalf("handler saw %q %q %q", gotAgent, gotOp, gotID)
	}
	if err := h.Apply(agentA, "other-launch", req); !errors.Is(err, ErrStaleLaunch) {
		t.Fatalf("a stale launch's request: err = %v, want ErrStaleLaunch", err)
	}
}

func TestApplyRequestPassesTheHandlersRefusal(t *testing.T) {
	h := newTestHub(newFakeClock())
	req := Report{Type: ReportRequest, Op: RequestDispatchCancel, DispatchID: "d1"}
	if err := h.Apply(agentA, testLaunch, req); !errors.Is(err, ErrRequestUnavailable) {
		t.Fatalf("no handler: err = %v, want ErrRequestUnavailable", err)
	}
	h.SetRequestHandler(func(string, string, string) error { return ErrRequestDenied })
	if err := h.Apply(agentA, testLaunch, req); !errors.Is(err, ErrRequestDenied) {
		t.Fatalf("err = %v, want ErrRequestDenied", err)
	}
}

// A reconnect replays the latest state, but the working time of a running
// dispatch moves on meanwhile: the mod counts from when a line arrives, so
// a replay of the stored seconds would set its clock back.
func TestStateReplayAgesRunningDispatches(t *testing.T) {
	clock := newFakeClock()
	h := newTestHub(clock)
	snap := testSnapshot(true)
	snap.Dispatches = append(snap.Dispatches, DispatchState{ID: "d2", Status: "idle", ActiveSeconds: 7})
	if err := h.SetState(agentA, snap); err != nil {
		t.Fatal(err)
	}
	clock.Advance(30 * time.Second)
	s, err := h.Connect(agentA, testLaunch)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got := nextLine(t, s)["dispatches"].([]any)
	if running := got[0].(map[string]any)["active_seconds"]; running != 33.0 {
		t.Errorf("running active_seconds = %v, want 33 (3 stored + 30 since)", running)
	}
	if idle := got[1].(map[string]any)["active_seconds"]; idle != 7.0 {
		t.Errorf("idle active_seconds = %v, want 7: an idle dispatch's clock stands still", idle)
	}
}
