package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/observe"
)

// progressServer is a token-split server with an attention store holding one
// launch token per agent and a progress store.
func progressServer(t *testing.T) *Server {
	t.Helper()
	s := newTokenSplitServer(t)
	s.attention = observe.NewAttentionStore(nil)
	s.attention.RegisterToken("tok-a", "alpha")
	s.attention.RegisterToken("tok-b", "beta")
	s.progress = observe.NewProgressStore(nil, nil)
	return s
}

func postProgress(t *testing.T, s *Server, bearer, body string) *httptest.ResponseRecorder {
	t.Helper()
	return requestAs(t, s, http.MethodPost, "/api/agent/progress", bearer, body)
}

type progressResp struct {
	OK    bool               `json:"ok"`
	Error string             `json:"error"`
	Task  *observe.AgentTask `json:"task"`
}

func decodeProgress(t *testing.T, w *httptest.ResponseRecorder) progressResp {
	t.Helper()
	var r progressResp
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
		t.Fatalf("decoding %q: %v", w.Body.String(), err)
	}
	return r
}

func TestProgressRouteStoresTaskForTheTokensAgent(t *testing.T) {
	s := progressServer(t)

	w := postProgress(t, s, testAgentToken, `{"token":"tok-a","id":"build","title":"Xcode build","percent":62,"status":"Compiling","eta_seconds":90}`)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	resp := decodeProgress(t, w)
	if !resp.OK || resp.Task == nil || resp.Task.ID != "build" || *resp.Task.Percent != 62 || resp.Task.EtaAt == nil {
		t.Fatalf("response = %s", w.Body.String())
	}
	if got := s.progress.Tasks("alpha"); len(got) != 1 || got[0].Title != "Xcode build" {
		t.Fatalf("alpha tasks = %+v", got)
	}
	if got := s.progress.Tasks("beta"); len(got) != 0 {
		t.Fatalf("beta tasks = %+v", got)
	}
}

// The body has no way to name an agent: an "agent" field is ignored, and a
// token only ever writes its own agent's tasks.
func TestProgressRouteNeverWritesAnotherAgent(t *testing.T) {
	s := progressServer(t)

	w := postProgress(t, s, testAgentToken, `{"token":"tok-a","agent":"beta","name":"beta","id":"build","title":"T"}`)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if len(s.progress.Tasks("beta")) != 0 || len(s.progress.Tasks("alpha")) != 1 {
		t.Fatalf("alpha=%+v beta=%+v", s.progress.Tasks("alpha"), s.progress.Tasks("beta"))
	}
	// Two agents using the same id keep separate tasks.
	postProgress(t, s, testAgentToken, `{"token":"tok-b","id":"build","title":"B"}`)
	if a, b := s.progress.Tasks("alpha")[0].Title, s.progress.Tasks("beta")[0].Title; a != "T" || b != "B" {
		t.Fatalf("titles = %q, %q", a, b)
	}
}

func TestProgressRouteRejectsUnknownAndRevokedTokens(t *testing.T) {
	s := progressServer(t)
	for name, body := range map[string]string{
		"unknown": `{"token":"tok-nope","id":"build","title":"T"}`,
		"empty":   `{"token":"","id":"build","title":"T"}`,
		"missing": `{"id":"build","title":"T"}`,
	} {
		if w := postProgress(t, s, testAgentToken, body); w.Code != http.StatusForbidden {
			t.Errorf("%s token: status = %d, want 403", name, w.Code)
		}
	}
	s.attention.UnregisterAgent("alpha") // the agent stopped
	if w := postProgress(t, s, testAgentToken, `{"token":"tok-a","id":"build","title":"T"}`); w.Code != http.StatusForbidden {
		t.Errorf("stopped agent's token: status = %d, want 403", w.Code)
	}
	if len(s.progress.Tasks("alpha")) != 0 {
		t.Fatal("a rejected report must store nothing")
	}
}

func TestProgressRouteRequiresBearerAuth(t *testing.T) {
	s := progressServer(t)
	if w := postProgress(t, s, "", `{"token":"tok-a","id":"build","title":"T"}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestProgressRouteFollowsARename(t *testing.T) {
	s := progressServer(t)
	s.attention.Move("alpha", "alpha2")

	postProgress(t, s, testAgentToken, `{"token":"tok-a","id":"build","title":"T"}`)

	if len(s.progress.Tasks("alpha2")) != 1 || len(s.progress.Tasks("alpha")) != 0 {
		t.Fatal("the token follows the agent's new name")
	}
}

func TestProgressRouteErrorsAreReadable(t *testing.T) {
	s := progressServer(t)
	for _, tc := range []struct {
		name, body string
		want       int
		contains   string
	}{
		{"bad id", `{"token":"tok-a","id":"Bad Id","title":"T"}`, http.StatusBadRequest, "id"},
		{"title required", `{"token":"tok-a","id":"x"}`, http.StatusBadRequest, "title"},
		{"percent and indeterminate", `{"token":"tok-a","id":"x","title":"T","percent":5,"indeterminate":true}`, http.StatusBadRequest, "mutually exclusive"},
		{"bad state", `{"token":"tok-a","id":"x","title":"T","state":"paused"}`, http.StatusBadRequest, "state"},
		{"not json", `{nope`, http.StatusBadRequest, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := postProgress(t, s, testAgentToken, tc.body)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d; body = %s", w.Code, tc.want, w.Body.String())
			}
			if r := decodeProgress(t, w); r.OK || !strings.Contains(r.Error, tc.contains) {
				t.Fatalf("error = %q", r.Error)
			}
		})
	}
}

func TestProgressRouteCapReturnsConflict(t *testing.T) {
	s := progressServer(t)
	for i := 0; i < observe.MaxTasksPerClass; i++ {
		body := `{"token":"tok-a","id":"t` + string(rune('a'+i)) + `","title":"T"}`
		if w := postProgress(t, s, testAgentToken, body); w.Code != http.StatusOK {
			t.Fatalf("report %d: %d %s", i, w.Code, w.Body.String())
		}
	}

	w := postProgress(t, s, testAgentToken, `{"token":"tok-a","id":"extra","title":"T"}`)

	if w.Code != http.StatusConflict || !strings.Contains(decodeProgress(t, w).Error, "too many running tasks") {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
}

func TestProgressRouteOversizedBodyIsRejected(t *testing.T) {
	s := progressServer(t)
	body := `{"token":"tok-a","id":"x","title":"T","status":"` + strings.Repeat("a", maxProgressBodyBytes) + `"}`
	if w := postProgress(t, s, testAgentToken, body); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", w.Code)
	}
}

func TestProgressRouteWithoutStoreIsUnavailable(t *testing.T) {
	s := progressServer(t)
	s.progress = nil
	if w := postProgress(t, s, testAgentToken, `{"token":"tok-a","id":"x","title":"T"}`); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}

func dismissTask(t *testing.T, s *Server, bearer, agent, id string) *httptest.ResponseRecorder {
	t.Helper()
	return requestAs(t, s, http.MethodDelete, "/api/v1/agents/"+agent+"/tasks/"+id, bearer, "")
}

func TestDismissTaskRequiresTheOperatorToken(t *testing.T) {
	s := progressServer(t)
	postProgress(t, s, testAgentToken, `{"token":"tok-a","id":"build","title":"T","state":"failed"}`)

	if w := dismissTask(t, s, testAgentToken, "alpha", "build"); w.Code != http.StatusForbidden {
		t.Fatalf("agent token: status = %d, want 403", w.Code)
	}
	if w := dismissTask(t, s, "", "alpha", "build"); w.Code != http.StatusUnauthorized {
		t.Fatalf("no token: status = %d, want 401", w.Code)
	}
	if len(s.progress.Tasks("alpha")) != 1 {
		t.Fatal("a refused dismiss must not remove the task")
	}
	if w := dismissTask(t, s, testAPIToken, "alpha", "build"); w.Code != http.StatusNoContent {
		t.Fatalf("operator: status = %d, body = %s", w.Code, w.Body.String())
	}
	if len(s.progress.Tasks("alpha")) != 0 {
		t.Fatal("dismissed task still present")
	}
}

func TestDismissTaskErrors(t *testing.T) {
	s := progressServer(t)
	postProgress(t, s, testAgentToken, `{"token":"tok-a","id":"running","title":"T"}`)

	if w := dismissTask(t, s, testAPIToken, "alpha", "running"); w.Code != http.StatusConflict {
		t.Errorf("dismiss running: status = %d, want 409", w.Code)
	}
	if w := dismissTask(t, s, testAPIToken, "alpha", "nope"); w.Code != http.StatusNotFound {
		t.Errorf("unknown task: status = %d, want 404", w.Code)
	}
	if w := dismissTask(t, s, testAPIToken, "ghost", "running"); w.Code != http.StatusNotFound {
		t.Errorf("unknown agent: status = %d, want 404", w.Code)
	}
}
