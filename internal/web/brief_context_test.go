package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/brief"
	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/observe"
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

// saveLaunchedRecord stores name's record as a launch holding token would
// have it (Save never writes the token; SetAttentionToken does).
func saveLaunchedRecord(t *testing.T, home, name, token string) {
	t.Helper()
	if err := agentstore.Save(home, agentstore.Record{Name: name}); err != nil {
		t.Fatal(err)
	}
	if err := agentstore.SetAttentionToken(home, name, token); err != nil {
		t.Fatal(err)
	}
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

// Codex hooks are home-global and child harness processes inherit the agent's
// token, so a SessionStart from some other process's session must neither get
// the agent's brief context nor be adopted as the agent's session.
func TestBriefContextIgnoresAForeignSessionOfAHomeGlobalHarness(t *testing.T) {
	s, home := briefContextServer(t, true)
	saveLaunchedRecord(t, home, "leo-b", "tok-b")
	svc := s.agentSvc.(*mockAgentService)
	svc.handles["leo-b"] = resolvedHandle{harnessName: "codex", handle: harness.SessionHandle{Name: "leo-b", IDs: staticIDs("sid-b")}}

	foreign := postContext(t, s, "tok-b", `{"hook_event_name":"SessionStart","source":"startup","session_id":"child-sid"}`)
	if foreign != "" {
		t.Errorf("a foreign session was told %q, want nothing", foreign)
	}
	if got := agentstore.ClaimedSession(home, "leo-b", "tok-b"); got != "" {
		t.Errorf("a foreign session was claimed as the agent's: %q", got)
	}
	if own := postContext(t, s, "tok-b", `{"hook_event_name":"SessionStart","source":"startup","session_id":"sid-b"}`); own != brief.Instruction {
		t.Errorf("the agent's own session was told %q, want the instruction", own)
	}
	if own := postContext(t, s, "tok-b", `{"hook_event_name":"UserPromptSubmit","session_id":"sid-b","prompt":"hi"}`); own != "" {
		t.Errorf("a told session's prompt = %q, want nothing", own)
	}
	if foreign := postContext(t, s, "tok-b", `{"hook_event_name":"UserPromptSubmit","session_id":"child-sid","prompt":"hi"}`); foreign != "" {
		t.Errorf("a foreign session's prompt = %q, want nothing", foreign)
	}
}

// Codex discovers its session id only after the first turn, so a start with no
// known id is the agent's own.
func TestBriefContextTellsACodexAgentBeforeItsSessionIDIsKnown(t *testing.T) {
	s, home := briefContextServer(t, true)
	saveLaunchedRecord(t, home, "leo-b", "tok-b")
	if got := postContext(t, s, "tok-b", `{"hook_event_name":"SessionStart","source":"startup","session_id":"first"}`); got != brief.Instruction {
		t.Errorf("first start = %q, want the instruction", got)
	}
}

// postContextPID is postContext with the reporting process's pid, which the
// relay sends alongside the hook payload.
func postContextPID(t *testing.T, s *Server, token string, pid int, payload string) string {
	t.Helper()
	body := `{"token":"` + token + `","pid":` + strconv.Itoa(pid) + `,"payload":` + payload + `}`
	req := httptest.NewRequest(http.MethodPost, "/api/agent/context", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	var resp struct{ Context string }
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding %s: %v", w.Body, err)
	}
	return resp.Context
}

// codexBriefServer is a brief-enabled server whose codex agent leo-b is bound
// to session sid-b, with a stored brief; pid 4242 is leo-b's own harness
// process and any other pid is not.
func codexBriefServer(t *testing.T) (*Server, string) {
	t.Helper()
	s, home := briefContextServer(t, true)
	saveLaunchedRecord(t, home, "leo-b", "tok-b")
	if err := agentstore.SetBrief(home, "leo-b", &brief.Brief{Goal: "ship it", UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	s.agentSvc.(*mockAgentService).handles["leo-b"] = resolvedHandle{harnessName: "codex", handle: harness.SessionHandle{Name: "leo-b", IDs: staticIDs("sid-b")}}
	s.ownsProcess = func(_ context.Context, agent string, pid int) bool { return agent == "leo-b" && pid == 4242 }
	return s, home
}

// A /clear in a codex agent starts a new session id. The agent's own harness
// process announces it, so the daemon rebinds: the brief is restored, nudges
// and the later turn hooks keep working against the new session.
func TestBriefContextRebindsAHomeGlobalAgentOnAClearFromItsOwnProcess(t *testing.T) {
	s, home := codexBriefServer(t)

	got := postContextPID(t, s, "tok-b", 4242, `{"hook_event_name":"SessionStart","source":"clear","session_id":"sid-b2"}`)

	if !strings.HasPrefix(got, brief.Instruction) || !strings.Contains(got, "goal: ship it") {
		t.Fatalf("clear = %q, want the instruction and the restored brief", got)
	}
	if claimed := agentstore.ClaimedSession(home, "leo-b", "tok-b"); claimed != "sid-b2" {
		t.Fatalf("claimed session = %q, want the new one", claimed)
	}
	for i := 0; i < brief.NudgeEveryTurns; i++ {
		s.briefs.TurnCompleted("leo-b")
	}
	if nudge := postContextPID(t, s, "tok-b", 4242, `{"hook_event_name":"UserPromptSubmit","session_id":"sid-b2","prompt":"hi"}`); nudge == "" {
		t.Error("the new session's prompt got no nudge, want one after 10 turns")
	}
	if code := postHook(t, s, "tok-b", `{"hook_event_name":"Stop","session_id":"sid-b2"}`); code < 200 || code > 299 {
		t.Fatalf("stop hook status %d", code)
	}
	if att, ok := s.attention.Get("leo-b"); !ok || att.State != observe.AttentionFinished {
		t.Errorf("attention after the new session's Stop = %+v, %v; want finished", att, ok)
	}
}

// A child harness process inherits the agent's token and fires the same
// hooks. Its session is not the agent's, whatever it announces.
func TestBriefContextRefusesAChildProcessesSessionTransition(t *testing.T) {
	for name, pid := range map[string]int{"another process": 9999, "no pid reported": 0} {
		t.Run(name, func(t *testing.T) {
			s, home := codexBriefServer(t)

			got := postContextPID(t, s, "tok-b", pid, `{"hook_event_name":"SessionStart","source":"clear","session_id":"child-sid"}`)

			if got != "" {
				t.Errorf("a child's session start was told %q, want nothing", got)
			}
			if claimed := agentstore.ClaimedSession(home, "leo-b", "tok-b"); claimed != "" {
				t.Errorf("a child's session was claimed: %q", claimed)
			}
			if prompt := postContextPID(t, s, "tok-b", pid, `{"hook_event_name":"UserPromptSubmit","session_id":"child-sid","prompt":"hi"}`); prompt != "" {
				t.Errorf("a child's prompt was told %q, want nothing", prompt)
			}
			postHook(t, s, "tok-b", `{"hook_event_name":"Stop","session_id":"child-sid"}`)
			if att, ok := s.attention.Get("leo-b"); ok && att.State == observe.AttentionFinished {
				t.Errorf("a child's Stop finished the agent's turn: %+v", att)
			}
		})
	}
}

// Only a SessionStart is a session transition: a prompt from a session the
// agent was never bound to is not rebound, even from its own process.
func TestBriefContextOnlyASessionStartRebinds(t *testing.T) {
	s, home := codexBriefServer(t)
	if got := postContextPID(t, s, "tok-b", 4242, `{"hook_event_name":"UserPromptSubmit","session_id":"sid-b2","prompt":"hi"}`); got != "" {
		t.Errorf("a prompt from an unbound session was told %q", got)
	}
	if claimed := agentstore.ClaimedSession(home, "leo-b", "tok-b"); claimed != "" {
		t.Errorf("a prompt rebound the session: %q", claimed)
	}
}

// agentOwnsProcess asks tmux for the agent's primary pane and that pane's
// process, takes one process-table snapshot, and judges the reporter by it.
func TestAgentOwnsProcessReadsThePaneRootAndTheProcessTable(t *testing.T) {
	s := newHookServer(t)
	const psTable = "  100     1 /opt/bin/codex\n  150   100 /bin/sh\n  200   150 /usr/local/bin/leo\n  160   150 /opt/bin/codex\n  210   160 /usr/local/bin/leo\n"
	var argvs [][]string
	s.execCommandContext = func(_ context.Context, name string, args ...string) *exec.Cmd {
		argvs = append(argvs, append([]string{name}, args...))
		switch {
		case slices.Contains(args, "show-options"):
			return exec.Command("printf", "%%7\n")
		case slices.Contains(args, "display-message"):
			return exec.Command("printf", "100\n")
		case name == "ps":
			return exec.Command("printf", "%s", psTable)
		}
		return exec.Command("false")
	}

	if !s.agentOwnsProcess(context.Background(), "leo-b", 200) {
		t.Error("the leo hook of the pane's harness was not recognised as its own")
	}
	if s.agentOwnsProcess(context.Background(), "leo-b", 210) {
		t.Error("the leo hook of a child harness was recognised as the agent's own")
	}
	var sawPaneRoot, sawTable bool
	for _, argv := range argvs {
		sawPaneRoot = sawPaneRoot || (slices.Contains(argv, "display-message") && slices.Contains(argv, "%7") && slices.Contains(argv, "#{pane_pid}"))
		sawTable = sawTable || argv[0] == "ps"
	}
	if !sawPaneRoot || !sawTable {
		t.Errorf("calls = %q, want a pane_pid query of the primary pane and a ps snapshot", argvs)
	}
}

// Any failure to establish ownership is a refusal, never a rebind.
func TestAgentOwnsProcessRefusesWhenTheHostCannotSayWhoOwnsIt(t *testing.T) {
	s := newHookServer(t)
	s.execCommandContext = func(context.Context, string, ...string) *exec.Cmd { return exec.Command("false") }
	if s.agentOwnsProcess(context.Background(), "leo-b", 200) {
		t.Fatal("ownership claimed although tmux and ps failed")
	}
}
