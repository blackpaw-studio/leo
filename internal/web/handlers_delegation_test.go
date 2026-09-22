package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

func TestDelegationPageAndCellSave(t *testing.T) {
	cfg := &config.Config{Web: config.WebConfig{Enabled: true}, Templates: map[string]config.TemplateConfig{"one": {}, "two": {}}, Delegation: &config.DelegationConfig{Roles: map[string]config.RoleSpec{"implement": {UseFor: "write"}}, ActiveProfile: "p", Profiles: map[string]config.Profile{"p": {Roles: map[string]config.RoleTarget{"implement": {Template: "one"}}}}}}
	s, path := newTestServerWithConfigFile(t, cfg)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/config/delegation", nil)
	authorizeTestRequest(req)
	s.httpServer.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Role routing") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/web/delegation/cell", strings.NewReader("profile=p&role=implement&template=two&model=m"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.handleDelegationCell(w, req)
	loaded := loadConfigFile(t, path)
	if got := loaded.Delegation.Profiles["p"].Roles["implement"].Template; got != "two" {
		t.Fatalf("template=%q", got)
	}
}
