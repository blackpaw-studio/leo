package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agentmove"
)

type fakeMoveReceiver struct {
	verbs  []string
	bodies []string
	ctxErr []error
	err    error
}

func (f *fakeMoveReceiver) Handle(ctx context.Context, verb string, body []byte) ([]byte, error) {
	f.verbs = append(f.verbs, verb)
	f.bodies = append(f.bodies, string(body))
	f.ctxErr = append(f.ctxErr, ctx.Err())
	if f.err != nil {
		return nil, f.err
	}
	return []byte(`{"verb":"` + verb + `"}`), nil
}

func moveRequest(s *Server, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	return w
}

func TestMoveRouteVerbs(t *testing.T) {
	s := New(tmpSockPath(t, "m.sock"), "/nonexistent/leo.yaml", nil)
	if w := moveRequest(s, "/agents/move/preflight", "{}"); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("no receiver = %d %s, want 503", w.Code, w.Body)
	}
	recv := &fakeMoveReceiver{}
	s.SetMoveReceiver(recv)
	for _, verb := range agentmove.Verbs {
		w := moveRequest(s, "/agents/move/"+verb, `{"protocol":1}`)
		if w.Code != http.StatusOK || w.Body.String() != `{"verb":"`+verb+`"}` {
			t.Errorf("%s = %d %s", verb, w.Code, w.Body)
		}
	}
	if strings.Join(recv.verbs, ",") != strings.Join(agentmove.Verbs, ",") || recv.bodies[0] != `{"protocol":1}` {
		t.Fatalf("receiver saw %v %v", recv.verbs, recv.bodies)
	}

	recv.err = agentmove.Problems{{Code: agentmove.CodeSubmoduleTorn, Message: "libs/a: bad HEAD"}}
	w := moveRequest(s, "/agents/move/commit", `{}`)
	var env agentmove.VerbError
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil || w.Code != http.StatusConflict || env.Code != agentmove.CodeSubmoduleTorn || !strings.Contains(env.Message, "bad HEAD") {
		t.Fatalf("refusal = %d %s (%v)", w.Code, w.Body, err)
	}
	if w := moveRequest(s, "/agents/move/commit", strings.Repeat("x", maxMoveBody+1)); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body = %d", w.Code)
	}
}

type fakeMoveJobs struct {
	reqs    []agentmove.Request
	err     error
	settled []string
}

func (f *fakeMoveJobs) Abandon(_ context.Context, name string) (agentmove.SettleResult, error) {
	f.settled = append(f.settled, "abandon "+name)
	return agentmove.SettleResult{Name: name, To: "laptop", RunID: "run-1", Outcome: agentmove.SettleAbandoned}, f.err
}

func (f *fakeMoveJobs) Reconcile(_ context.Context, name string) (agentmove.SettleResult, error) {
	f.settled = append(f.settled, "reconcile "+name)
	if f.err != nil {
		return agentmove.SettleResult{}, f.err
	}
	return agentmove.SettleResult{Name: name, To: "laptop", RunID: "run-1", Outcome: agentmove.SettleMoved}, nil
}

func (f *fakeMoveJobs) Start(_ context.Context, req agentmove.Request) (agentmove.JobStatus, error) {
	f.reqs = append(f.reqs, req)
	if f.err != nil {
		return agentmove.JobStatus{}, f.err
	}
	return agentmove.JobStatus{ID: "mv-1", Request: req, State: agentmove.JobRunning}, nil
}

func (f *fakeMoveJobs) Get(id string) (agentmove.JobStatus, bool) {
	return agentmove.JobStatus{ID: id, State: agentmove.JobDone}, id == "mv-1"
}

func TestMoveJobRoutes(t *testing.T) {
	s := New(tmpSockPath(t, "j.sock"), "/nonexistent/leo.yaml", nil)
	if w := moveRequest(s, "/agents/web/move", `{}`); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("no jobs = %d, want 503", w.Code)
	}
	jobs := &fakeMoveJobs{}
	s.SetMoveJobs(jobs)
	w := moveRequest(s, "/agents/web/move", `{"agent":"other","mode":"move","to":"laptop","force":true}`)
	var st agentmove.JobStatus
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil || w.Code != http.StatusAccepted || st.ID != "mv-1" {
		t.Fatalf("start = %d %s (%v)", w.Code, w.Body, err)
	}
	if want := (agentmove.Request{Agent: "web", Mode: "move", To: "laptop", Force: true}); jobs.reqs[0] != want {
		t.Fatalf("request = %+v, want %+v (the path names the agent)", jobs.reqs[0], want)
	}
	jobs.err = agentmove.Problems{{Code: agentmove.CodeAgentMoving, Message: "busy"}}
	w = moveRequest(s, "/agents/web/move", `{"mode":"move"}`)
	var env agentmove.VerbError
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil || w.Code != http.StatusConflict || env.Code != agentmove.CodeAgentMoving {
		t.Fatalf("refused start = %d %s", w.Code, w.Body)
	}
	if w := moveRequest(s, "/agents/web/move", `{`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad body = %d", w.Code)
	}
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w
	}
	if w := get("/moves/mv-1"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"state":"done"`) {
		t.Fatalf("get = %d %s", w.Code, w.Body)
	}
	if w := get("/moves/nope"); w.Code != http.StatusNotFound {
		t.Fatalf("unknown job = %d", w.Code)
	}
}

func TestMoveReconcileRoute(t *testing.T) {
	s := New(tmpSockPath(t, "r.sock"), "/nonexistent/leo.yaml", nil)
	if w := moveRequest(s, "/agents/web/move/reconcile", `{}`); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("no jobs = %d, want 503", w.Code)
	}
	jobs := &fakeMoveJobs{}
	s.SetMoveJobs(jobs)
	w := moveRequest(s, "/agents/web/move/reconcile", `{}`)
	var res agentmove.SettleResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || w.Code != http.StatusOK || res.Outcome != agentmove.SettleMoved || jobs.settled[0] != "reconcile web" {
		t.Fatalf("reconcile = %d %s (%v), settled %v", w.Code, w.Body, err, jobs.settled)
	}
	jobs.err = agentmove.Problems{{Code: agentmove.CodeOutcomeUnknown, Message: "phase hooks"}}
	w = moveRequest(s, "/agents/web/move/reconcile", `{}`)
	var env agentmove.VerbError
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil || w.Code != http.StatusConflict || env.Code != agentmove.CodeOutcomeUnknown {
		t.Fatalf("unsettled reconcile = %d %s", w.Code, w.Body)
	}
}

func TestMoveAbandonRoute(t *testing.T) {
	s := New(tmpSockPath(t, "a.sock"), "/nonexistent/leo.yaml", nil)
	jobs := &fakeMoveJobs{}
	s.SetMoveJobs(jobs)
	w := moveRequest(s, "/agents/web/move/abandon", `{}`)
	var res agentmove.SettleResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || w.Code != http.StatusOK || res.Outcome != agentmove.SettleAbandoned || jobs.settled[0] != "abandon web" {
		t.Fatalf("abandon = %d %s (%v), settled %v", w.Code, w.Body, err, jobs.settled)
	}
}

// fresh_session reaches the job request.
func TestMoveStartForwardsFreshSession(t *testing.T) {
	s := New(tmpSockPath(t, "f.sock"), "/nonexistent/leo.yaml", nil)
	jobs := &fakeMoveJobs{}
	s.SetMoveJobs(jobs)
	w := moveRequest(s, "/agents/web/move", `{"mode":"copy","fresh_session":true}`)
	if w.Code != http.StatusAccepted || len(jobs.reqs) != 1 || !jobs.reqs[0].FreshSession {
		t.Fatalf("start = %d %s, requests %+v; want fresh_session forwarded", w.Code, w.Body, jobs.reqs)
	}
}

// A refused start answers 409 with the code and the plain sentence: the
// message does not repeat the code, and no job id is in the body.
func TestMoveStartSessionMissingIsPlain(t *testing.T) {
	s := New(tmpSockPath(t, "sm.sock"), "/nonexistent/leo.yaml", nil)
	const sentence = "web's saved session transcript no longer exists; retry with a fresh session"
	s.SetMoveJobs(&fakeMoveJobs{err: &agentmove.Problem{Code: agentmove.CodeSessionMissing, Message: sentence}})
	w := moveRequest(s, "/agents/web/move", `{"mode":"move","to":"laptop"}`)
	var env agentmove.VerbError
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusConflict || env.Code != "session_missing" || env.Message != sentence {
		t.Fatalf("refused start = %d %s, want 409 session_missing with the plain sentence", w.Code, w.Body)
	}
}
