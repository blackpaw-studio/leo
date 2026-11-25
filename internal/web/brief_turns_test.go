package web

import (
	"testing"

	"github.com/blackpaw-studio/leo/internal/brief"
)

// The staleness nudge counts finished turns of the agent's own session: a
// Stop or a StopFailure, not prompts, tool use or another session's Stop.
func TestHooksCountFinishedTurnsForTheBriefNudge(t *testing.T) {
	s := newHookServer(t)
	for _, body := range []string{
		`{"hook_event_name":"UserPromptSubmit","session_id":"sid-a"}`,
		`{"hook_event_name":"PostToolUse","session_id":"sid-a"}`,
		`{"hook_event_name":"Stop","session_id":"child-sid"}`, // a subagent's session
	} {
		postHook(t, s, "tok-a", body)
	}
	postHook(t, s, "tok-nope", `{"hook_event_name":"Stop","session_id":"sid-a"}`)
	for i := 0; i < brief.NudgeEveryTurns-1; i++ {
		postHook(t, s, "tok-a", `{"hook_event_name":"Stop","session_id":"sid-a"}`)
	}
	if s.briefs.Nudge("leo-a") {
		t.Fatal("nudge due before ten finished turns")
	}
	postHook(t, s, "tok-a", `{"hook_event_name":"StopFailure","session_id":"sid-a","error":"boom"}`)
	if !s.briefs.Nudge("leo-a") {
		t.Fatal("nine Stops and a StopFailure are ten finished turns")
	}
}
