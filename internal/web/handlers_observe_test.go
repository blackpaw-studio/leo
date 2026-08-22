package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/cron"
	"github.com/blackpaw-studio/leo/internal/history"
	"github.com/blackpaw-studio/leo/internal/observe"
)

// fakeActivityProvider is a test double for observe.ActivityProvider.
type fakeActivityProvider struct {
	activities map[string]observe.AgentActivity
}

func (f *fakeActivityProvider) Activities() map[string]observe.AgentActivity {
	return f.activities
}

// fakeEventSource is a test double for the eventSource seam.
type fakeEventSource struct {
	ch           chan observe.Event
	unsubscribed chan struct{}
	seq          uint64
}

func newFakeEventSource() *fakeEventSource {
	return &fakeEventSource{
		ch:           make(chan observe.Event, 8),
		unsubscribed: make(chan struct{}, 1),
	}
}

// Subscribe returns the fake's channel and unsubscribe func alongside its
// canned sequence number, letting tests assert handleAPIEvents stamps hello
// with whatever the event source reports as its last-assigned sequence.
func (f *fakeEventSource) Subscribe(buffer int) (<-chan observe.Event, func(), uint64) {
	return f.ch, func() {
		select {
		case f.unsubscribed <- struct{}{}:
		default:
		}
	}, f.seq
}

// --- GET /api/v1/state ---

func TestHandleAPIStateEnvelopeAndAuth(t *testing.T) {
	s, _ := newTestServer(t)

	req := httptest.NewRequest("GET", "/api/v1/state", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp apiResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected ok=true, got %+v", resp)
	}

	data, ok := resp.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected object data, got %T", resp.Data)
	}
	if data["version"] != float64(observe.SnapshotVersion) {
		t.Errorf("version = %v, want %d", data["version"], observe.SnapshotVersion)
	}
	if _, ok := data["agents"]; !ok {
		t.Error("expected agents field")
	}
	if _, ok := data["tasks"]; !ok {
		t.Error("expected tasks field")
	}
	if _, ok := data["recent_runs"]; !ok {
		t.Error("expected recent_runs field")
	}
}

func TestHandleAPIStateRejectsUnauthenticated(t *testing.T) {
	s, _ := newRawTestServer(t)

	req := httptest.NewRequest("GET", "/api/v1/state", nil)
	req.Host = testHost
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

// --- buildSnapshot (pure, directly unit-testable) ---

func TestBuildSnapshotStatusMappingUnrecognizedBecomesStopped(t *testing.T) {
	in := snapshotInput{
		Records: []agent.Record{
			{Name: "agent-a", Status: "zombie"},
		},
		Now: time.Now(),
	}
	snap := buildSnapshot(in)
	if len(snap.Agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(snap.Agents))
	}
	if snap.Agents[0].Status != observe.StatusStopped {
		t.Errorf("status = %q, want %q", snap.Agents[0].Status, observe.StatusStopped)
	}
}

// TestBuildSnapshotStatusMappingRestartingBecomesStarting guards against the
// snapshot path (buildAgent -> observe.MapStatus) and the event-stream path
// (service.toObserveStatus, since folded into observe.MapStatus too)
// disagreeing on "restarting": both must report "starting", the spec-mandated
// value, never "stopped".
func TestBuildSnapshotStatusMappingRestartingBecomesStarting(t *testing.T) {
	in := snapshotInput{
		Records: []agent.Record{
			{Name: "agent-a", Status: "restarting"},
		},
		Now: time.Now(),
	}
	snap := buildSnapshot(in)
	if len(snap.Agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(snap.Agents))
	}
	if snap.Agents[0].Status != observe.StatusStarting {
		t.Errorf("status = %q, want %q", snap.Agents[0].Status, observe.StatusStarting)
	}
}

func TestBuildSnapshotPrefersLiveProcessStateOverRecord(t *testing.T) {
	started := time.Now().Add(-time.Hour)
	in := snapshotInput{
		Records: []agent.Record{
			{Name: "agent-a", Status: "stopped", Restarts: 0, StartedAt: time.Time{}},
		},
		ProcessStates: map[string]ProcessStateInfo{
			"agent-a": {Name: "agent-a", Status: "running", Restarts: 3, StartedAt: started},
		},
		Now: time.Now(),
	}
	snap := buildSnapshot(in)
	got := snap.Agents[0]
	if got.Status != observe.StatusRunning {
		t.Errorf("status = %q, want running (live process state should win)", got.Status)
	}
	if got.Restarts != 3 {
		t.Errorf("restarts = %d, want 3", got.Restarts)
	}
	if !got.StartedAt.Equal(started) {
		t.Errorf("startedAt = %v, want %v", got.StartedAt, started)
	}
}

func TestBuildSnapshotFallsBackToRecordWhenNoLiveState(t *testing.T) {
	started := time.Now().Add(-2 * time.Hour)
	in := snapshotInput{
		Records: []agent.Record{
			{Name: "agent-a", Status: "stopped", Restarts: 1, StartedAt: started},
		},
		Now: time.Now(),
	}
	snap := buildSnapshot(in)
	got := snap.Agents[0]
	if got.Status != observe.StatusStopped {
		t.Errorf("status = %q, want stopped", got.Status)
	}
	if got.Restarts != 1 {
		t.Errorf("restarts = %d, want 1", got.Restarts)
	}
}

// TestBuildSnapshotWakeOnMessage locks in the wire shape a dormant agent
// reports: Status and WakeOnMessage must always agree, whether the agent
// went dormant via the idle sweep (wake_on_message: true) or a manual stop
// (wake_on_message: false) — and a stale WakeOnMessage=true on a record that
// isn't actually stopped must never leak onto the wire.
func TestBuildSnapshotWakeOnMessage(t *testing.T) {
	cases := []struct {
		name       string
		rec        agent.Record
		wantStatus observe.Status
		wantWake   bool
	}{
		{
			name:       "idle-swept dormant agent",
			rec:        agent.Record{Name: "agent-a", Status: "stopped", WakeOnMessage: true},
			wantStatus: observe.StatusStopped,
			wantWake:   true,
		},
		{
			name:       "manually stopped agent",
			rec:        agent.Record{Name: "agent-a", Status: "stopped", WakeOnMessage: false},
			wantStatus: observe.StatusStopped,
			wantWake:   false,
		},
		{
			name:       "running agent with a stale WakeOnMessage flag never reports it",
			rec:        agent.Record{Name: "agent-a", Status: "running", WakeOnMessage: true},
			wantStatus: observe.StatusRunning,
			wantWake:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap := buildSnapshot(snapshotInput{Records: []agent.Record{tc.rec}, Now: time.Now()})
			got := snap.Agents[0]
			if got.Status != tc.wantStatus || got.WakeOnMessage != tc.wantWake {
				t.Errorf("got (status=%q, wake=%v), want (status=%q, wake=%v)",
					got.Status, got.WakeOnMessage, tc.wantStatus, tc.wantWake)
			}
		})
	}
}

func TestBuildSnapshotActivityUnknownWhenProviderAbsent(t *testing.T) {
	in := snapshotInput{
		Records: []agent.Record{{Name: "agent-a", Status: "running"}},
		Now:     time.Now(),
	}
	snap := buildSnapshot(in)
	if snap.Agents[0].Activity != observe.ActivityUnknown {
		t.Errorf("activity = %q, want unknown", snap.Agents[0].Activity)
	}
}

func TestBuildSnapshotActivityFromProvider(t *testing.T) {
	lastActive := time.Now().Add(-5 * time.Second)
	in := snapshotInput{
		Records: []agent.Record{{Name: "agent-a", Status: "running"}},
		Activity: &fakeActivityProvider{activities: map[string]observe.AgentActivity{
			"agent-a": {
				Activity:       observe.ActivityWorking,
				LastActivityAt: lastActive,
				CurrentAction:  &observe.Action{Kind: observe.ActionKindPane, Detail: "go test ./..."},
			},
		}},
		Now: time.Now(),
	}
	snap := buildSnapshot(in)
	got := snap.Agents[0]
	if got.Activity != observe.ActivityWorking {
		t.Errorf("activity = %q, want working", got.Activity)
	}
	if got.LastActivityAt == nil || !got.LastActivityAt.Equal(lastActive) {
		t.Errorf("lastActivityAt = %v, want %v", got.LastActivityAt, lastActive)
	}
	if got.CurrentAction == nil || got.CurrentAction.Detail != "go test ./..." {
		t.Errorf("currentAction = %+v", got.CurrentAction)
	}
}

func TestBuildSnapshotResolvesModelAndHarnessThroughCascade(t *testing.T) {
	cfg := &config.Config{
		Defaults: config.DefaultsConfig{Model: "sonnet"},
		Templates: map[string]config.TemplateConfig{
			"coding": {Model: "opus"},
		},
	}
	in := snapshotInput{
		Config:  cfg,
		Records: []agent.Record{{Name: "agent-a", Template: "coding", Status: "running"}},
		Now:     time.Now(),
	}
	snap := buildSnapshot(in)
	got := snap.Agents[0]
	if got.Model != "opus" {
		t.Errorf("model = %q, want opus (template override)", got.Model)
	}
	if got.Harness != cfg.DefaultsHarness() {
		t.Errorf("harness = %q, want defaults harness %q", got.Harness, cfg.DefaultsHarness())
	}
}

func TestBuildSnapshotRecentRunsNewestFirstAndCapped(t *testing.T) {
	now := time.Now()
	entries := map[string][]history.Entry{
		"task-a": {
			{Task: "task-a", ExitCode: 0, Reason: history.ReasonSuccess, RunAt: now},
			{Task: "task-a", ExitCode: 1, Reason: history.ReasonFailure, RunAt: now.Add(-time.Hour)},
		},
		"task-b": {
			{Task: "task-b", ExitCode: 0, Reason: history.ReasonSuccess, RunAt: now.Add(-30 * time.Minute)},
		},
	}
	in := snapshotInput{History: entries, Now: now}
	snap := buildSnapshot(in)
	if len(snap.RecentRuns) != 3 {
		t.Fatalf("expected 3 runs, got %d", len(snap.RecentRuns))
	}
	for i := 1; i < len(snap.RecentRuns); i++ {
		if snap.RecentRuns[i].StartedAt.After(snap.RecentRuns[i-1].StartedAt) {
			t.Fatalf("recent runs not sorted newest-first at index %d", i)
		}
	}
	if snap.RecentRuns[0].Task != "task-a" || snap.RecentRuns[0].Status != observe.RunSucceeded {
		t.Errorf("newest run = %+v, want task-a succeeded", snap.RecentRuns[0])
	}
	failed := snap.RecentRuns[2]
	if failed.Status != observe.RunFailed || failed.Error == "" {
		t.Errorf("failed run = %+v, want non-empty error", failed)
	}
}

// fakeRunProvider is a test double for the runProvider seam.
type fakeRunProvider struct {
	runs []observe.TaskRun
}

func (f *fakeRunProvider) Recent(n int) []observe.TaskRun {
	if n <= 0 || n > len(f.runs) {
		return f.runs
	}
	return f.runs[:n]
}

func TestBuildSnapshotIncludesInFlightRunFromRunLog(t *testing.T) {
	now := time.Now()
	in := snapshotInput{
		Now: now,
		RunLog: &fakeRunProvider{runs: []observe.TaskRun{
			{ID: "task-a-1", Task: "task-a", Status: observe.RunRunning, StartedAt: now},
		}},
	}
	snap := buildSnapshot(in)
	if len(snap.RecentRuns) != 1 {
		t.Fatalf("expected 1 run, got %d", len(snap.RecentRuns))
	}
	if snap.RecentRuns[0].Status != observe.RunRunning {
		t.Errorf("status = %q, want running", snap.RecentRuns[0].Status)
	}
	if snap.RecentRuns[0].EndedAt != nil {
		t.Errorf("expected nil EndedAt for an in-flight run, got %v", snap.RecentRuns[0].EndedAt)
	}
}

func TestBuildSnapshotRunLogDedupesAgainstHistory(t *testing.T) {
	now := time.Now()
	started := now.Add(-time.Minute)
	ended := now
	duration := ended.Sub(started).Milliseconds()

	// Same firing recorded in both sources — the run log's copy (with honest
	// timing) must win, and it must not appear twice.
	entries := map[string][]history.Entry{
		"task-a": {{Task: "task-a", ExitCode: 0, Reason: history.ReasonSuccess, RunAt: ended, StartedAt: started, DurationMS: duration}},
	}
	in := snapshotInput{
		Now:     now,
		History: entries,
		RunLog: &fakeRunProvider{runs: []observe.TaskRun{
			{ID: fmt.Sprintf("task-a-%d", started.UnixNano()), Task: "task-a", Status: observe.RunSucceeded, StartedAt: started, EndedAt: &ended, DurationMS: &duration},
		}},
	}
	snap := buildSnapshot(in)
	if len(snap.RecentRuns) != 1 {
		t.Fatalf("expected 1 deduplicated run, got %d: %+v", len(snap.RecentRuns), snap.RecentRuns)
	}
}

func TestBuildSnapshotOmitsEndedAtForLegacyHistoryEntryWithoutTiming(t *testing.T) {
	now := time.Now()
	entries := map[string][]history.Entry{
		"task-a": {{Task: "task-a", ExitCode: 0, Reason: history.ReasonSuccess, RunAt: now}},
	}
	in := snapshotInput{Now: now, History: entries}
	snap := buildSnapshot(in)
	if len(snap.RecentRuns) != 1 {
		t.Fatalf("expected 1 run, got %d", len(snap.RecentRuns))
	}
	run := snap.RecentRuns[0]
	if run.EndedAt != nil {
		t.Errorf("expected nil EndedAt for a legacy entry with no timing info, got %v", run.EndedAt)
	}
	if run.DurationMS != nil {
		t.Errorf("expected nil DurationMS for a legacy entry with no timing info, got %v", run.DurationMS)
	}
	if run.StartedAt.IsZero() {
		t.Error("expected a best-effort StartedAt (RunAt) even for a legacy entry")
	}
}

func TestBuildSnapshotHistoryEntryWithTimingReportsHonestDuration(t *testing.T) {
	now := time.Now()
	started := now.Add(-2 * time.Second)
	entries := map[string][]history.Entry{
		"task-a": {{Task: "task-a", ExitCode: 0, Reason: history.ReasonSuccess, RunAt: now, StartedAt: started, DurationMS: 2000}},
	}
	in := snapshotInput{Now: now, History: entries}
	snap := buildSnapshot(in)
	run := snap.RecentRuns[0]
	if !run.StartedAt.Equal(started) {
		t.Errorf("StartedAt = %v, want %v", run.StartedAt, started)
	}
	if run.EndedAt == nil || !run.EndedAt.Equal(now) {
		t.Errorf("EndedAt = %v, want %v", run.EndedAt, now)
	}
	if run.DurationMS == nil || *run.DurationMS != 2000 {
		t.Errorf("DurationMS = %v, want 2000", run.DurationMS)
	}
}

func TestBuildSnapshotRecentRunsCappedAtMax(t *testing.T) {
	now := time.Now()
	var entries []history.Entry
	for i := 0; i < observe.MaxRecentRuns+10; i++ {
		entries = append(entries, history.Entry{
			Task:     "task-a",
			ExitCode: 0,
			Reason:   history.ReasonSuccess,
			RunAt:    now.Add(-time.Duration(i) * time.Minute),
		})
	}
	in := snapshotInput{History: map[string][]history.Entry{"task-a": entries}, Now: now}
	snap := buildSnapshot(in)
	if len(snap.RecentRuns) != observe.MaxRecentRuns {
		t.Fatalf("expected %d runs, got %d", observe.MaxRecentRuns, len(snap.RecentRuns))
	}
}

// --- GET /api/v1/events (SSE) ---

func startSSERequest(t *testing.T, s *Server) (*httptest.Server, *http.Response, *bufio.Reader) {
	t.Helper()
	ts := httptest.NewServer(s.httpServer.Handler)
	t.Cleanup(ts.Close)

	req, err := http.NewRequest("GET", ts.URL+"/api/v1/events", nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Host = testHost
	req.Header.Set("Authorization", "Bearer "+testAPIToken)

	resp, err := http.DefaultClient.Do(req) //nolint:bodyclose // closed via t.Cleanup below
	if err != nil {
		t.Fatalf("starting SSE request: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}

	return ts, resp, bufio.NewReader(resp.Body)
}

// readSSEFrame reads one "event: X\ndata: Y\n\n" or ": ping\n\n" frame.
func readSSEFrame(t *testing.T, r *bufio.Reader) (event, data string) {
	t.Helper()
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("reading SSE line: %v", err)
		}
		line = strings.TrimRight(line, "\n")
		if strings.HasPrefix(line, ": ") {
			return "", line // comment/heartbeat
		}
		if strings.HasPrefix(line, "event: ") {
			event = strings.TrimPrefix(line, "event: ")
			continue
		}
		if strings.HasPrefix(line, "data: ") {
			data = strings.TrimPrefix(line, "data: ")
			continue
		}
		if line == "" && event != "" {
			return event, data
		}
	}
}

func TestHandleAPIEventsRejectsUnauthenticated(t *testing.T) {
	s, _ := newRawTestServer(t)
	req := httptest.NewRequest("GET", "/api/v1/events", nil)
	req.Host = testHost
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestHandleAPIEventsSendsHelloOnConnect(t *testing.T) {
	s, _ := newTestServerWithObserveDeps(t, nil, nil)
	_, resp, r := startSSERequest(t, s)
	defer resp.Body.Close() //nolint:bodyclose // already deferred via t.Cleanup in startSSERequest; explicit close here satisfies the linter

	event, data := readSSEFrame(t, r)
	if event != string(observe.EventHello) {
		t.Fatalf("first event = %q, want hello", event)
	}
	var payload observe.HelloPayload
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		t.Fatalf("decoding hello payload: %v", err)
	}
	if payload.Version != observe.SnapshotVersion {
		t.Errorf("hello version = %d, want %d", payload.Version, observe.SnapshotVersion)
	}
}

// TestHandleAPIEventsHelloCarriesRealTimestamp guards against hello.At being
// left at its zero value: handleAPIEvents used to build HelloPayload without
// going through Bus.Publish, so the embedded observe.Meta (Seq, At) never got
// stamped and At shipped as "0001-01-01T00:00:00Z" on the wire.
func TestHandleAPIEventsHelloCarriesRealTimestamp(t *testing.T) {
	s, _ := newTestServerWithObserveDeps(t, nil, nil)
	before := time.Now().Add(-time.Second)
	_, resp, r := startSSERequest(t, s)
	defer resp.Body.Close() //nolint:bodyclose // already deferred via t.Cleanup in startSSERequest; explicit close here satisfies the linter
	after := time.Now().Add(time.Second)

	_, data := readSSEFrame(t, r)
	var payload observe.HelloPayload
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		t.Fatalf("decoding hello payload: %v", err)
	}
	if payload.At.IsZero() {
		t.Fatal("hello.at is zero, want a real timestamp")
	}
	if payload.At.Before(before) || payload.At.After(after) {
		t.Errorf("hello.at = %v, want between %v and %v", payload.At, before, after)
	}
}

// TestHandleAPIEventsHelloReflectsEventSourceSeq guards against hello.seq
// shipping as 0 on a daemon that has been running (and publishing) for a
// while: a consumer would read hello.seq as "nothing published yet" and see
// the next real event's much higher seq as a huge gap, triggering exactly
// the spurious resnapshot the monotonic-sequence work exists to prevent.
func TestHandleAPIEventsHelloReflectsEventSourceSeq(t *testing.T) {
	src := newFakeEventSource()
	src.seq = 4212
	s, _ := newTestServerWithObserveDeps(t, nil, src)
	_, resp, r := startSSERequest(t, s)
	defer resp.Body.Close() //nolint:bodyclose // already deferred via t.Cleanup in startSSERequest; explicit close here satisfies the linter

	_, data := readSSEFrame(t, r)
	var payload observe.HelloPayload
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		t.Fatalf("decoding hello payload: %v", err)
	}
	if payload.Seq != 4212 {
		t.Errorf("hello.seq = %d, want 4212", payload.Seq)
	}
}

// TestHandleAPIEventsHelloOnRealBusReflectsPublishedEvents exercises the real
// observe.Bus (not the fake) to confirm handleAPIEvents reads its actual
// current sequence rather than a fake's canned value.
func TestHandleAPIEventsHelloOnRealBusReflectsPublishedEvents(t *testing.T) {
	bus := observe.NewBus()
	for i := 0; i < 3; i++ {
		bus.Publish(observe.Event{
			Type:    observe.EventAgentStopped,
			Payload: &observe.AgentStoppedPayload{Agent: "leo-coding-leo"},
		})
	}

	s, _ := newTestServerWithObserveDeps(t, nil, bus)
	_, resp, r := startSSERequest(t, s)
	defer resp.Body.Close() //nolint:bodyclose // already deferred via t.Cleanup in startSSERequest; explicit close here satisfies the linter

	_, data := readSSEFrame(t, r)
	var payload observe.HelloPayload
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		t.Fatalf("decoding hello payload: %v", err)
	}
	if payload.Seq != 3 {
		t.Errorf("hello.seq = %d, want 3", payload.Seq)
	}
}

// TestHandleAPIEventsHelloWithNoEventSourceStillHasRealTimestamp locks in the
// documented "no bus wired" fallback: seq 0 is a defensible default (there is
// genuinely no bus to report a sequence for), but the timestamp must still be
// real rather than the zero value.
func TestHandleAPIEventsHelloWithNoEventSourceStillHasRealTimestamp(t *testing.T) {
	s, _ := newTestServerWithObserveDeps(t, nil, nil)
	_, resp, r := startSSERequest(t, s)
	defer resp.Body.Close() //nolint:bodyclose // already deferred via t.Cleanup in startSSERequest; explicit close here satisfies the linter

	_, data := readSSEFrame(t, r)
	var payload observe.HelloPayload
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		t.Fatalf("decoding hello payload: %v", err)
	}
	if payload.Seq != 0 {
		t.Errorf("hello.seq = %d, want 0 (no event source wired)", payload.Seq)
	}
	if payload.At.IsZero() {
		t.Fatal("hello.at is zero, want a real timestamp")
	}
}

func TestHandleAPIEventsStreamsPublishedEvents(t *testing.T) {
	src := newFakeEventSource()
	s, _ := newTestServerWithObserveDeps(t, nil, src)
	_, resp, r := startSSERequest(t, s)
	defer resp.Body.Close() //nolint:bodyclose // already deferred via t.Cleanup in startSSERequest; explicit close here satisfies the linter

	// Drain hello.
	readSSEFrame(t, r)

	src.ch <- observe.Event{
		Type:    observe.EventAgentStopped,
		Payload: &observe.AgentStoppedPayload{Agent: "leo-coding-leo"},
	}

	event, data := readSSEFrame(t, r)
	if event != string(observe.EventAgentStopped) {
		t.Fatalf("event = %q, want %q", event, observe.EventAgentStopped)
	}
	var payload observe.AgentStoppedPayload
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		t.Fatalf("decoding payload: %v", err)
	}
	if payload.Agent != "leo-coding-leo" {
		t.Errorf("agent = %q, want leo-coding-leo", payload.Agent)
	}
}

func TestHandleAPIEventsHeartbeat(t *testing.T) {
	s, _ := newTestServerWithObserveDeps(t, nil, nil)
	s.sseHeartbeat = 20 * time.Millisecond
	_, resp, r := startSSERequest(t, s)
	defer resp.Body.Close() //nolint:bodyclose // already deferred via t.Cleanup in startSSERequest; explicit close here satisfies the linter

	// Drain hello, then expect a heartbeat comment.
	readSSEFrame(t, r)
	event, data := readSSEFrame(t, r)
	if event != "" || data != ": ping" {
		t.Fatalf("expected heartbeat comment, got event=%q data=%q", event, data)
	}
}

// deadlineWriterRecorder is a minimal http.ResponseWriter that also
// implements the underlying SetWriteDeadline method http.ResponseController
// looks for, letting TestHandleAPIEventsSetsBoundedWriteDeadlinePerWrite
// observe every deadline handleAPIEvents sets without needing a real TCP
// connection to stall.
type deadlineWriterRecorder struct {
	mu        sync.Mutex
	header    http.Header
	body      bytes.Buffer
	deadlines []time.Time
}

func (w *deadlineWriterRecorder) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *deadlineWriterRecorder) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.Write(p)
}

func (w *deadlineWriterRecorder) WriteHeader(int) {}

func (w *deadlineWriterRecorder) Flush() {}

func (w *deadlineWriterRecorder) SetWriteDeadline(deadline time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.deadlines = append(w.deadlines, deadline)
	return nil
}

func (w *deadlineWriterRecorder) Deadlines() []time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]time.Time, len(w.deadlines))
	copy(out, w.deadlines)
	return out
}

// TestHandleAPIEventsSetsBoundedWriteDeadlinePerWrite guards finding #6: the
// handler used to clear the write deadline entirely (time.Time{}) once at
// connect, so a client that stalls mid-stream (stops reading, e.g. a full
// TCP receive window) would block the handler goroutine inside a write
// forever — a goroutine leak, since r.Context() never fires for a
// connection that never closes. Every write must instead get its own
// bounded, non-zero deadline.
func TestHandleAPIEventsSetsBoundedWriteDeadlinePerWrite(t *testing.T) {
	src := newFakeEventSource()
	s, _ := newTestServerWithObserveDeps(t, nil, src)
	s.sseHeartbeat = 5 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/api/v1/events", nil).WithContext(ctx)

	w := &deadlineWriterRecorder{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.handleAPIEvents(w, req)
	}()

	// Let the hello write and at least one heartbeat write happen.
	time.Sleep(30 * time.Millisecond)
	cancel()
	<-done

	deadlines := w.Deadlines()
	if len(deadlines) < 2 {
		t.Fatalf("expected multiple SetWriteDeadline calls (one per write), got %d", len(deadlines))
	}
	for i, d := range deadlines {
		if d.IsZero() {
			t.Fatalf("write deadline #%d must never be cleared entirely (zero time), got %v", i, d)
		}
	}
}

func TestHandleAPIEventsUnsubscribesOnDisconnect(t *testing.T) {
	src := newFakeEventSource()
	s, _ := newTestServerWithObserveDeps(t, nil, src)
	ts := httptest.NewServer(s.httpServer.Handler)
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/v1/events", nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Host = testHost
	req.Header.Set("Authorization", "Bearer "+testAPIToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("starting SSE request: %v", err)
	}
	r := bufio.NewReader(resp.Body)
	readSSEFrame(t, r) // hello

	cancel()
	resp.Body.Close()

	select {
	case <-src.unsubscribed:
	case <-time.After(2 * time.Second):
		t.Fatal("expected unsubscribe to be called after client disconnect")
	}
}

// newTestServerWithObserveDeps builds a server via newTestServer and rewires
// its handler with the given activity/event dependencies, since New() only
// accepts options at construction time. events accepts any eventSource
// implementation — the fake or a real *observe.Bus — so tests can exercise
// either.
func newTestServerWithObserveDeps(t *testing.T, activity observe.ActivityProvider, events eventSource) (*Server, string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "leo-web-observe-test-*")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	cfgPath := writeTestConfig(t, dir)

	processes := &mockProcesses{states: map[string]ProcessStateInfo{}}
	scheduler := &mockScheduler{entries: []cron.EntryInfo{}}
	reloader := &mockReloader{}

	var opts []Option
	if activity != nil {
		opts = append(opts, WithActivityProvider(activity))
	}
	if events != nil {
		opts = append(opts, WithEventSource(events))
	}

	s := New(cfgPath, processes, scheduler, reloader, nil, Options{Port: testPort, APIToken: testAPIToken}, opts...)

	rawHandler := s.httpServer.Handler
	s.httpServer.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorizeTestRequest(r)
		rawHandler.ServeHTTP(w, r)
	})
	return s, dir
}
