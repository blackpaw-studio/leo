package consult

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
)

// lockedTranscripts is a memTranscripts a test can change while a
// dispatch's watcher reads it.
type lockedTranscripts struct {
	mu    sync.Mutex
	files memTranscripts
	reads atomic.Int64
}

func (l *lockedTranscripts) read(path string, offset int64) ([]byte, int64, error) {
	l.reads.Add(1)
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.files.read(path, offset)
}

func (l *lockedTranscripts) set(path string, data []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.files[path] = append([]byte(nil), data...)
}

func startLiveDispatch(t *testing.T, files *lockedTranscripts, poll time.Duration) (*Dispatcher, string) {
	t.Helper()
	d := NewDispatcher(newFakeRecorder())
	d.liveUsage = liveUsageConfig{poll: poll, read: files.read, path: func(cwd, sid string) (string, error) { return "/derived/" + sid + ".jsonl", nil }}
	rt := &bridgedFakeRuntime{fakeInteractiveRuntime: &fakeInteractiveRuntime{arm: true, empty: true}, bridges: true}
	d.SetInteractiveRuntime(rt)
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "hello", Cwd: t.TempDir(), Mode: ModeInteractive})
	if err != nil {
		t.Fatal(err)
	}
	return d, started.ID
}

func liveHook(t *testing.T, eventID, event, prompt, transcript string) HookReport {
	t.Helper()
	payload := map[string]string{"hook_event_name": event, "session_id": "sess-1"}
	if prompt != "" {
		payload["prompt"] = prompt
	}
	if transcript != "" {
		payload["transcript_path"] = transcript
	}
	b, _ := json.Marshal(payload)
	return HookReport{EventID: eventID, Payload: b}
}

func refreshLive(t *testing.T, d *Dispatcher, id string) {
	t.Helper()
	_, s, err := d.lookup(id)
	if err != nil || s == nil {
		t.Fatalf("lookup %s: %v", id, err)
	}
	d.refreshLiveUsage(s)
}

func mustReport(t *testing.T, d *Dispatcher, id string, hr HookReport) {
	t.Helper()
	if err := d.Report(id, hr); err != nil {
		t.Fatal(err)
	}
}

// A running first turn shows the tokens its transcript has reported so far,
// marked incomplete, before any turn.complete arrives.
func TestLiveUsageShowsTheRunningTurnsTranscriptTokens(t *testing.T) {
	files := &lockedTranscripts{files: memTranscripts{"/t.jsonl": fixture(t, "streamed.jsonl")}}
	d, id := startLiveDispatch(t, files, time.Hour)
	mustReport(t, d, id, liveHook(t, "s1", "UserPromptSubmit", "hello", "/t.jsonl"))

	refreshLive(t, d, id)
	wantTokens(t, recordOf(t, d, id), 2312, 57, true)
}

// Without a transcript_path in the hooks (bridge events carry none) the
// transcript is found from the run's cwd and claude session id.
func TestLiveUsageDerivesTheTranscriptFromTheSession(t *testing.T) {
	files := &lockedTranscripts{files: memTranscripts{"/derived/sess-1.jsonl": fixture(t, "streamed.jsonl")}}
	d, id := startLiveDispatch(t, files, time.Hour)
	mustReport(t, d, id, liveHook(t, "s1", "UserPromptSubmit", "hello", ""))

	refreshLive(t, d, id)
	wantTokens(t, recordOf(t, d, id), 2312, 57, true)
}

// turn.complete's figures replace the live estimate, and a follow-up turn
// adds only what its own messages report: lines of the finished turn that
// no poll had read yet are not counted again in the next one.
func TestLiveUsageYieldsToTurnCompleteAndCountsFollowUpsOnce(t *testing.T) {
	streamed := fixture(t, "streamed.jsonl")
	files := &lockedTranscripts{files: memTranscripts{}}
	files.set("/t.jsonl", streamed[:len(streamed)/2])
	d, id := startLiveDispatch(t, files, time.Hour)
	mustReport(t, d, id, liveHook(t, "s1", "UserPromptSubmit", "hello", "/t.jsonl"))
	refreshLive(t, d, id)

	files.set("/t.jsonl", streamed) // the turn's tail, never polled
	mustReport(t, d, id, liveHook(t, "c1", "Stop", "", "/t.jsonl"))
	d.ApplyBridgeTokens(id, "turn.complete:1", &bridge.TurnTokens{Input: 2000, Output: 60})
	d.ApplyBridgeUsage(id, json.RawMessage(`{"cost":{"usd":0.5}}`))
	wantTokens(t, recordOf(t, d, id), 2000, 60, false)

	refreshLive(t, d, id)
	wantTokens(t, recordOf(t, d, id), 2000, 60, false)

	files.set("/t.jsonl", append(append([]byte(nil), streamed...), fixture(t, "next_turn.jsonl")...))
	mustReport(t, d, id, liveHook(t, "s2", "UserPromptSubmit", "follow up", "/t.jsonl"))
	refreshLive(t, d, id)
	rec := recordOf(t, d, id)
	wantTokens(t, rec, 2000+1323, 60+9, true)
	if rec.CostUSD == nil || *rec.CostUSD != 0.5 {
		t.Fatalf("cost = %v, want the last reported 0.5 kept", rec.CostUSD)
	}
}

// The watcher polls while the run lives and stops once it is terminal.
func TestLiveUsageWatcherStopsWhenTheRunEnds(t *testing.T) {
	files := &lockedTranscripts{files: memTranscripts{}}
	d, id := startLiveDispatch(t, files, time.Millisecond)
	mustReport(t, d, id, liveHook(t, "s1", "UserPromptSubmit", "hello", "/t.jsonl"))
	files.set("/t.jsonl", fixture(t, "streamed.jsonl"))
	waitFor(t, func() bool {
		rec := recordOf(t, d, id)
		return rec.InputTokens != nil && *rec.InputTokens == 2312
	})

	if _, err := d.Cancel(id); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return recordOf(t, d, id).Status.Terminal() })
	time.Sleep(20 * time.Millisecond) // let an in-flight poll finish
	before := files.reads.Load()
	time.Sleep(50 * time.Millisecond)
	if after := files.reads.Load(); after != before {
		t.Fatalf("watcher still polling after the run ended: %d reads became %d", before, after)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(time.Millisecond)
	}
}

// A follow-up sent right after a turn completes can append its usage before
// the turn's tokens are applied: the boundary is the transcript as it stood
// before the run went idle, so the follow-up's usage is still shown live.
func TestLiveUsageBoundaryIsTakenBeforeTheRunGoesIdle(t *testing.T) {
	streamed := fixture(t, "streamed.jsonl")
	files := &lockedTranscripts{files: memTranscripts{}}
	files.set("/t.jsonl", streamed)
	d, id := startLiveDispatch(t, files, time.Hour)
	mustReport(t, d, id, liveHook(t, "s1", "UserPromptSubmit", "hello", "/t.jsonl"))

	d.CatchUpLiveUsage(id) // as the bridge subscriber does ahead of the Stop
	mustReport(t, d, id, liveHook(t, "c1", "Stop", "", "/t.jsonl"))
	files.set("/t.jsonl", append(append([]byte(nil), streamed...), fixture(t, "next_turn.jsonl")...))
	d.ApplyBridgeTokens(id, "turn.complete:1", &bridge.TurnTokens{Input: 2000, Output: 60})

	mustReport(t, d, id, liveHook(t, "s2", "UserPromptSubmit", "follow up", "/t.jsonl"))
	refreshLive(t, d, id)
	wantTokens(t, recordOf(t, d, id), 2000+1323, 60+9, true)
}

// A catch-up that cannot read the transcript leaves the boundary where it
// was rather than moving it to a partial read.
func TestLiveUsageFailedCatchUpKeepsTheOldBoundary(t *testing.T) {
	streamed := fixture(t, "streamed.jsonl")
	files := &lockedTranscripts{files: memTranscripts{}}
	files.set("/t.jsonl", streamed[:len(streamed)/2])
	d, id := startLiveDispatch(t, files, time.Hour)
	mustReport(t, d, id, liveHook(t, "s1", "UserPromptSubmit", "hello", "/t.jsonl"))
	refreshLive(t, d, id)

	d.liveUsage.read = func(string, int64) ([]byte, int64, error) { return nil, 0, errors.New("boom") }
	d.ApplyBridgeTokens(id, "turn.complete:1", &bridge.TurnTokens{Input: 2000, Output: 60})
	_, s, _ := d.lookup(id)
	d.mu.Lock()
	baseIn, baseOut := s.live.baseIn, s.live.baseOut
	d.mu.Unlock()
	if baseIn != 0 || baseOut != 0 {
		t.Fatalf("boundary moved to %d/%d after a failed catch-up", baseIn, baseOut)
	}
}

type liveOrderLog struct{ tokenLog }

func (l *liveOrderLog) Report(id string, hr HookReport) error {
	l.mu.Lock()
	l.order = append(l.order, "report")
	l.mu.Unlock()
	return l.reportLog.Report(id, hr)
}

func (l *liveOrderLog) CatchUpLiveUsage(string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.order = append(l.order, "catchup")
}

// The subscriber catches the live usage up before the Stop it reports can
// let a follow-up in.
func TestDispatchBridgeSubscriberCatchesUpLiveUsageBeforeReportingTurnComplete(t *testing.T) {
	g := newBridgeRig(t, "2.1.289")
	g.launch(t, "d-live", "claude", "brief")
	var log liveOrderLog
	g.hub.AddSubscriber(g.r.DispatchBridgeSubscriber(&log))
	key := DispatchBridgeKey("d-live")
	g.connectAndAckOpening(t, key)
	if err := g.apply(t, key, bridge.Report{Type: bridge.ReportEvent, Name: bridge.EventTurnComplete, EventID: "turn.complete:t1"}); err != nil {
		t.Fatal(err)
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	want := []string{"catchup", "report", "tokens:turn.complete:t1"}
	if len(log.order) != len(want) || log.order[0] != want[0] || log.order[1] != want[1] || log.order[2] != want[2] {
		t.Fatalf("calls = %v, want %v", log.order, want)
	}
}
