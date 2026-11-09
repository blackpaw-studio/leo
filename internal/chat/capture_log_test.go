package chat

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// logRecorder keeps a Service's log lines.
type logRecorder struct {
	mu    sync.Mutex
	lines []string
}

func (r *logRecorder) logf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, fmt.Sprintf(format, args...))
}

// has reports whether a line contains every part.
func (r *logRecorder) has(parts ...string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, l := range r.lines {
		if containsAll(l, parts) {
			return true
		}
	}
	return false
}

func containsAll(s string, parts []string) bool {
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}

// Every capture decision at a Stop is logged with the agent, the message,
// what was done and why, so a reply that never reached chat can be traced.
func TestStopHookLogsEveryCaptureDecision(t *testing.T) {
	for _, tc := range []struct {
		name  string
		tr    transcript
		parts []string
	}{
		{"posted", transcript{}.user("[Leo Chat] status?").assistant("All green."), []string{"posted"}},
		{"suppressed", transcript{}.user("[Leo Chat] status?").chatPost().toolResult().assistant("Posted it."), []string{"suppressed", "leo_chat_post"}},
		{"pending", transcript{}.user("something else").assistant("x"), []string{"pending", "not in the transcript"}},
		{"dropped", transcript{}.user("[Leo Chat] status?").toolUse(), []string{"dropped", "no reply text"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			in, _ := f.svc.Send(context.Background(), "assistant", SendRequest{Text: "status?"})
			f.svc.Flush()
			f.svc.OnHook("assistant", HookEvent{Event: "Stop", TranscriptPath: tc.tr.write(t)})
			if want := append([]string{"chat: capture", in.ID, "assistant"}, tc.parts...); !f.logs.has(want...) {
				t.Fatalf("logs = %q, want a line with %q", f.logs.lines, want)
			}
		})
	}
}

func TestStopHookAfterCloseIsLogged(t *testing.T) {
	f := newFixture(t)
	f.svc.Close()
	f.svc.OnHook("assistant", HookEvent{Event: "Stop"})
	if !f.logs.has("chat:", "Stop hook", "assistant", "ignored", "shutting down") {
		t.Fatalf("logs = %q", f.logs.lines)
	}
}
