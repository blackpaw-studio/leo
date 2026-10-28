package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/agentmove"
)

type fakeMoveJobs struct {
	reqs []agentmove.Request
	err  error
	jobs map[string]agentmove.JobStatus
}

func (f *fakeMoveJobs) Get(id string) (agentmove.JobStatus, bool) {
	st, ok := f.jobs[id]
	return st, ok
}

func (f *fakeMoveJobs) Start(_ context.Context, req agentmove.Request) (agentmove.JobStatus, error) {
	f.reqs = append(f.reqs, req)
	if f.err != nil {
		return agentmove.JobStatus{}, f.err
	}
	return agentmove.JobStatus{ID: "mv-1", Request: req, State: agentmove.JobRunning}, nil
}

func TestAPIAgentMoveStartsJob(t *testing.T) {
	s, _, svc := newTestServerWithAgents(t)
	svc.records = []agent.Record{{Name: "leo-coding-leo", Status: "running"}}
	if w := postJSON(t, s, "/api/agent/leo-coding-leo/move", `{"mode":"move","to":"laptop"}`); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unwired = %d, want 503", w.Code)
	}
	jobs := &fakeMoveJobs{}
	s.moveJobs = jobs

	w := postJSON(t, s, "/api/agent/leo-coding-leo/move", `{"agent":"other","mode":"copy","to":"laptop","name":"c","branch":"b","force":true}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d: %s", w.Code, w.Body)
	}
	var resp struct {
		OK   bool                `json:"ok"`
		Data agentmove.JobStatus `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || !resp.OK || resp.Data.ID != "mv-1" {
		t.Fatalf("body = %s (%v)", w.Body, err)
	}
	want := agentmove.Request{Agent: "leo-coding-leo", Mode: "copy", To: "laptop", Name: "c", Branch: "b", Force: true}
	if jobs.reqs[0] != want {
		t.Fatalf("request = %+v, want %+v (the path names the agent)", jobs.reqs[0], want)
	}

	jobs.err = agentmove.Problems{{Code: agentmove.CodeAgentMoving, Message: "busy"}}
	w = postJSON(t, s, "/api/agent/leo-coding-leo/move", `{"mode":"move","to":"laptop"}`)
	var refused struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &refused); err != nil || w.Code != http.StatusConflict || refused.Code != agentmove.CodeAgentMoving {
		t.Fatalf("refused = %d %s", w.Code, w.Body)
	}
	if w := postJSON(t, s, "/api/agent/nobody/move", `{"mode":"move"}`); w.Code != http.StatusNotFound {
		t.Fatalf("unknown agent = %d", w.Code)
	}
}

func TestAPIAgentMoveJobStatus(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)
	if w := getPath(t, s, "/api/agent/moves/mv-1"); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unwired = %d, want 503", w.Code)
	}
	s.moveJobs = &fakeMoveJobs{jobs: map[string]agentmove.JobStatus{
		"mv-1": {ID: "mv-1", Request: agentmove.Request{Agent: "a", Mode: "copy"}, State: agentmove.JobDone},
	}}

	w := getPath(t, s, "/api/agent/moves/mv-1")
	var resp struct {
		OK   bool                `json:"ok"`
		Data agentmove.JobStatus `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || w.Code != http.StatusOK || !resp.OK ||
		resp.Data.ID != "mv-1" || resp.Data.State != agentmove.JobDone || resp.Data.Request.Mode != "copy" {
		t.Fatalf("found = %d %s (%v)", w.Code, w.Body, err)
	}
	if w := getPath(t, s, "/api/agent/moves/mv-gone"); w.Code != http.StatusNotFound {
		t.Fatalf("unknown id = %d, want 404", w.Code)
	}
}

// TestAPIAgentMoveJobStatusAuthScope: the poll takes the same tokens as the
// POST that started the job — operator, agent and paired device — and no
// token is refused.
func TestAPIAgentMoveJobStatusAuthScope(t *testing.T) {
	s, store, _ := newDeviceTestServer(t)
	s.moveJobs = &fakeMoveJobs{jobs: map[string]agentmove.JobStatus{"mv-1": {ID: "mv-1", State: agentmove.JobRunning}}}
	dev := pairDevice(t, s, store, "phone")

	for name, tok := range map[string]string{"operator": testAPIToken, "agent": "agent-token", "device": dev.Token} {
		if rec := doRequest(s, http.MethodGet, "/api/agent/moves/mv-1", tok, ""); rec.Code != http.StatusOK {
			t.Errorf("%s token GET = %d: %s", name, rec.Code, rec.Body)
		}
		if rec := doRequest(s, http.MethodPost, "/api/agent/nobody/move", tok, `{"mode":"move"}`); rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden {
			t.Errorf("%s token POST = %d, want it past auth", name, rec.Code)
		}
	}
	if rec := doRequest(s, http.MethodGet, "/api/agent/moves/mv-1", "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token = %d, want 401", rec.Code)
	}
	if rec := doRequest(s, http.MethodGet, "/api/agent/moves/mv-1", "wrong", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("bad token = %d, want 401", rec.Code)
	}
}

func getPath(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

// TestMoveStatusRouteLeavesSiblingRoutesAlone pins how the routes beside
// GET /api/agent/moves/{id} answer: a wrong method on a POST-only sibling is
// a 405 that advertises POST, and the client-tools route a literal "moves"
// agent shares a shape with keeps its handler.
func TestMoveStatusRouteLeavesSiblingRoutesAlone(t *testing.T) {
	s, _, _ := newDeviceTestServer(t)
	s.moveJobs = &fakeMoveJobs{jobs: map[string]agentmove.JobStatus{"mv-1": {ID: "mv-1", State: agentmove.JobDone}}}
	cases := []struct {
		method, path string
		status       int
		allow        string
	}{
		{http.MethodGet, "/api/agent/foo/move", http.StatusMethodNotAllowed, "POST"},
		{http.MethodPut, "/api/agent/foo/move", http.StatusMethodNotAllowed, "POST"},
		{http.MethodGet, "/api/agent/foo/rename", http.StatusMethodNotAllowed, "POST"},
		{http.MethodGet, "/api/agent/moves/rename", http.StatusMethodNotAllowed, "POST"},
		{http.MethodPut, "/api/agent/moves/mv-1", http.StatusMethodNotAllowed, "GET, HEAD"},
		{http.MethodGet, "/api/agent/moves/mv-1", http.StatusOK, ""},
		{http.MethodHead, "/api/agent/moves/mv-1", http.StatusOK, ""},
	}
	for _, c := range cases {
		rec := doRequest(s, c.method, c.path, "agent-token", "")
		if rec.Code != c.status || rec.Header().Get("Allow") != c.allow {
			t.Errorf("%s %s = %d Allow %q, want %d Allow %q", c.method, c.path, rec.Code, rec.Header().Get("Allow"), c.status, c.allow)
		}
	}

	// Same handler as any other agent's client-tools list: no relay wired
	// here, so whatever it answers for "foo" it answers for "moves".
	want := doRequest(s, http.MethodGet, "/api/agent/foo/client-tools", "agent-token", "")
	got := doRequest(s, http.MethodGet, "/api/agent/moves/client-tools", "agent-token", "")
	if got.Code != want.Code || got.Body.String() != want.Body.String() {
		t.Errorf("GET /api/agent/moves/client-tools = %d %q, want %d %q", got.Code, got.Body, want.Code, want.Body)
	}
}
