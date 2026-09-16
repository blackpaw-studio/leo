package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/consult"
)

func TestResolveDispatchCallerUsesPrimaryPaneAndExactTmuxArgv(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)
	s.processes = &mockProcesses{states: map[string]ProcessStateInfo{"caller": {Name: "caller", Status: "running"}}}
	var calls [][]string
	s.execCommand = func(_ string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string(nil), args...))
		if len(calls) == 1 {
			return exec.Command("printf", "%%7\\n")
		}
		return exec.Command("printf", "leo-caller $9 @3\\n")
	}
	got, err := s.resolveDispatchCaller("caller", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != (dispatchCaller{PaneID: "%7", SessionID: "$9", WindowID: "@3", Harness: "claude"}) {
		t.Fatalf("caller = %#v", got)
	}
	want := [][]string{{"-L", "leo", "show-options", "-t", "=leo-caller:", "-v", "@leo_primary_pane"}, {"-L", "leo", "display-message", "-p", "-t", "%7", "#{session_name} #{session_id} #{window_id}"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("tmux argv = %#v, want %#v", calls, want)
	}
}

func TestDispatchRelease(t *testing.T) {
	state := t.TempDir()
	recorder := consult.NewFileRecorder(state)
	if err := os.MkdirAll(consult.Dir(state), 0o700); err != nil {
		t.Fatal(err)
	}
	s, _, _ := newTestServerWithAgents(t)
	s.consults = consult.NewDispatcher(recorder)
	for _, rec := range []consult.Record{{ID: "d-idle", Kind: "dispatch", Mode: consult.ModeInteractive, Status: consult.StatusIdle}, {ID: "d-running", Kind: "dispatch", Mode: consult.ModeInteractive, Status: consult.StatusRunning}} {
		if err := recorder.PersistRecord(rec); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(consult.StreamPath(state, rec.ID), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	call := func(id string) (int, string) {
		req := httptest.NewRequest(http.MethodPost, "/api/dispatch/"+id+"/release", nil)
		req.SetPathValue("id", id)
		w := httptest.NewRecorder()
		s.handleAPIDispatchRelease(w, req)
		return w.Code, w.Body.String()
	}
	if code, _ := call("missing"); code != http.StatusNotFound {
		t.Fatalf("missing=%d", code)
	}
	if code, _ := call("d-running"); code != http.StatusConflict {
		t.Fatalf("running=%d", code)
	}
	if code, body := call("d-idle"); code != http.StatusOK || !strings.Contains(body, "released") {
		t.Fatalf("idle=%d %s", code, body)
	}
	if code, _ := call("d-idle"); code != http.StatusOK {
		t.Fatalf("idempotent=%d", code)
	}
}

func TestResolveDispatchCallerRejectsPaneOutsideNamedSession(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)
	s.processes = &mockProcesses{states: map[string]ProcessStateInfo{"caller": {Name: "caller", Status: "running"}}}
	s.execCommand = func(_ string, _ ...string) *exec.Cmd { return exec.Command("printf", "leo-other $2 @2\\n") }
	if _, err := s.resolveDispatchCaller("caller", "%8"); err == nil || !strings.Contains(err.Error(), "does not belong") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolveDispatchCallerRequiresCanonicalPaneID(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)
	called := false
	s.execCommand = func(_ string, _ ...string) *exec.Cmd { called = true; return exec.Command("true") }
	if _, err := s.resolveDispatchCaller("", "leo-caller:0.0"); err == nil || !strings.Contains(err.Error(), "canonical %N pane id") {
		t.Fatalf("dynamic target error = %v", err)
	}
	if called {
		t.Fatal("tmux lookup called for dynamic target")
	}
	s.execCommand = func(_ string, _ ...string) *exec.Cmd { return exec.Command("printf", "leo-caller $2 @2\\n") }
	got, err := s.resolveDispatchCaller("", "%8")
	if err != nil || got.PaneID != "%8" {
		t.Fatalf("canonical pane result = %#v, %v", got, err)
	}
}

func TestResolveDispatchCallerDerivesHarnessFromExplicitPaneCommandExactArgv(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)
	var calls [][]string
	s.execCommand = func(_ string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string(nil), args...))
		if len(calls) == 1 {
			return exec.Command("printf", "outside $4 @4\\n")
		}
		return exec.Command("printf", "opencode\\n")
	}
	got, err := s.resolveDispatchCaller("", "%8")
	if err != nil {
		t.Fatal(err)
	}
	if got.Harness != "opencode" {
		t.Fatalf("caller=%+v", got)
	}
	want := []string{"-L", "leo", "display-message", "-p", "-t", "%8", "#{pane_current_command}"}
	if !reflect.DeepEqual(calls[1], want) {
		t.Fatalf("argv=%q want=%q", calls[1], want)
	}
}

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
		return exec.CommandContext(ctx, "echo", `{"type":"result","result":"done","is_error":false,"usage":{"input_tokens":3,"output_tokens":0},"total_cost_usd":0,"num_turns":0}`)
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
	var waited struct {
		Data []struct {
			ElapsedSeconds *float64        `json:"elapsed_seconds"`
			Elapsed        json.RawMessage `json:"elapsed"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &waited); err != nil {
		t.Fatalf("decode wait: %v", err)
	}
	if len(waited.Data) != 1 || waited.Data[0].ElapsedSeconds == nil || waited.Data[0].Elapsed != nil {
		t.Fatalf("wait elapsed shape = %+v", waited.Data)
	}
	w = httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/dispatch/"+started.Data.ID, nil)
	req.SetPathValue("id", started.Data.ID)
	s.handleAPIDispatchGet(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("get: %d %s", w.Code, w.Body.String())
	}
	var payload struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"input_tokens", "output_tokens", "cost_usd", "usage_turns"} {
		if string(payload.Data[key]) != "0" && (key != "input_tokens" || string(payload.Data[key]) != "3") {
			t.Fatalf("API %s = %s", key, payload.Data[key])
		}
	}
	if _, present := payload.Data["tool_calls"]; present {
		t.Fatalf("API emitted unknown tool_calls: %s", w.Body.String())
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

func TestAPIDispatchRejectsWorktreeIsolationOutsideGit(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)
	w := httptest.NewRecorder()
	s.handleAPIDispatch(w, httptest.NewRequest("POST", "/api/dispatch", strings.NewReader(`{"template":"coding","prompt":"work","cwd":"/tmp","isolation":"worktree"}`)))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `worktree isolation requires`) {
		t.Fatalf("response %d: %s", w.Code, w.Body.String())
	}
}

func TestAPIDispatchIgnoresUnknownJSONFields(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)
	s.consults.ExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "echo", `{"type":"result","result":"done","is_error":false}`)
	}
	w := httptest.NewRecorder()
	s.handleAPIDispatch(w, httptest.NewRequest("POST", "/api/dispatch", strings.NewReader(`{"template":"coding","prompt":"work","cwd":"/tmp","future_field":true}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
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

func TestDispatchReportHTTPMalformed(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)
	w := httptest.NewRecorder()
	s.handleAPIDispatchReport(w, httptest.NewRequest("POST", "/api/dispatch/d-nope/report", strings.NewReader(`{`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	// Unknown reports are deliberately acknowledged so hook retry loops stop.
	w = httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/dispatch/d-nope/report", strings.NewReader(`{"event_id":"e","payload":{}}`))
	req.SetPathValue("id", "d-nope")
	s.handleAPIDispatchReport(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
}
