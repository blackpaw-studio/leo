package drain

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type resumeRecorder struct {
	mu     sync.Mutex
	events []string
	logs   []string
}

func (r *resumeRecorder) add(event string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *resumeRecorder) logf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, fmt.Sprintf(format, args...))
}

type recordingNotifier struct {
	rec  *resumeRecorder
	fail map[string]error
}

func (n recordingNotifier) Message(_ context.Context, name, text, id string) (Delivery, error) {
	if err := n.fail[name]; err != nil {
		return Delivery{}, err
	}
	n.rec.add("message " + name + ": " + text + " id=" + id)
	return Delivery{}, nil
}

func (recordingNotifier) Holds(string, string) (bool, error) { return false, nil }

// recordingStore logs each persisted state of the obligation into the same
// event stream as the messages, so tests see their order.
type recordingStore struct{ rec *resumeRecorder }

func (s recordingStore) Write(p PendingResume) error {
	s.rec.add(fmt.Sprintf("write agents=%v tasks=%v", p.Agents, p.DeferredTasks))
	return nil
}

func (s recordingStore) Remove() error {
	s.rec.add("remove")
	return nil
}

func resumeDeps(rec *resumeRecorder, exists map[string]bool, fail map[string]error) ResumeDeps {
	return ResumeDeps{
		Store:       recordingStore{rec},
		Exists:      func(name string) bool { return exists[name] },
		AwaitBridge: func(_ context.Context, name string) { rec.add("await " + name) },
		Notifier:    recordingNotifier{rec: rec, fail: fail},
		RunTasks:    func(names []string) { rec.add("run " + strings.Join(names, ",")) },
		Logf:        rec.logf,
	}
}

func TestResumeMessagesExistingAgentsThenRunsDeferredTasks(t *testing.T) {
	rec := &resumeRecorder{}
	p := PendingResume{Agents: []string{"a", "ghost", "b"}, DeferredTasks: []string{"heartbeat", "news"}}
	Resume(context.Background(), p, resumeDeps(rec, map[string]bool{"a": true, "b": true}, nil))
	want := []string{
		"await a", "message a: " + ResumeText + " id=",
		"write agents=[ghost b] tasks=[heartbeat news]",
		"write agents=[b] tasks=[heartbeat news]",
		"await b", "message b: " + ResumeText + " id=",
		"write agents=[] tasks=[heartbeat news]",
		"run heartbeat,news",
		"remove",
	}
	if !reflect.DeepEqual(rec.events, want) {
		t.Fatalf("events = %q, want %q", rec.events, want)
	}
	if len(rec.logs) == 0 || !strings.Contains(strings.Join(rec.logs, "\n"), "ghost") {
		t.Fatalf("missing agent not logged: %q", rec.logs)
	}
}

func TestResumeContinuesPastANotifierError(t *testing.T) {
	rec := &resumeRecorder{}
	p := PendingResume{Agents: []string{"a", "b"}}
	deps := resumeDeps(rec, map[string]bool{"a": true, "b": true}, map[string]error{"a": errors.New("no such agent")})
	Resume(context.Background(), p, deps)
	// a's resume failed: it stays the next boot's obligation.
	want := []string{
		"await a",
		"await b", "message b: " + ResumeText + " id=",
		"write agents=[a] tasks=[]",
	}
	if !reflect.DeepEqual(rec.events, want) {
		t.Fatalf("events = %q, want %q", rec.events, want)
	}
	if !strings.Contains(strings.Join(rec.logs, "\n"), "no such agent") {
		t.Fatalf("notifier error not logged: %q", rec.logs)
	}
}

// AwaitBridge is bounded by the caller and returns either way: a mod that
// never reconnects still gets the message, by the legacy path.
func TestResumeMessagesAfterAwaitBridgeGivesUp(t *testing.T) {
	rec := &resumeRecorder{}
	deps := resumeDeps(rec, map[string]bool{"a": true}, nil)
	deps.AwaitBridge = func(ctx context.Context, _ string) {
		ctx, cancel := context.WithCancel(ctx)
		cancel() // the bounded wait timed out
		<-ctx.Done()
		rec.add("await timed out")
	}
	Resume(context.Background(), PendingResume{Agents: []string{"a"}}, deps)
	want := []string{"await timed out", "message a: " + ResumeText + " id=", "write agents=[] tasks=[]", "remove"}
	if !reflect.DeepEqual(rec.events, want) {
		t.Fatalf("events = %q, want %q", rec.events, want)
	}
}

func TestResumeWithoutDeferredTasksSkipsRunTasks(t *testing.T) {
	rec := &resumeRecorder{}
	Resume(context.Background(), PendingResume{}, resumeDeps(rec, nil, nil))
	if want := []string{"remove"}; !reflect.DeepEqual(rec.events, want) {
		t.Fatalf("events = %q, want %q", rec.events, want)
	}
}

// Deferred tasks that ran leave the file even when an agent's resume
// failed, so the next boot does not run them twice.
func TestResumeDropsRunTasksButKeepsFailedAgents(t *testing.T) {
	rec := &resumeRecorder{}
	p := PendingResume{DrainID: "d1", Agents: []string{"a"}, DeferredTasks: []string{"heartbeat"}}
	Resume(context.Background(), p, resumeDeps(rec, map[string]bool{"a": true}, map[string]error{"a": errors.New("busy")}))
	want := []string{"await a", "run heartbeat", "write agents=[a] tasks=[]"}
	if !reflect.DeepEqual(rec.events, want) {
		t.Fatalf("events = %q, want %q", rec.events, want)
	}
}
