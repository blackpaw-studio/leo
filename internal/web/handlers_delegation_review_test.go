package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

func TestDelegationPreviewEscapesTargetValues(t *testing.T) {
	cfg := delegationTestConfig()
	cfg.Delegation.Profiles["next"] = config.Profile{Roles: map[string]config.RoleTarget{
		"implement": {Template: "two", Model: `<img src=x onerror=alert(1)>`},
	}}
	s, _ := newTestServerWithConfigFile(t, cfg)
	w := httptest.NewRecorder()
	s.handleDelegationPreview(w, httptest.NewRequest(http.MethodGet, "/web/delegation/preview?profile=next", nil))
	if strings.Contains(w.Body.String(), `<img src=x onerror=alert(1)>`) || !strings.Contains(w.Body.String(), "&lt;img") {
		t.Fatalf("preview is not escaped: %s", w.Body.String())
	}
}

func TestDelegationRenameRefreshesPageBody(t *testing.T) {
	s, _ := newTestServerWithConfigFile(t, delegationTestConfig())
	w := postDelegation(t, s.handleDelegationProfileRename, "name=p&new_name=production")
	if w.Header().Get("HX-Refresh") != "true" {
		t.Fatalf("HX-Refresh = %q", w.Header().Get("HX-Refresh"))
	}
	w = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/config/delegation", nil)
	authorizeTestRequest(req)
	s.httpServer.Handler.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), "production") {
		t.Fatalf("refreshed page omitted renamed profile: %s", w.Body.String())
	}
}

func TestDelegationGridIncludesUndeclaredMappedRoles(t *testing.T) {
	cfg := delegationTestConfig()
	cfg.Delegation.Profiles["p"] = config.Profile{Roles: map[string]config.RoleTarget{
		"implement": {Template: "one"},
		"legacy":    {Template: "one"},
	}}
	s, path := newTestServerWithConfigFile(t, cfg)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/config/delegation", nil)
	authorizeTestRequest(req)
	s.httpServer.Handler.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), "legacy") || !strings.Contains(w.Body.String(), "undeclared") {
		t.Fatalf("fallback role missing: %s", w.Body.String())
	}
	postDelegation(t, s.handleDelegationRoleDelete, "name=legacy")
	if _, ok := loadConfigFile(t, path).Delegation.Profiles["p"].Roles["legacy"]; ok {
		t.Fatal("undeclared role could not be removed")
	}
}

func TestDelegationCellInitializesNilRoles(t *testing.T) {
	cfg := delegationTestConfig()
	cfg.Delegation.Profiles["p"] = config.Profile{}
	s, path := newTestServerWithConfigFile(t, cfg)
	postDelegation(t, s.handleDelegationCell, "profile=p&role=implement&template=two")
	if got := loadConfigFile(t, path).Delegation.Profiles["p"].Roles["implement"].Template; got != "two" {
		t.Fatalf("template = %q", got)
	}
}

func TestDelegationActivationUsesPreviewDialog(t *testing.T) {
	s, _ := newTestServerWithConfigFile(t, delegationTestConfig())
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/config/delegation", nil)
	authorizeTestRequest(req)
	s.httpServer.Handler.ServeHTTP(w, req)
	page := w.Body.String()
	if strings.Contains(page, "hx-confirm") || !strings.Contains(page, "delegation-preview") || !strings.Contains(page, "make active") {
		t.Fatalf("activation does not require preview dialog: %s", page)
	}
}
