package config

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func minimalValidConfig() *Config {
	return &Config{
		Defaults: DefaultsConfig{Model: "sonnet", MaxTurns: 15},
		Tasks: map[string]TaskConfig{
			"job": {Schedule: "0 * * * *", PromptFile: "p.md", Enabled: true},
		},
		HomePath: "/tmp/leo",
	}
}

func envCfg() *Config {
	return &Config{
		Defaults: DefaultsConfig{Environments: []string{"base"}},
		Environments: map[string]map[string]string{
			"base":   {"FOO": "base", "SHARED": "base"},
			"acct-b": {"CLAUDE_CONFIG_DIR": "/b", "SHARED": "b"},
			"empty":  {},
		},
	}
}

func TestEnvironmentNamesCascade(t *testing.T) {
	c := envCfg()
	tests := []struct {
		name            string
		override, scope []string
		want            []string
	}{
		{"defaults when nothing set", nil, nil, []string{"base"}},
		{"scope replaces defaults", nil, []string{"acct-b"}, []string{"acct-b"}},
		{"empty scope clears defaults", nil, []string{}, []string{}},
		{"override beats scope", []string{"empty"}, []string{"acct-b"}, []string{"empty"}},
		{"empty override clears scope", []string{}, []string{"acct-b"}, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := c.EnvironmentNames(tt.override, tt.scope)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestEnvironmentNamesReturnsCopy(t *testing.T) {
	c := envCfg()
	got := c.EnvironmentNames(nil, nil)
	got[0] = "mutated"
	if c.Defaults.Environments[0] != "base" {
		t.Fatal("EnvironmentNames aliased the defaults slice")
	}
}

func TestMergeEnvironmentsLaterWins(t *testing.T) {
	c := envCfg()
	got, err := c.MergeEnvironments([]string{"base", "acct-b"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"FOO": "base", "SHARED": "b", "CLAUDE_CONFIG_DIR": "/b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	got, _ = c.MergeEnvironments([]string{"acct-b", "base"})
	if got["SHARED"] != "base" {
		t.Fatalf("order must decide precedence, got SHARED=%q", got["SHARED"])
	}
}

func TestMergeEnvironmentsUnknownName(t *testing.T) {
	_, err := envCfg().MergeEnvironments([]string{"base", "nope"})
	var unknown *UnknownEnvironmentError
	if !errors.As(err, &unknown) || unknown.Name != "nope" {
		t.Fatalf("want UnknownEnvironmentError{nope}, got %v", err)
	}
}

func TestResolveEnvPrecedence(t *testing.T) {
	c := envCfg()
	literal := map[string]string{"SHARED": "literal", "LIT": "1"}
	spawn := map[string]string{"SHARED": "spawn"}
	got, err := c.ResolveEnv([]string{"base", "acct-b"}, literal, spawn)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"FOO": "base", "CLAUDE_CONFIG_DIR": "/b", "LIT": "1", "SHARED": "spawn"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if literal["SHARED"] != "literal" || len(literal) != 2 || len(spawn) != 1 {
		t.Fatal("inputs were mutated")
	}
}

func TestResolveEnvNoLayersIsNil(t *testing.T) {
	got, err := (&Config{}).ResolveEnv(nil, nil, nil)
	if err != nil || got != nil {
		t.Fatalf("got %v, %v; want nil, nil", got, err)
	}
}

func TestValidateEnvironments(t *testing.T) {
	base := func() *Config {
		c := minimalValidConfig()
		c.Environments = map[string]map[string]string{"a": {"X": "1"}, "b": {"Y": "2"}}
		return c
	}
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"unknown in defaults", func(c *Config) { c.Defaults.Environments = []string{"zzz"} }, `defaults.environments: unknown environment "zzz"`},
		{"unknown in template", func(c *Config) {
			c.Templates = map[string]TemplateConfig{"t": {Environments: []string{"a", "zzz"}}}
		}, `templates.t.environments: unknown environment "zzz"`},
		{"unknown in task", func(c *Config) {
			t := c.Tasks["job"]
			t.Environments = []string{"zzz"}
			c.Tasks["job"] = t
		}, `tasks.job.environments: unknown environment "zzz"`},
		{"duplicate in list", func(c *Config) { c.Defaults.Environments = []string{"a", "b", "a"} }, `defaults.environments: duplicate environment "a"`},
		{"bad key", func(c *Config) { c.Environments["a"] = map[string]string{"1BAD": "x"} }, `environments.a key "1BAD"`},
		{"bad name", func(c *Config) { c.Environments["a,b"] = map[string]string{"X": "1"} }, `environments name "a,b"`},
		{"environments on persistent task with template", func(c *Config) {
			c.Templates = map[string]TemplateConfig{"t": {}}
			c.Tasks["job"] = TaskConfig{Schedule: "0 * * * *", PromptFile: "p.md", Runtime: "persistent", Template: "t", Environments: []string{"a"}}
		}, `tasks.job.environments`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := base()
			tt.mutate(c)
			err := c.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want error containing %q, got %v", tt.want, err)
			}
		})
	}
	if err := base().Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestEnvironmentWarningsFlagLiteralTilde(t *testing.T) {
	c := minimalValidConfig()
	c.Environments = map[string]map[string]string{
		"acct-b": {"CLAUDE_CONFIG_DIR": "~/.claude-b", "CODEX_HOME": "~/.codex-b", "FOO": "~/ok"},
		"fine":   {"CLAUDE_CONFIG_DIR": "/Users/evan/.claude-b"},
	}
	got := c.EnvironmentWarnings()
	if len(got) != 2 {
		t.Fatalf("want 2 warnings, got %v", got)
	}
	for _, w := range got {
		if !strings.Contains(w, "acct-b") || !strings.Contains(w, "~") {
			t.Fatalf("unexpected warning %q", w)
		}
	}
}
