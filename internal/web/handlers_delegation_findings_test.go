package web

import (
	"bytes"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

func undeclaredDelegationConfig() *config.Config {
	cfg := delegationTestConfig()
	cfg.Delegation.Roles = nil
	cfg.Delegation.Profiles["p"] = config.Profile{Roles: map[string]config.RoleTarget{
		"implement": {Template: "one"},
		"plan":      {Template: "two", Model: "m"},
	}}
	return cfg
}

func TestDelegationFirstRoleDeclarationMigratesMappedRoles(t *testing.T) {
	s, path := newTestServerWithConfigFile(t, undeclaredDelegationConfig())
	w := postDelegation(t, s.handleDelegationRoleAdd, "name=review&template=one")
	if strings.Contains(w.Body.String(), "error") {
		t.Fatalf("response = %q", w.Body.String())
	}
	loaded := loadConfigFile(t, path)
	for _, role := range []string{"implement", "plan", "review"} {
		if _, ok := loaded.Delegation.Roles[role]; !ok {
			t.Fatalf("role %q not declared: %#v", role, loaded.Delegation.Roles)
		}
	}
	block := config.RenderDelegationInstructions(loaded)
	for _, role := range []string{"implement", "plan", "review"} {
		if !strings.Contains(block, "- "+role) {
			t.Fatalf("block missing %q:\n%s", role, block)
		}
	}
}

func TestDelegationRoleAddNeverOverwritesMappedUndeclaredRole(t *testing.T) {
	cfg := undeclaredDelegationConfig()
	cfg.Delegation.Roles = map[string]config.RoleSpec{"implement": {}}
	s, path := newTestServerWithConfigFile(t, cfg)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	w := postDelegation(t, s.handleDelegationRoleAdd, "name=plan&template=one")
	if !strings.Contains(w.Body.String(), `role &#34;plan&#34; already exists`) && !strings.Contains(w.Body.String(), `role "plan" already exists`) {
		t.Fatalf("response = %q", w.Body.String())
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("duplicate role add changed leo.yaml")
	}
}

func TestDelegationRejectsDotNames(t *testing.T) {
	s, path := newTestServerWithConfigFile(t, delegationTestConfig())
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	routes := map[string]struct {
		handler http.HandlerFunc
		form    string
	}{
		"role add":          {s.handleDelegationRoleAdd, "template=one&name="},
		"role rename":       {s.handleDelegationRoleRename, "name=implement&new_name="},
		"profile add":       {s.handleDelegationProfileAdd, "name="},
		"profile rename":    {s.handleDelegationProfileRename, "name=p&new_name="},
		"profile duplicate": {s.handleDelegationProfileDuplicate, "name=p&new_name="},
	}
	for route, tc := range routes {
		for _, name := range []string{".", ".."} {
			w := postDelegation(t, tc.handler, tc.form+name)
			if !strings.Contains(w.Body.String(), entityNameError) {
				t.Errorf("%s %q: response = %q", route, name, w.Body.String())
			}
		}
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("dot name changed leo.yaml")
	}
}

func TestDelegationCannotRemoveLastDeclaredRole(t *testing.T) {
	s, path := newTestServerWithConfigFile(t, delegationTestConfig())
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	w := postDelegation(t, s.handleDelegationRoleDelete, "name=implement")
	if !strings.Contains(w.Body.String(), "cannot remove the last declared role") {
		t.Fatalf("response = %q", w.Body.String())
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("blocked removal changed leo.yaml")
	}
}
