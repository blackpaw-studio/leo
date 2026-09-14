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
)

type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlineCleared bool
}

func (r *deadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	r.deadlineCleared = deadline.IsZero()
	return nil
}

func TestAPIConsultReturnsResultSynchronously(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)
	s.consults.ExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "echo", `{"type":"result","result":"consultant says yes","is_error":false}`)
	}
	req := httptest.NewRequest("POST", "/api/consult", strings.NewReader(`{"from":"assistant","template":"coding","prompt":"opinion?"}`))
	w := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
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
	if !w.deadlineCleared {
		t.Fatal("consult handler did not clear the server write deadline")
	}
}

func TestAPIConsultExecutionFailureIsBadGateway(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)
	s.consults.ExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "false")
	}
	req := httptest.NewRequest("POST", "/api/consult", strings.NewReader(`{"template":"coding","prompt":"q"}`))
	w := httptest.NewRecorder()
	s.handleAPIConsult(w, req)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502: %s", w.Code, w.Body.String())
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

func TestAPIDispatchLifecycle(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)
	s.consults.ExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "echo", `{"type":"result","result":"done","is_error":false}`)
	}
	w := httptest.NewRecorder()
	s.handleAPIDispatch(w, httptest.NewRequest("POST", "/api/dispatch", strings.NewReader(`{"from":"caller","template":"coding","prompt":"work","cwd":"/tmp"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	var started struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if started.Data.ID == "" {
		t.Fatal("missing dispatch id")
	}
	w = httptest.NewRecorder()
	s.handleAPIDispatchWait(w, httptest.NewRequest("GET", "/api/dispatch/wait?id="+started.Data.ID+"&timeout=2", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "done") {
		t.Fatalf("wait: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/dispatch/"+started.Data.ID, nil)
	req.SetPathValue("id", started.Data.ID)
	s.handleAPIDispatchGet(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("get: %d %s", w.Code, w.Body.String())
	}
}

func TestAPIDispatchRejectsMissingCWD(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)
	w := httptest.NewRecorder()
	s.handleAPIDispatch(w, httptest.NewRequest("POST", "/api/dispatch", strings.NewReader(`{"template":"coding","prompt":"work"}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", w.Code)
	}
}

func TestDispatchTimeout(t *testing.T) {
	jsonTimeout := 3.5
	if got, err := dispatchTimeout(&jsonTimeout, ""); err != nil || got != 3500*time.Millisecond {
		t.Fatalf("JSON timeout = %s, %v", got, err)
	}
	if got, err := dispatchTimeout(nil, "2"); err != nil || got != 2*time.Second {
		t.Fatalf("query timeout = %s, %v", got, err)
	}
	if _, err := dispatchTimeout(nil, "-1"); err == nil {
		t.Fatal("negative timeout was accepted")
	}
}
