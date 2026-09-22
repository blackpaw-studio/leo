package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/consult"
)

func delegationServer(t *testing.T) (*Server, string) {
	t.Helper()
	cfg := &config.Config{Templates: map[string]config.TemplateConfig{"one": {}, "two": {}}, Delegation: &config.DelegationConfig{
		ActiveProfile: "a",
		Profiles: map[string]config.Profile{
			"a": {Roles: map[string]config.RoleTarget{"implement": {Template: "one", Model: "profile-model", Effort: "high"}}},
			"b": {Roles: map[string]config.RoleTarget{"implement": {Template: "two"}}},
		},
	}}
	s, path := newTestServerWithConfigFile(t, cfg)
	s.consults.ExecCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "echo", `{"type":"result","result":"ok"}`)
	}
	return s, path
}

func dispatchRole(t *testing.T, s *Server, body string) (int, string) {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleAPIDispatch(w, httptest.NewRequest(http.MethodPost, "/api/dispatch", strings.NewReader(body)))
	return w.Code, w.Body.String()
}

func TestAPIDispatchRoleValidationAndResolution(t *testing.T) {
	s, _ := delegationServer(t)
	for _, body := range []string{`{"prompt":"x","cwd":"/tmp"}`, `{"template":"one","role":"implement","prompt":"x","cwd":"/tmp"}`} {
		if code, got := dispatchRole(t, s, body); code != http.StatusBadRequest || !strings.Contains(got, "exactly one of template or role is required") {
			t.Fatalf("%d %s", code, got)
		}
	}
	if code, got := dispatchRole(t, s, `{"role":"review.security","prompt":"x","cwd":"/tmp"}`); code != http.StatusBadRequest || !strings.Contains(got, "review.security") || !strings.Contains(got, "mapped: implement") {
		t.Fatalf("%d %s", code, got)
	}
	if code, got := dispatchRole(t, s, `{"role":"implement","prompt":"x","cwd":"/tmp","model":"call-model","effort":"max"}`); code != http.StatusOK {
		t.Fatalf("%d %s", code, got)
	}
	recs := s.consults.Records()
	if len(recs) != 1 || recs[0].Template != "one" || recs[0].Role != "implement" || recs[0].Profile != "a" || recs[0].Model != "call-model" || recs[0].Effort != "max" {
		t.Fatalf("records = %#v", recs)
	}
}

func TestAPIDispatchRoleUsesFreshConfigAndExpectedTemplate(t *testing.T) {
	s, path := delegationServer(t)
	if code, _ := dispatchRole(t, s, `{"role":"implement","prompt":"x","cwd":"/tmp"}`); code != http.StatusOK {
		t.Fatal(code)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Delegation.ActiveProfile = "b"
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	if code, got := dispatchRole(t, s, `{"role":"implement","expect_template":"one","prompt":"x","cwd":"/tmp"}`); code != http.StatusConflict || !strings.Contains(got, "delegation profile changed during dispatch; retry") {
		t.Fatalf("%d %s", code, got)
	}
	if code, got := dispatchRole(t, s, `{"role":"implement","prompt":"x","cwd":"/tmp"}`); code != http.StatusOK {
		t.Fatalf("%d %s", code, got)
	}
	recs := s.consults.Records()
	if len(recs) != 2 || !containsDispatchTemplate(recs, "one") || !containsDispatchTemplate(recs, "two") {
		t.Fatalf("records = %#v", recs)
	}
}

func containsDispatchTemplate(records []consult.Record, template string) bool {
	for _, record := range records {
		if record.Template == template {
			return true
		}
	}
	return false
}

func TestAPIDelegationEndpoints(t *testing.T) {
	s, _ := delegationServer(t)
	w := httptest.NewRecorder()
	s.handleAPIDelegation(w, httptest.NewRequest(http.MethodGet, "/api/delegation", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "implement") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	s.handleAPIDelegationResolve(w, httptest.NewRequest(http.MethodGet, "/api/delegation/resolve?role=implement", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"template":"one"`) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}
