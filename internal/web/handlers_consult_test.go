package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/harness"
)

func TestDeliverConsultReplyLiveAgent(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)
	var gotSession, gotBody string
	s.injectPrompt = func(_ context.Context, session, body string) error {
		gotSession, gotBody = session, body
		return nil
	}
	// "assistant" is live in mockProcesses.states.
	if err := s.deliverConsultReply(context.Background(), "assistant", "[consult c-1] hi"); err != nil {
		t.Fatalf("deliverConsultReply: %v", err)
	}
	if gotSession != agent.SessionName("assistant") || gotBody != "[consult c-1] hi" {
		t.Fatalf("injected (%q, %q)", gotSession, gotBody)
	}
}

func TestDeliverConsultReplyResumesSuspendedCaller(t *testing.T) {
	s, _, svc := newTestServerWithAgents(t)
	var gotSession string
	s.injectPrompt = func(_ context.Context, session, _ string) error {
		gotSession = session
		return nil
	}
	// "leo-coding-leo" exists as a record but is NOT in processes.States()
	// → delivery must resume it first, then inject.
	if err := s.deliverConsultReply(context.Background(), "leo-coding-leo", "body"); err != nil {
		t.Fatalf("deliverConsultReply: %v", err)
	}
	if !svc.resumeCalled || svc.resumeName != "leo-coding-leo" {
		t.Fatalf("expected resume of caller, got %+v", svc)
	}
	if gotSession != agent.SessionName("leo-coding-leo") {
		t.Fatalf("injected into %q", gotSession)
	}
}

func TestDeliverConsultReplyUnknownCaller(t *testing.T) {
	s, _, svc := newTestServerWithAgents(t)
	svc.resumeErr = &agent.ErrNotFound{Query: "ghost"}
	if err := s.deliverConsultReply(context.Background(), "ghost", "body"); err == nil {
		t.Fatal("expected error for unknown caller")
	}
}

// TestDeliverConsultReplyNonClaudeDriver verifies that a consult reply
// destined for a caller resolving to a non-claude harness is delivered via
// driver.Inject (bypassing the tmux/readiness-probing injectPrompt path
// entirely).
func TestDeliverConsultReplyNonClaudeDriver(t *testing.T) {
	drv := registerFakeTurnsHarness()
	drv.mu.Lock()
	drv.injects = nil
	drv.result = &harness.Result{Text: "turn done", SessionID: "thread-1"}
	drv.err = nil
	drv.mu.Unlock()

	s, _, svc := newTestServerWithAgents(t)
	wantHandle := harness.SessionHandle{
		Kind:        harness.KindAgent,
		Name:        "codex-worker",
		TmuxSession: agent.SessionName("codex-worker"),
		Workspace:   "/tmp/codex-worker",
	}
	svc.handles = map[string]resolvedHandle{
		"codex-worker": {harnessName: fakeTurnsHarnessName, handle: wantHandle},
	}

	s.injectPrompt = func(context.Context, string, string) error {
		t.Fatal("injectPrompt should not be called for a non-claude caller")
		return nil
	}

	if err := s.deliverConsultReply(context.Background(), "codex-worker", "[consult c-1] hi"); err != nil {
		t.Fatalf("deliverConsultReply: %v", err)
	}

	drv.mu.Lock()
	defer drv.mu.Unlock()
	if len(drv.injects) != 1 {
		t.Fatalf("expected exactly one Inject call, got %d", len(drv.injects))
	}
	got := drv.injects[0]
	if got.msg != "[consult c-1] hi" {
		t.Errorf("Inject msg = %q, want %q", got.msg, "[consult c-1] hi")
	}
	if got.handle.Name != wantHandle.Name || got.handle.TmuxSession != wantHandle.TmuxSession || got.handle.Workspace != wantHandle.Workspace {
		t.Errorf("Inject handle = %+v, want %+v", got.handle, wantHandle)
	}
}

func TestAPIConsultDispatchesAndDelivers(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)

	delivered := make(chan string, 1)
	s.injectPrompt = func(_ context.Context, _, body string) error {
		delivered <- body
		return nil
	}
	s.consults.ExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "echo", `{"type":"result","result":"gpt says yes","is_error":false}`)
	}

	// testConfigWithTemplatesYAML defines a "coding" template.
	body := `{"from":"assistant","template":"coding","prompt":"opinion?"}`
	req := httptest.NewRequest("POST", "/api/consult", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleAPIConsult(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		OK   bool `json:"ok"`
		Data struct {
			ID      string `json:"id"`
			Harness string `json:"harness"`
			Model   string `json:"model"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || !resp.OK || resp.Data.ID == "" {
		t.Fatalf("bad response %s (err %v)", w.Body.String(), err)
	}

	select {
	case got := <-delivered:
		if !strings.Contains(got, "gpt says yes") || !strings.Contains(got, resp.Data.ID) {
			t.Fatalf("unexpected delivery %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reply never delivered")
	}
}

func TestAPIConsultRejectsUnknownCaller(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)
	body := `{"from":"ghost","template":"coding","prompt":"q"}`
	req := httptest.NewRequest("POST", "/api/consult", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleAPIConsult(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", w.Code)
	}
}

func TestAPIConsultRejectsUnknownTemplate(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)
	body := `{"from":"assistant","template":"nope","prompt":"q"}`
	req := httptest.NewRequest("POST", "/api/consult", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleAPIConsult(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", w.Code)
	}
}
