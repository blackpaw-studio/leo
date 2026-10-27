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
