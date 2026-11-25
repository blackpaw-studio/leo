package web

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/brief"
)

// reportOffered is what an agent's MCP server does when it lists or withdraws
// leo_brief: POST /api/agent/brief/offered with its own launch token.
func reportOffered(t *testing.T, s *Server, bearer, body string) int {
	t.Helper()
	return requestAs(t, s, http.MethodPost, "/api/agent/brief/offered", bearer, body).Code
}

func offeredBody(token string, offered bool) string {
	raw, _ := json.Marshal(map[string]any{"token": token, "offered": offered})
	return string(raw)
}

func dueNudge(s *Server, agent string) {
	for i := 0; i < brief.NudgeEveryTurns; i++ {
		s.briefs.TurnCompleted(agent)
	}
}

// An MCP server that never reported (an old binary, a launch before briefs
// were on that has not heard of the flip) is never told to use a tool it does
// not have, however long briefs have been on.
func TestBriefGuidanceSilentUntilTheMCPServerReportsTheTool(t *testing.T) {
	s, home := unreportedBriefContextServer(t, true)
	_ = agentstore.SetBrief(home, "leo-a", &brief.Brief{Goal: "g"})
	dueNudge(s, "leo-a")
	for _, source := range []string{"startup", "clear", "compact", "resume"} {
		if got := startCtx(t, s, source, "sid-"+source); got != "" {
			t.Errorf("session start (%s) = %q, want nothing from a server that never reported", source, got)
		}
	}
	if got := promptCtx(t, s, "sid-a"); got != "" {
		t.Errorf("prompt = %q, want no instruction and no nudge", got)
	}
}

func TestBriefGuidanceFollowsWhatTheMCPServerOffers(t *testing.T) {
	s, home := unreportedBriefContextServer(t, true)
	_ = agentstore.SetBrief(home, "leo-a", &brief.Brief{Goal: "kept"})
	if got := promptCtx(t, s, "sid-a"); got != "" {
		t.Fatalf("before the report: %q", got)
	}

	if code := reportOffered(t, s, "", offeredBody("tok-a", true)); code != http.StatusOK {
		t.Fatalf("report on: status %d", code)
	}
	if got := promptCtx(t, s, "sid-a"); got != brief.Instruction {
		t.Fatalf("first prompt after the report = %q, want the instruction", got)
	}
	if got := startCtx(t, s, "clear", "sid-b"); got == "" || got == brief.Instruction {
		t.Fatalf("clear after the report = %q, want the instruction and the stored brief", got)
	}
	dueNudge(s, "leo-a")
	if got := promptCtx(t, s, "sid-b"); got == "" {
		t.Fatal("no nudge once reported and due")
	}

	if code := reportOffered(t, s, "", offeredBody("tok-a", false)); code != http.StatusOK {
		t.Fatalf("report off: status %d", code)
	}
	dueNudge(s, "leo-a")
	if got := promptCtx(t, s, "sid-b"); got != "" {
		t.Errorf("prompt after the tool was withdrawn = %q, want nothing", got)
	}
	if got := startCtx(t, s, "resume", "sid-c"); got != "" {
		t.Errorf("resume after the tool was withdrawn = %q, want nothing", got)
	}
}

// A report from a previous launch of the agent says nothing about the new
// launch: its MCP server has to report for itself.
func TestBriefOfferedIsPerLaunch(t *testing.T) {
	s, _ := unreportedBriefContextServer(t, true)
	reportOffered(t, s, "", offeredBody("tok-a", true))
	s.attention.RegisterToken("tok-a2", "leo-a")
	if got := postContext(t, s, "tok-a2", `{"hook_event_name":"SessionStart","source":"startup","session_id":"sid-n"}`); got != "" {
		t.Errorf("a new launch inherited the old launch's report: %q", got)
	}
}

// The capability is the caller's own: the agent is whoever the token names,
// whatever else the body says.
func TestBriefOfferedCannotBeSetForAnotherAgent(t *testing.T) {
	s, _, _ := briefServer(t, true)
	if code := reportOffered(t, s, testAgentToken, `{"token":"tok-a","agent":"beta","offered":true}`); code != http.StatusOK && code != http.StatusBadRequest {
		t.Fatalf("status %d", code)
	}
	if s.briefOffers.offered("tok-b") {
		t.Error("alpha's token set beta's capability")
	}
	if !s.briefOffers.offered("tok-a") {
		t.Fatal("alpha's own report did not stick")
	}
	if code := reportOffered(t, s, testAgentToken, `{"token":"tok-b","agent":"alpha","offered":false}`); code != http.StatusOK && code != http.StatusBadRequest {
		t.Fatalf("status %d", code)
	}
	if !s.briefOffers.offered("tok-a") {
		t.Error("beta's token withdrew alpha's capability")
	}
	if code := reportOffered(t, s, testAgentToken, offeredBody("nope", true)); code != http.StatusForbidden {
		t.Errorf("unknown token: status %d, want 403", code)
	}
}

// Renaming a live agent moves its token mapping; what its MCP server offers
// has to follow, since that server only re-reports on a reconnect or a flip.
func TestBriefOfferedSurvivesALiveRename(t *testing.T) {
	s, home := unreportedBriefContextServer(t, true)
	reportOffered(t, s, "", offeredBody("tok-a", true))
	if err := agentstore.Save(home, agentstore.Record{Name: "leo-z"}); err != nil {
		t.Fatal(err)
	}

	s.attention.Move("leo-a", "leo-z")

	if got := postContext(t, s, "tok-a", `{"hook_event_name":"SessionStart","source":"startup","session_id":"sid-r"}`); got != brief.Instruction {
		t.Fatalf("session start after the rename = %q, want the instruction", got)
	}
	dueNudge(s, "leo-z")
	if got := postContext(t, s, "tok-a", `{"hook_event_name":"UserPromptSubmit","session_id":"sid-r","prompt":"hi"}`); got != brief.NudgeText(false) {
		t.Fatalf("prompt after the rename = %q, want the nudge", got)
	}
}

// SessionStart (resume, clear, compact) usually fires before the MCP server has
// reported, so the restore has to wait for the report instead of being lost.
func TestBriefRestoreWaitsForTheReportAfterAResume(t *testing.T) {
	for _, source := range []string{"resume", "clear", "compact"} {
		t.Run(source, func(t *testing.T) {
			s, home := unreportedBriefContextServer(t, true)
			_ = agentstore.SetBrief(home, "leo-a", &brief.Brief{Goal: "kept"})
			if got := startCtx(t, s, source, "sid-r"); got != "" {
				t.Fatalf("session start before the report = %q, want nothing", got)
			}
			reportOffered(t, s, "", offeredBody("tok-a", true))
			got := promptCtx(t, s, "sid-r")
			if !strings.HasPrefix(got, brief.Instruction) || !strings.Contains(got, "goal: kept") || !strings.Contains(got, "after "+source) {
				t.Fatalf("first prompt after the report = %q, want the instruction and the stored brief", got)
			}
			if got := promptCtx(t, s, "sid-r"); got != "" {
				t.Errorf("second prompt = %q, want the restore delivered once", got)
			}
		})
	}
}

func TestBriefPendingRestoreIsDroppedByANotOfferedReportOrANewSessionStart(t *testing.T) {
	cases := map[string]func(t *testing.T, s *Server){
		"not offered report": func(t *testing.T, s *Server) { reportOffered(t, s, "", offeredBody("tok-a", false)) },
		"new session start":  func(t *testing.T, s *Server) { startCtx(t, s, "startup", "sid-new") },
	}
	for name, interrupt := range cases {
		t.Run(name, func(t *testing.T) {
			s, home := unreportedBriefContextServer(t, true)
			_ = agentstore.SetBrief(home, "leo-a", &brief.Brief{Goal: "kept"})
			startCtx(t, s, "resume", "sid-r")
			interrupt(t, s)
			reportOffered(t, s, "", offeredBody("tok-a", true))
			if got := promptCtx(t, s, "sid-n"); got != brief.Instruction {
				t.Fatalf("prompt = %q, want the instruction without the dropped restore", got)
			}
		})
	}
}

// A retired token's entries go with it, so they do not pile up.
func TestBriefOfferedEntriesAreDroppedWhenATokenIsRetired(t *testing.T) {
	s, home := unreportedBriefContextServer(t, true)
	_ = agentstore.SetBrief(home, "leo-a", &brief.Brief{Goal: "kept"})
	startCtx(t, s, "resume", "sid-r")
	reportOffered(t, s, "", offeredBody("tok-a", true))
	s.attention.UnregisterToken("tok-a")
	s.attention.RegisterToken("tok-b2", "leo-b")
	reportOffered(t, s, "", offeredBody("tok-b2", true))
	if s.briefOffers.offered("tok-a") || s.briefOffers.hasState("tok-a") {
		t.Error("a retired token's state was kept")
	}
}
