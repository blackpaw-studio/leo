package config

import (
	"strings"
	"testing"
)

func TestValidNameRejectsDotNames(t *testing.T) {
	for _, name := range []string{"", ".", "..", "a/b", "a b"} {
		if ValidName(name) {
			t.Fatalf("ValidName(%q) = true", name)
		}
	}
	for _, name := range []string{"a", "review.security", "a_b-c", "..x"} {
		if !ValidName(name) {
			t.Fatalf("ValidName(%q) = false", name)
		}
	}
}

func TestDelegationValidateRejectsDotNames(t *testing.T) {
	cfg := delegationFixture()
	cfg.Delegation.Profiles[".."] = Profile{Roles: map[string]RoleTarget{".": {Template: "worker-template"}}}
	cfg.Delegation.Roles["."] = RoleSpec{}
	errs := strings.Join(cfg.validateDelegation(), "\n")
	for _, want := range []string{`delegation.profiles.".." is not a valid name`, `delegation.roles."." is not a valid name`} {
		if !strings.Contains(errs, want) {
			t.Fatalf("errors missing %q:\n%s", want, errs)
		}
	}
}

func TestResolveRoleReportsEffectiveModel(t *testing.T) {
	cfg := delegationFixture()
	cfg.Templates["bare-template"] = TemplateConfig{}
	cfg.Delegation.Profiles["primary"].Roles["review"] = RoleTarget{Template: "bare-template"}
	tests := []struct {
		role, model, effective, source string
	}{
		{"implement", "role-model", "role-model", ModelSourceProfile},
		{"plan", "", "planner-model", ModelSourceTemplate},
		{"review", "", DefaultModel, ModelSourceDefault},
	}
	for _, tt := range tests {
		got, err := cfg.ResolveRole(tt.role)
		if err != nil {
			t.Fatal(err)
		}
		if got.Model != tt.model || got.EffectiveModel != tt.effective || got.ModelSource != tt.source {
			t.Fatalf("%s: %#v", tt.role, got)
		}
	}
	got, _ := cfg.ResolveRole("plan")
	got = got.WithOverrides("request-model", "")
	if got.Model != "request-model" || got.EffectiveModel != "request-model" || got.ModelSource != ModelSourceRequest {
		t.Fatalf("override: %#v", got)
	}
}

func TestDelegationStatusShowsInheritedModel(t *testing.T) {
	status := RenderDelegationStatus(delegationFixture())
	if !strings.Contains(status, "primary\tplan\tplanner-template\tplanner-model\ttemplate\t") {
		t.Fatalf("status missing inherited model:\n%s", status)
	}
	if !strings.Contains(status, "primary\timplement\tworker-template\trole-model\tprofile\thigh") {
		t.Fatalf("status missing override:\n%s", status)
	}
}

func TestRenameRoleRejectsNameMappedElsewhere(t *testing.T) {
	cfg := delegationFixture()
	// "legacy" is undeclared and mapped only in secondary; "plan" is mapped in both.
	cfg.Delegation.Profiles["secondary"].Roles["legacy"] = RoleTarget{Template: "worker-template"}
	delete(cfg.Delegation.Profiles["primary"].Roles, "implement")
	delete(cfg.Delegation.Roles, "implement")
	cfg.Delegation.Profiles["primary"].Roles["solo"] = RoleTarget{Template: "planner-template"}
	err := RenameRole(cfg, "solo", "legacy")
	if err == nil || !strings.Contains(err.Error(), `role "legacy" already exists`) {
		t.Fatalf("err = %v", err)
	}
	if _, ok := cfg.Delegation.Profiles["primary"].Roles["solo"]; !ok {
		t.Fatal("failed rename mutated config")
	}
}

func TestDelegationHasRoleCoversDeclaredAndMapped(t *testing.T) {
	d := delegationFixture().Delegation
	d.Profiles["secondary"].Roles["legacy"] = RoleTarget{Template: "worker-template"}
	d.Roles["declared-only"] = RoleSpec{}
	for name, want := range map[string]bool{"plan": true, "legacy": true, "declared-only": true, "nope": false} {
		if got := d.HasRole(name); got != want {
			t.Fatalf("HasRole(%q) = %v", name, got)
		}
	}
}
