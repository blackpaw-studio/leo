package web

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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
}

func newFakeEventSource() *fakeEventSource {
	return &fakeEventSource{
		ch:           make(chan observe.Event, 8),
		unsubscribed: make(chan struct{}, 1),
	}
}

func (f *fakeEventSource) Subscribe(buffer int) (<-chan observe.Event, func()) {
	return f.ch, func() {
		select {
		case f.unsubscribed <- struct{}{}:
		default:
		}
	}
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
			{Name: "agent-a", Status: "suspended", Restarts: 1, StartedAt: started},
		},
		Now: time.Now(),
	}
	snap := buildSnapshot(in)
	got := snap.Agents[0]
	if got.Status != observe.StatusSuspended {
		t.Errorf("status = %q, want suspended", got.Status)
	}
	if got.Restarts != 1 {
		t.Errorf("restarts = %d, want 1", got.Restarts)
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
// accepts options at construction time.
func newTestServerWithObserveDeps(t *testing.T, activity observe.ActivityProvider, events *fakeEventSource) (*Server, string) {
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
