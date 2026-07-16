package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
)

func TestAPIConsultReturnsResultSynchronously(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)
	s.consults.ExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "echo", `{"type":"result","result":"consultant says yes","is_error":false}`)
	}
	req := httptest.NewRequest("POST", "/api/consult", strings.NewReader(`{"from":"assistant","template":"coding","prompt":"opinion?"}`))
	w := httptest.NewRecorder()
	s.handleAPIConsult(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		OK   bool                                  `json:"ok"`
		Data struct{ Harness, Model, Text string } `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || !resp.OK {
		t.Fatalf("bad response %s (err %v)", w.Body.String(), err)
	}
	if resp.Data.Text != "consultant says yes" || resp.Data.Harness != "claude" {
		t.Fatalf("unexpected result %+v", resp.Data)
	}
}

func TestAPIConsultAllowsCallerWithoutSupervisedSession(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)
	s.consults.ExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "echo", `{"type":"result","result":"ok","is_error":false}`)
	}
	req := httptest.NewRequest("POST", "/api/consult", strings.NewReader(`{"from":"oneshot-task","template":"coding","prompt":"q"}`))
	w := httptest.NewRecorder()
	s.handleAPIConsult(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
}

func TestAPIConsultRejectsUnknownTemplate(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)
	req := httptest.NewRequest("POST", "/api/consult", strings.NewReader(`{"template":"nope","prompt":"q"}`))
	w := httptest.NewRecorder()
	s.handleAPIConsult(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", w.Code)
	}
}
