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

func resumeDeps(rec *resumeRecorder, exists map[string]bool, fail map[string]error) ResumeDeps {
	return ResumeDeps{
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
		"await b", "message b: " + ResumeText + " id=",
		"run heartbeat,news",
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
	want := []string{"await a", "await b", "message b: " + ResumeText + " id="}
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
	want := []string{"await timed out", "message a: " + ResumeText + " id="}
	if !reflect.DeepEqual(rec.events, want) {
		t.Fatalf("events = %q, want %q", rec.events, want)
	}
}

func TestResumeWithoutDeferredTasksSkipsRunTasks(t *testing.T) {
	rec := &resumeRecorder{}
	Resume(context.Background(), PendingResume{}, resumeDeps(rec, nil, nil))
	if len(rec.events) != 0 {
		t.Fatalf("events = %q, want none", rec.events)
	}
}
