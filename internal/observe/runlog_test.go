package observe

import (
	"sync"
	"testing"
	"time"
)

// recordingPub captures every event published to it, for assertions.
type recordingPub struct {
	mu     sync.Mutex
	events []Event
}

func (r *recordingPub) Publish(ev Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *recordingPub) Events() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Event, len(r.events))
	copy(out, r.events)
	return out
}

func startedPayload(id, task string, at time.Time) *TaskRunPayload {
	return &TaskRunPayload{Run: TaskRun{ID: id, Task: task, Status: RunRunning, StartedAt: at}}
}

func finishedPayload(id, task string, started, ended time.Time, status RunStatus, errText string) *TaskRunPayload {
	d := ended.Sub(started).Milliseconds()
	return &TaskRunPayload{Run: TaskRun{
		ID: id, Task: task, Status: status, StartedAt: started,
		EndedAt: &ended, DurationMS: &d, Error: errText,
	}}
}

func TestRunLogRecordsRunningThenTransitionsToSucceeded(t *testing.T) {
	log := NewRunLog(nil, 0)
	started := time.Now()
	log.Publish(Event{Type: EventTaskRunStarted, Payload: startedPayload("r1", "task-a", started)})

	recent := log.Recent(10)
	if len(recent) != 1 || recent[0].Status != RunRunning {
		t.Fatalf("expected 1 running run, got %+v", recent)
	}

	ended := started.Add(2 * time.Second)
	log.Publish(Event{Type: EventTaskRunSucceeded, Payload: finishedPayload("r1", "task-a", started, ended, RunSucceeded, "")})

	recent = log.Recent(10)
	if len(recent) != 1 {
		t.Fatalf("expected still 1 run (transition, not append), got %d", len(recent))
	}
	got := recent[0]
	if got.Status != RunSucceeded {
		t.Fatalf("expected RunSucceeded, got %s", got.Status)
	}
	if got.EndedAt == nil || !got.EndedAt.Equal(ended) {
		t.Fatalf("expected EndedAt %v, got %v", ended, got.EndedAt)
	}
	if got.DurationMS == nil || *got.DurationMS != 2000 {
		t.Fatalf("expected DurationMS 2000, got %v", got.DurationMS)
	}
}

func TestRunLogRecentNewestFirstAndBounded(t *testing.T) {
	log := NewRunLog(nil, 2)
	base := time.Now()
	log.Publish(Event{Type: EventTaskRunStarted, Payload: startedPayload("r1", "task-a", base)})
	log.Publish(Event{Type: EventTaskRunStarted, Payload: startedPayload("r2", "task-b", base.Add(time.Second))})
	log.Publish(Event{Type: EventTaskRunStarted, Payload: startedPayload("r3", "task-c", base.Add(2*time.Second))})

	recent := log.Recent(10)
	if len(recent) != 2 {
		t.Fatalf("expected capacity-bounded to 2, got %d", len(recent))
	}
	if recent[0].ID != "r3" || recent[1].ID != "r2" {
		t.Fatalf("expected newest-first [r3, r2], got [%s, %s]", recent[0].ID, recent[1].ID)
	}
}

func TestRunLogForwardsEventsToWrappedPublisher(t *testing.T) {
	next := &recordingPub{}
	log := NewRunLog(next, 0)

	ev := Event{Type: EventTaskRunStarted, Payload: startedPayload("r1", "task-a", time.Now())}
	log.Publish(ev)

	// Non-run events must also be forwarded unchanged.
	stopEv := Event{Type: EventAgentStopped, Payload: &AgentStoppedPayload{Agent: "agent-a"}}
	log.Publish(stopEv)

	got := next.Events()
	if len(got) != 2 {
		t.Fatalf("expected 2 forwarded events, got %d", len(got))
	}
	if got[0].Type != EventTaskRunStarted || got[1].Type != EventAgentStopped {
		t.Fatalf("unexpected forwarded events: %+v", got)
	}
}

// TestRunLogRecentReturnsDefensiveCopies mutates the *pointer* fields
// (EndedAt, DurationMS), not just a value field — Recent's old
// implementation copied the TaskRun struct but left EndedAt/DurationMS
// aliasing the log's own state, so a caller could mutate the log through
// its return value despite Task (a plain string field) being safely copied
// all along.
func TestRunLogRecentReturnsDefensiveCopies(t *testing.T) {
	log := NewRunLog(nil, 0)
	started := time.Now()
	ended := started.Add(time.Second)
	log.Publish(Event{Type: EventTaskRunStarted, Payload: startedPayload("r1", "task-a", started)})
	log.Publish(Event{Type: EventTaskRunSucceeded, Payload: finishedPayload("r1", "task-a", started, ended, RunSucceeded, "")})

	recent := log.Recent(10)
	recent[0].Task = "mutated"
	*recent[0].EndedAt = ended.Add(time.Hour)
	*recent[0].DurationMS = 999999

	again := log.Recent(10)
	if again[0].Task != "task-a" {
		t.Fatalf("expected internal state unaffected by caller mutation of Task, got %q", again[0].Task)
	}
	if !again[0].EndedAt.Equal(ended) {
		t.Fatalf("expected internal state unaffected by caller mutation of EndedAt, got %v, want %v", again[0].EndedAt, ended)
	}
	if *again[0].DurationMS != *finishedPayload("r1", "task-a", started, ended, RunSucceeded, "").Run.DurationMS {
		t.Fatalf("expected internal state unaffected by caller mutation of DurationMS, got %d", *again[0].DurationMS)
	}
}

func TestRunLogRecentZeroOrNegativeReturnsAll(t *testing.T) {
	log := NewRunLog(nil, 0)
	log.Publish(Event{Type: EventTaskRunStarted, Payload: startedPayload("r1", "task-a", time.Now())})
	log.Publish(Event{Type: EventTaskRunStarted, Payload: startedPayload("r2", "task-b", time.Now())})

	if got := len(log.Recent(0)); got != 2 {
		t.Fatalf("Recent(0) = %d entries, want 2 (all)", got)
	}
	if got := len(log.Recent(-1)); got != 2 {
		t.Fatalf("Recent(-1) = %d entries, want 2 (all)", got)
	}
}

func TestRunLogConcurrentPublishAndRecent(t *testing.T) {
	log := NewRunLog(nil, 50)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "run"
			started := time.Now()
			log.Publish(Event{Type: EventTaskRunStarted, Payload: startedPayload(id, "task", started)})
			log.Publish(Event{Type: EventTaskRunSucceeded, Payload: finishedPayload(id, "task", started, started.Add(time.Millisecond), RunSucceeded, "")})
			_ = log.Recent(10)
		}(i)
	}
	wg.Wait()
}

func TestRunLogIgnoresNonTaskRunPayloadsForRecording(t *testing.T) {
	log := NewRunLog(nil, 0)
	log.Publish(Event{Type: EventAgentStopped, Payload: &AgentStoppedPayload{Agent: "agent-a"}})
	if got := len(log.Recent(10)); got != 0 {
		t.Fatalf("expected 0 recorded runs from a non-run event, got %d", got)
	}
}
