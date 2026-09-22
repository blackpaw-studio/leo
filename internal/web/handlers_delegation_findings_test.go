package web

import (
	"bytes"
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
	for _, name := range []string{".", ".."} {
		postDelegation(t, s.handleDelegationRoleAdd, "name="+name+"&template=one")
		postDelegation(t, s.handleDelegationProfileAdd, "name="+name)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("dot name changed leo.yaml")
	}
}
