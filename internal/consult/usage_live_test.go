package consult

import (
	"context"
	"encoding/json"
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
