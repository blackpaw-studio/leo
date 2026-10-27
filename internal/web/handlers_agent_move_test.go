package web

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/agentmove"
)

type fakeMoveJobs struct {
	reqs []agentmove.Request
	err  error
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
