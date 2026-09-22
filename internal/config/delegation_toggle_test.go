package config

import (
	"errors"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDelegationEnabledDefaultsOn(t *testing.T) {
	var cfg Config
	if err := yaml.Unmarshal([]byte("delegation:\n  active_profile: p\n  profiles: {p: {roles: {r: t}}}\n"), &cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.Delegation.IsEnabled() {
		t.Fatal("absent enabled must default to on")
	}
	off := false
	cfg.Delegation.Enabled = &off
	if cfg.Delegation.IsEnabled() {
		t.Fatal("enabled: false must be off")
	}
	var none *DelegationConfig
	if none.IsEnabled() {
		t.Fatal("nil delegation must be off")
	}
}

func TestDisabledDelegationRejectsRolesButStillValidates(t *testing.T) {
	cfg := delegationFixture()
	cfg.Delegation.SetEnabled(false)
	_, err := cfg.ResolveRole("plan")
	if !errors.Is(err, ErrDelegationDisabled) || err.Error() != `delegation is disabled (enable it with "leo delegation enable" or the web Delegation page)` {
		t.Fatalf("err = %v", err)
	}
	if status := RenderDelegationStatus(cfg); !strings.Contains(status, "delegation is disabled") {
		t.Fatalf("status = %q", status)
	}
	if RenderDelegationInstructions(cfg) != "" {
		t.Fatal("disabled delegation must render no instructions")
	}
	cfg.Delegation.Profiles["primary"].Roles["plan"] = RoleTarget{Template: "missing"}
	if errs := strings.Join(cfg.validateDelegation(), "\n"); !strings.Contains(errs, `template "missing" does not exist`) {
		t.Fatalf("disabled delegation must still validate profiles: %s", errs)
	}
}
