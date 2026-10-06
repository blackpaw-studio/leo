package web

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func renderDelegationPage(t *testing.T, s *Server) string {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/config/delegation", nil)
	authorizeTestRequest(req)
	s.httpServer.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	return w.Body.String()
}

func TestDelegationPageShowsTheHiddenNativeAgents(t *testing.T) {
	s, _ := newTestServerWithConfigFile(t, delegationTestConfig())
	page := renderDelegationPage(t, s)
	if !strings.Contains(page, `name="hide_native_agents"`) || !strings.Contains(page, "implementer, implementer-hard, code-reviewer, Explore, Plan, general-purpose") {
		t.Fatalf("page lacks the hide list field with the default list:\n%s", page)
	}
}

func TestDelegationOffBannerSaysBridgedAgentsUpdateLive(t *testing.T) {
	cfg := delegationTestConfig()
	cfg.Delegation.SetEnabled(false)
	s, _ := newTestServerWithConfigFile(t, cfg)
	if page := renderDelegationPage(t, s); !strings.Contains(page, "Bridged claude agents update live") {
		t.Fatalf("off banner lacks the live-update note:\n%s", page)
	}
}

func TestDelegationHideAgentsAutosaves(t *testing.T) {
	s, path := newTestServerWithConfigFile(t, delegationTestConfig())
	w := postDelegation(t, s.handleDelegationHideAgents, "hide_native_agents=Explore%2C+Plan++implementer")
	if !strings.Contains(w.Body.String(), "cell-status ok") {
		t.Fatalf("response = %q", w.Body.String())
	}
	if got, want := loadConfigFile(t, path).Delegation.HiddenNativeAgents(), []string{"Explore", "Plan", "implementer"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("hidden = %v, want %v", got, want)
	}

	w = postDelegation(t, s.handleDelegationHideAgents, "hide_native_agents=")
	if got := loadConfigFile(t, path).Delegation.HiddenNativeAgents(); len(got) != 0 {
		t.Fatalf("an emptied field must hide none, got %v (%s)", got, w.Body.String())
	}

	w = postDelegation(t, s.handleDelegationHideAgents, "hide_native_agents=bad%2Fname")
	if !strings.Contains(w.Body.String(), "cell-status err") {
		t.Fatalf("an invalid name must be refused: %q", w.Body.String())
	}
}
