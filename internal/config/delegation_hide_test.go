package config

import (
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestHiddenNativeAgentsDefaultsWhenAbsent(t *testing.T) {
	var cfg Config
	if err := yaml.Unmarshal([]byte("delegation:\n  active_profile: p\n  profiles: {p: {roles: {r: t}}}\n"), &cfg); err != nil {
		t.Fatal(err)
	}
	want := []string{"implementer", "implementer-hard", "code-reviewer", "Explore", "Plan", "general-purpose"}
	if got := cfg.Delegation.HiddenNativeAgents(); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	var none *DelegationConfig
	if got := none.HiddenNativeAgents(); got != nil {
		t.Fatalf("nil delegation hides %v", got)
	}
}

func TestHiddenNativeAgentsExplicitEmptyHidesNothingAndRoundTrips(t *testing.T) {
	cfg := delegationFixture()
	cfg.Delegation.SetHiddenNativeAgents([]string{})
	out, err := yaml.Marshal(cfg.Delegation)
	if err != nil {
		t.Fatal(err)
	}
	var back DelegationConfig
	if err := yaml.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	if got := back.HiddenNativeAgents(); len(got) != 0 {
		t.Fatalf("explicit empty list came back as %v (yaml %s)", got, out)
	}
}

func TestHiddenNativeAgentsAbsentIsNotWritten(t *testing.T) {
	cfg := delegationFixture()
	out, err := yaml.Marshal(cfg.Delegation)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "hide_native_agents") {
		t.Fatalf("default list must not be persisted: %s", out)
	}
}

func TestHiddenNativeAgentsValidatesNames(t *testing.T) {
	cfg := delegationFixture()
	cfg.Delegation.SetHiddenNativeAgents([]string{"Explore", "bad name", ""})
	errs := strings.Join(cfg.validateDelegation(), "\n")
	if !strings.Contains(errs, `delegation.hide_native_agents "bad name" is not a valid agent type`) || !strings.Contains(errs, `delegation.hide_native_agents "" is not a valid agent type`) {
		t.Fatalf("errs = %s", errs)
	}
	if strings.Contains(errs, `"Explore"`) {
		t.Fatalf("Explore is valid: %s", errs)
	}
}
