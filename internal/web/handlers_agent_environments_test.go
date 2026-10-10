package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/config"
)

var errTestBoom = errors.New("boom")

func postJSON(t *testing.T, s *Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)
	return w
}

func TestAPIAgentSpawnForwardsEnvironments(t *testing.T) {
	s, _, svc := newTestServerWithAgents(t)
	w := postJSON(t, s, "/api/agent/spawn", `{"template":"coding","environments":["work","proxy"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if want := []string{"work", "proxy"}; !reflect.DeepEqual(svc.spawnSpec.Environments, want) {
		t.Fatalf("Environments = %v, want %v", svc.spawnSpec.Environments, want)
	}
}

func TestAPIAgentSpawnUnknownEnvironmentIs400(t *testing.T) {
	s, _, svc := newTestServerWithAgents(t)
	svc.spawnErr = &config.UnknownEnvironmentError{Name: "nope"}
	w := postJSON(t, s, "/api/agent/spawn", `{"template":"coding","environments":["nope"]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
}

func TestAPIAgentSetEnvironments(t *testing.T) {
	s, _, svc := newTestServerWithAgents(t)
	svc.records = []agent.Record{{Name: "leo-coding-leo", Status: "running"}}

	w := postJSON(t, s, "/api/agent/leo-coding-leo/environments", `{"environments":["work"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if !svc.setEnvCalled || svc.setEnvName != "leo-coding-leo" || !reflect.DeepEqual(svc.setEnvNames, []string{"work"}) {
		t.Fatalf("SetEnvironments(%q, %v) called=%v", svc.setEnvName, svc.setEnvNames, svc.setEnvCalled)
	}
}

func TestAPIAgentSetEnvironmentsErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"unknown environment", &config.UnknownEnvironmentError{Name: "nope"}, http.StatusBadRequest},
		{"other failure", errTestBoom, http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _, svc := newTestServerWithAgents(t)
			svc.records = []agent.Record{{Name: "leo-coding-leo", Status: "running"}}
			svc.setEnvErr = tt.err
			w := postJSON(t, s, "/api/agent/leo-coding-leo/environments", `{"environments":["x"]}`)
			if w.Code != tt.want {
				t.Fatalf("status = %d, want %d: %s", w.Code, tt.want, w.Body.String())
			}
		})
	}
}

func TestAPIAgentSetEnvironmentsUnknownAgentIs404(t *testing.T) {
	s, _, svc := newTestServerWithAgents(t)
	w := postJSON(t, s, "/api/agent/ghost/environments", `{"environments":["work"]}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if svc.setEnvCalled {
		t.Fatal("SetEnvironments must not run for an unresolvable agent")
	}
}

func TestWebAgentSpawnParsesEnvironmentsCSV(t *testing.T) {
	s, _, svc := newTestServerWithAgents(t)
	postForm(t, s, "/web/agent/spawn", url.Values{"template": {"coding"}, "environments": {" work, proxy ,,"}})
	if want := []string{"work", "proxy"}; !reflect.DeepEqual(svc.spawnSpec.Environments, want) {
		t.Fatalf("Environments = %v, want %v", svc.spawnSpec.Environments, want)
	}
}

func TestWebAgentSetEnvironmentsRerendersList(t *testing.T) {
	s, _, svc := newTestServerWithAgents(t)
	svc.records = []agent.Record{{Name: "leo-coding-leo", Status: "running", StartedAt: time.Now(), Environments: []string{"work"}}}

	w := postForm(t, s, "/web/agent/leo-coding-leo/environments", url.Values{"environments": {"work"}})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if !reflect.DeepEqual(svc.setEnvNames, []string{"work"}) {
		t.Fatalf("SetEnvironments names = %v", svc.setEnvNames)
	}
	if body := w.Body.String(); !strings.Contains(body, `id="agents-content"`) || !strings.Contains(body, "work") {
		t.Fatalf("expected re-rendered agents list showing the environment, got %q", body)
	}
}

func TestWebAgentSetEnvironmentsEmptyClearsOverride(t *testing.T) {
	s, _, svc := newTestServerWithAgents(t)
	svc.records = []agent.Record{{Name: "leo-coding-leo", Status: "running", StartedAt: time.Now()}}
	postForm(t, s, "/web/agent/leo-coding-leo/environments", url.Values{"environments": {""}})
	if !svc.setEnvCalled || len(svc.setEnvNames) != 0 {
		t.Fatalf("called=%v names=%v, want call with no names", svc.setEnvCalled, svc.setEnvNames)
	}
}

func TestWebAgentSetEnvironmentsErrorFlashesToContainer(t *testing.T) {
	s, _, svc := newTestServerWithAgents(t)
	svc.records = []agent.Record{{Name: "leo-coding-leo", Status: "running"}}
	svc.setEnvErr = &config.UnknownEnvironmentError{Name: "nope"}
	w := postForm(t, s, "/web/agent/leo-coding-leo/environments", url.Values{"environments": {"nope"}})
	if got := w.Header().Get("HX-Retarget"); got == "" {
		t.Fatal("error path must retarget the flash container")
	}
	if !strings.Contains(w.Body.String(), "nope") {
		t.Fatalf("error not shown: %q", w.Body.String())
	}
}
