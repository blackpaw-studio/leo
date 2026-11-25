package web

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/brief"
)

// briefContextServer is a hook server (agent leo-a, launch token tok-a)
// with a stored record and, when on, brief.enabled set in leo.yaml.
func briefContextServer(t *testing.T, on bool) (*Server, string) {
	t.Helper()
	s := newHookServer(t)
	if on {
		raw, err := os.ReadFile(s.configPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(s.configPath, append(raw, "\nbrief:\n  enabled: true\n"...), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	home, ok := s.homePath()
	if !ok {
		t.Fatal("no home")
	}
	if err := agentstore.Save(home, agentstore.Record{Name: "leo-a"}); err != nil {
		t.Fatal(err)
	}
	return s, home
}

func startCtx(t *testing.T, s *Server, source, sid string) string {
	t.Helper()
	return postContext(t, s, "tok-a", `{"hook_event_name":"SessionStart","source":"`+source+`","session_id":"`+sid+`"}`)
}

func promptCtx(t *testing.T, s *Server, sid string) string {
	t.Helper()
	return postContext(t, s, "tok-a", `{"hook_event_name":"UserPromptSubmit","session_id":"`+sid+`","prompt":"hi"}`)
}

func TestBriefContextSaysNothingWhileOff(t *testing.T) {
	s, home := briefContextServer(t, false)
	_ = agentstore.SetBrief(home, "leo-a", &brief.Brief{Goal: "g"})
	for i := 0; i < brief.NudgeEveryTurns; i++ {
		s.briefs.TurnCompleted("leo-a")
	}
	if got := startCtx(t, s, "clear", "sid-a"); got != "" {
		t.Errorf("session start = %q, want nothing", got)
	}
	if got := promptCtx(t, s, "sid-a"); got != "" {
		t.Errorf("prompt = %q, want no instruction and no nudge", got)
	}
}

func TestBriefContextInstructsAtSessionStartAndRestoresTheBriefAfterARebuild(t *testing.T) {
	s, home := briefContextServer(t, true)
	if got := startCtx(t, s, "startup", "sid-a"); got != brief.Instruction {
		t.Fatalf("startup = %q, want the instruction alone", got)
	}
	stored := &brief.Brief{Goal: "ship the brief", Status: "wiring hooks", Open: []string{"codex probe"}, UpdatedAt: time.Now()}
	if err := agentstore.SetBrief(home, "leo-a", stored); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"clear", "compact", "resume"} {
		got := startCtx(t, s, source, "sid-"+source)
		if !strings.HasPrefix(got, brief.Instruction) || !strings.Contains(got, "goal: ship the brief") || !strings.Contains(got, "- codex probe") || !strings.Contains(got, "after "+source) {
			t.Errorf("%s = %q, want the instruction and the stored brief", source, got)
		}
	}
	if got := startCtx(t, s, "startup", "sid-new"); strings.Contains(got, "ship the brief") {
		t.Errorf("a fresh startup restored a brief: %q", got)
	}
}

func TestBriefContextInstructsOncePerSessionOnPrompts(t *testing.T) {
	s, _ := briefContextServer(t, true)
	// Briefs switched on mid-session: no SessionStart told this session.
	if got := promptCtx(t, s, "sid-a"); got != brief.Instruction {
		t.Fatalf("first prompt = %q, want the instruction", got)
	}
	if got := promptCtx(t, s, "sid-a"); got != "" {
		t.Fatalf("second prompt = %q, want nothing", got)
	}
	startCtx(t, s, "startup", "sid-b")
	if got := promptCtx(t, s, "sid-b"); got != "" {
		t.Fatalf("a prompt after the session start told it = %q, want nothing", got)
	}
}

func TestBriefContextNudgesOncePerTenTurns(t *testing.T) {
	s, home := briefContextServer(t, true)
	startCtx(t, s, "startup", "sid-a")
	turn := func() { s.briefs.TurnCompleted("leo-a") }
	for i := 0; i < brief.NudgeEveryTurns-1; i++ {
		turn()
	}
	if got := promptCtx(t, s, "sid-a"); got != "" {
		t.Fatalf("prompt after 9 turns = %q, want nothing", got)
	}
	turn()
	if got := promptCtx(t, s, "sid-a"); got != brief.NudgeText(false) {
		t.Fatalf("prompt after 10 turns = %q, want the missing-brief nudge", got)
	}
	for i := 0; i < brief.NudgeEveryTurns-1; i++ {
		turn()
		if got := promptCtx(t, s, "sid-a"); got != "" {
			t.Fatalf("prompt %d turns after a nudge = %q, want nothing", i+1, got)
		}
	}
	_ = agentstore.SetBrief(home, "leo-a", &brief.Brief{Goal: "g"})
	turn()
	if got := promptCtx(t, s, "sid-a"); got != brief.NudgeText(true) {
		t.Fatalf("prompt at the next window = %q, want the stale nudge", got)
	}
}

func TestBriefContextIgnoresSubagentsAndUnknownTokens(t *testing.T) {
	s, _ := briefContextServer(t, true)
	if got := postContext(t, s, "tok-a", `{"hook_event_name":"SessionStart","source":"startup","session_id":"sid-a","agent_id":"sub"}`); got != "" {
		t.Errorf("subagent = %q", got)
	}
	if got := postContext(t, s, "tok-nope", `{"hook_event_name":"SessionStart","source":"startup","session_id":"sid-a"}`); got != "" {
		t.Errorf("unknown token = %q", got)
	}
}

func TestBriefContextJoinsWithDelegationContext(t *testing.T) {
	policy := delegationOn
	s := contextServer(t, &policy)
	raw, _ := os.ReadFile(s.configPath)
	_ = os.WriteFile(s.configPath, append(raw, "\nbrief:\n  enabled: true\n"...), 0o600)
	home, _ := s.homePath()
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-a"})

	got := startCtx(t, s, "startup", "sid-a")

	if !strings.HasPrefix(got, "SECTION v1") || !strings.HasSuffix(got, brief.Instruction) {
		t.Fatalf("context = %q, want delegation then brief", got)
	}
}
