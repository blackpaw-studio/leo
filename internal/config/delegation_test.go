package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func delegationFixture() *Config {
	return &Config{
		Templates: map[string]TemplateConfig{
			"planner-template": {Model: "planner-model"},
			"worker-template":  {Model: "worker-model"},
		},
		Delegation: &DelegationConfig{
			Roles: map[string]RoleSpec{
				"plan":      {UseFor: "Make a design"},
				"implement": {UseFor: "Write the code"},
			},
			ActiveProfile: "primary",
			Profiles: map[string]Profile{
				"primary": {Roles: map[string]RoleTarget{
					"plan":      {Template: "planner-template"},
					"implement": {Template: "worker-template", Model: "role-model", Effort: "high"},
				}},
				"secondary": {Roles: map[string]RoleTarget{
					"plan":      {Template: "worker-template"},
					"implement": {Template: "planner-template"},
				}},
			},
		},
	}
}

func TestRoleTargetYAMLForms(t *testing.T) {
	var cfg Config
	err := yaml.Unmarshal([]byte(`delegation:
  active_profile: p
  profiles:
    p:
      roles:
        scalar: worker-template
        object: {template: planner-template, model: role-model, effort: high}
`), &cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.Delegation.Profiles["p"].Roles
	if got["scalar"].Template != "worker-template" || got["object"].Effort != "high" {
		t.Fatalf("roles = %#v", got)
	}

	err = yaml.Unmarshal([]byte(`delegation:
  active_profile: p
  profiles: {p: {roles: {bad: {template: x, surprise: y}}}}
`), &cfg)
	if err == nil || !strings.Contains(err.Error(), "surprise") {
		t.Fatalf("error = %v", err)
	}
}

func TestDelegationScalarRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leo.yaml")
	cfg := delegationFixture()
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "plan: planner-template") {
		t.Fatalf("saved YAML did not use scalar: %s", data)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Delegation.Profiles["primary"].Roles["plan"].Template; got != "planner-template" {
		t.Fatalf("template = %q", got)
	}
	object := loaded.Delegation.Profiles["primary"].Roles["implement"]
	if object.Template != "worker-template" || object.Model != "role-model" || object.Effort != "high" {
		t.Fatalf("object target = %#v", object)
	}
}

func TestDelegationDeclaredRoleErrorsAreSorted(t *testing.T) {
	cfg := delegationFixture()
	cfg.Delegation.Roles = map[string]RoleSpec{"z bad": {}, "a bad": {}}
	cfg.Delegation.Profiles["primary"] = Profile{}
	for range 100 {
		err := cfg.Validate()
		if err == nil {
			t.Fatal("expected validation error")
		}
		got := err.Error()
		if strings.Index(got, `delegation.roles."a bad"`) > strings.Index(got, `delegation.roles."z bad"`) {
			t.Fatalf("role errors were not sorted: %s", got)
		}
	}
}

func TestResolveRoleAndOverrides(t *testing.T) {
	cfg := delegationFixture()
	resolved, err := cfg.ResolveRole("implement")
	if err != nil {
		t.Fatal(err)
	}
	if got := resolved.WithOverrides("request-model", "request-effort"); got.Model != "request-model" || got.Effort != "request-effort" {
		t.Fatalf("overrides = %#v", got)
	}
	if _, err := cfg.ResolveRole("review.security"); err == nil || err.Error() != `role "review.security" is not mapped in active delegation profile "primary" (mapped: implement, plan)` {
		t.Fatalf("error = %v", err)
	}
	cfg.Delegation = nil
	if _, err := cfg.ResolveRole("plan"); !errors.Is(err, ErrDelegationNotConfigured) {
		t.Fatalf("error = %v", err)
	}
}

func TestDelegationValidate(t *testing.T) {
	tests := []struct {
		name, want string
		change     func(*Config)
	}{
		{"empty active", "delegation.active_profile is required", func(c *Config) { c.Delegation.ActiveProfile = "" }},
		{"unknown active", `delegation.active_profile "missing" does not exist`, func(c *Config) { c.Delegation.ActiveProfile = "missing" }},
		{"no profiles", "delegation.profiles must not be empty", func(c *Config) { c.Delegation.Profiles = nil }},
		{"bad profile", `delegation.profiles."bad name" is not a valid name`, func(c *Config) { c.Delegation.Profiles["bad name"] = Profile{} }},
		{"bad role", `delegation.roles."bad name" is not a valid name`, func(c *Config) { c.Delegation.Roles["bad name"] = RoleSpec{} }},
		{"bad mapped role", "delegation.profiles.primary.roles.bad role is not a valid name", func(c *Config) {
			c.Delegation.Profiles["primary"] = Profile{Roles: map[string]RoleTarget{"bad role": {Template: "planner-template"}}}
		}},
		{"empty target", "delegation.profiles.primary.roles.plan.template is required", func(c *Config) { c.Delegation.Profiles["primary"] = Profile{Roles: map[string]RoleTarget{"plan": {}}} }},
		{"unknown target", `delegation.profiles.primary.roles.plan.template "missing" does not exist`, func(c *Config) {
			c.Delegation.Profiles["primary"] = Profile{Roles: map[string]RoleTarget{"plan": {Template: "missing"}}}
		}},
		{"bad model", "delegation.profiles.primary.roles.plan.model", func(c *Config) {
			c.Delegation.Profiles["primary"] = Profile{Roles: map[string]RoleTarget{"plan": {Template: "planner-template", Model: "bad model"}}}
		}},
		{"effort unsupported", "delegation.profiles.primary.roles.plan.effort: effort not supported by harness stubnochannels", func(c *Config) {
			registerStubNoChannels()
			tmpl := c.Templates["planner-template"]
			tmpl.Harness, tmpl.Model = stubNoChannelsName, ""
			c.Templates["planner-template"] = tmpl
			c.Delegation.Profiles["primary"] = Profile{Roles: map[string]RoleTarget{"plan": {Template: "planner-template", Effort: "high"}}}
		}},
		{"declared unmapped", "delegation.roles.implement is not mapped in active profile primary", func(c *Config) {
			c.Delegation.Profiles["primary"] = Profile{Roles: map[string]RoleTarget{"plan": {Template: "planner-template"}}}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := delegationFixture()
			tt.change(cfg)
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
	if err := (&Config{}).Validate(); err != nil {
		t.Fatalf("no delegation must not affect validation: %v", err)
	}
}

func TestDelegationWarningsAndRenames(t *testing.T) {
	cfg := delegationFixture()
	cfg.Delegation.Profiles["primary"] = Profile{Roles: map[string]RoleTarget{"other": {Template: "worker-template"}, "plan": {Template: "planner-template"}, "implement": {Template: "worker-template"}}}
	if got := cfg.DelegationWarnings(); len(got) != 1 || !strings.Contains(got[0], "other") {
		t.Fatalf("warnings = %v", got)
	}
	cfg.Delegation.Roles = nil
	if got := cfg.DelegationWarnings(); len(got) != 0 {
		t.Fatalf("warnings = %v", got)
	}
	cfg = delegationFixture()
	if err := RenameTemplate(cfg, "worker-template", "renamed-template"); err != nil {
		t.Fatal(err)
	}
	if got := cfg.Delegation.Profiles["primary"].Roles["implement"].Template; got != "renamed-template" {
		t.Fatalf("template = %q", got)
	}
	if err := RenameRole(cfg, "implement", "build"); err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Delegation.Roles["build"]; !ok || cfg.Delegation.Profiles["primary"].Roles["build"].Template != "renamed-template" {
		t.Fatal("role rename did not cascade")
	}
	cfg.Delegation.Roles = nil
	if err := RenameRole(cfg, "build", "compile"); err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Delegation.Profiles["primary"].Roles["compile"]; !ok {
		t.Fatal("fallback role rename did not cascade")
	}
}

func TestDelegationRenderers(t *testing.T) {
	cfg := delegationFixture()
	block := RenderDelegationInstructions(cfg)
	for _, want := range []string{"plan", "implement", "Make a design", "Write the code"} {
		if !strings.Contains(block, want) {
			t.Fatalf("block missing %q: %s", want, block)
		}
	}
	for _, forbidden := range []string{"planner-template", "worker-template", "planner-model", "worker-model", "role-model"} {
		if strings.Contains(block, forbidden) {
			t.Fatalf("block leaked %q: %s", forbidden, block)
		}
	}
	cfg.Delegation.ActiveProfile = "secondary"
	if got := RenderDelegationInstructions(cfg); got != block {
		t.Fatalf("profile switch changed instructions:\n%s", got)
	}
	cfg.Delegation.Roles = nil
	block = RenderDelegationInstructions(cfg)
	for _, want := range []string{"plan", "implement"} {
		if !strings.Contains(block, want) {
			t.Fatalf("fallback missing %q", want)
		}
	}
	status := RenderDelegationStatus(cfg)
	for _, want := range []string{"secondary", "planner-template", "worker-template"} {
		if !strings.Contains(status, want) {
			t.Fatalf("status missing %q", want)
		}
	}
}
