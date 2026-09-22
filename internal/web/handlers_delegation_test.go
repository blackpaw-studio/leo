package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
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

func TestDelegationRoleMutations(t *testing.T) {
	cfg := delegationTestConfig()
	s, path := newTestServerWithConfigFile(t, cfg)
	postDelegation(t, s.handleDelegationRoleAdd, "name=review&template=one")
	loaded := loadConfigFile(t, path)
	if got := loaded.Delegation.Profiles["p"].Roles["review"].Template; got != "one" {
		t.Fatalf("added role template = %q", got)
	}
	postDelegation(t, s.handleDelegationRoleRename, "name=review&new_name=security")
	loaded = loadConfigFile(t, path)
	if _, ok := loaded.Delegation.Profiles["p"].Roles["security"]; !ok {
		t.Fatal("renamed role did not cascade to profile")
	}
	postDelegation(t, s.handleDelegationRoleDelete, "name=security")
	loaded = loadConfigFile(t, path)
	if _, ok := loaded.Delegation.Roles["security"]; ok {
		t.Fatal("role was not removed")
	}
	if _, ok := loaded.Delegation.Profiles["p"].Roles["security"]; ok {
		t.Fatal("removed role remains mapped")
	}
}

func TestDelegationProfileMutations(t *testing.T) {
	s, path := newTestServerWithConfigFile(t, delegationTestConfig())
	postDelegation(t, s.handleDelegationProfileAdd, "name=staging")
	postDelegation(t, s.handleDelegationProfileDuplicate, "name=p&new_name=copy")
	postDelegation(t, s.handleDelegationProfileRename, "name=p&new_name=production")
	loaded := loadConfigFile(t, path)
	if loaded.Delegation.ActiveProfile != "production" {
		t.Fatalf("active profile = %q", loaded.Delegation.ActiveProfile)
	}
	if got := loaded.Delegation.Profiles["copy"].Roles["implement"].Template; got != "one" {
		t.Fatalf("duplicate template = %q", got)
	}
	postDelegation(t, s.handleDelegationProfileDelete, "name=staging")
	loaded = loadConfigFile(t, path)
	if _, ok := loaded.Delegation.Profiles["staging"]; ok {
		t.Fatal("profile was not removed")
	}
}

func TestDelegationInvalidMutationKeepsFile(t *testing.T) {
	s, path := newTestServerWithConfigFile(t, delegationTestConfig())
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	w := postDelegation(t, s.handleDelegationProfileAdd, "name=not valid")
	if !strings.Contains(w.Body.String(), entityNameError) {
		t.Fatalf("response = %q", w.Body.String())
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("invalid mutation changed leo.yaml")
	}
}

func TestDelegationPreviewShowsDiffAndBlock(t *testing.T) {
	cfg := delegationTestConfig()
	cfg.Delegation.Roles["review"] = config.RoleSpec{}
	cfg.Delegation.Profiles["next"] = config.Profile{Roles: map[string]config.RoleTarget{"implement": {Template: "two", Model: "m", Effort: "high"}}}
	s, _ := newTestServerWithConfigFile(t, cfg)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/web/delegation/preview?profile=next", nil)
	s.handleDelegationPreview(w, req)
	got := w.Body.String()
	if !strings.Contains(got, "implement") || !strings.Contains(got, "Blocked: unmapped declared roles:") || !strings.Contains(got, "review") {
		t.Fatalf("preview = %q", got)
	}
}

func TestDelegationCannotRemoveActiveProfile(t *testing.T) {
	s, _ := newTestServerWithConfigFile(t, delegationTestConfig())
	w := postDelegation(t, s.handleDelegationProfileDelete, "name=p")
	if !strings.Contains(w.Body.String(), "cannot remove active profile") || !strings.Contains(w.Body.String(), "p") {
		t.Fatalf("response = %q", w.Body.String())
	}
}

func delegationTestConfig() *config.Config {
	return &config.Config{Web: config.WebConfig{Enabled: true}, Templates: map[string]config.TemplateConfig{"one": {}, "two": {}}, Delegation: &config.DelegationConfig{Roles: map[string]config.RoleSpec{"implement": {UseFor: "write"}}, ActiveProfile: "p", Profiles: map[string]config.Profile{"p": {Roles: map[string]config.RoleTarget{"implement": {Template: "one"}}}}}}
}

func postDelegation(t *testing.T, handler http.HandlerFunc, encoded string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(encoded))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handler(w, req)
	return w
}
