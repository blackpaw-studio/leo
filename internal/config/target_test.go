package config

import (
	"strings"
	"testing"
)

func TestResolveTaskTargetExplicitTemplate(t *testing.T) {
	cfg := &Config{
		Templates: map[string]TemplateConfig{"assistant": {Workspace: "/w", Channels: []string{"plugin:a@b"}}},
		Tasks:     map[string]TaskConfig{"brief": {Runtime: "persistent", Template: "assistant", Channels: []string{"plugin:a@b"}}},
	}
	name, tmpl, implicit, err := cfg.ResolveTaskTarget("brief")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if name != "assistant" || implicit || tmpl.Workspace != "/w" {
		t.Fatalf("got name=%q implicit=%v ws=%q", name, implicit, tmpl.Workspace)
	}
}

func TestResolveTaskTargetImplicit(t *testing.T) {
	cfg := &Config{Tasks: map[string]TaskConfig{"digest": {Runtime: "persistent", Workspace: "/tw", Model: "opus", Channels: []string{"plugin:a@b"}}}}
	name, tmpl, implicit, err := cfg.ResolveTaskTarget("digest")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if name != "digest" || !implicit || tmpl.Workspace != "/tw" || tmpl.Model != "opus" {
		t.Fatalf("got name=%q implicit=%v tmpl=%+v", name, implicit, tmpl)
	}
}

func TestResolveTaskTargetImplicitCarriesRuntimeFields(t *testing.T) {
	cfg := &Config{Tasks: map[string]TaskConfig{"digest": {
		Runtime:        "persistent",
		Workspace:      "/tw",
		Harness:        "codex",
		HarnessOptions: map[string]any{"sandbox": "workspace-write"},
		Env:            map[string]string{"K": "V"},
	}}}
	name, tmpl, implicit, err := cfg.ResolveTaskTarget("digest")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if name != "digest" || !implicit {
		t.Fatalf("got name=%q implicit=%v", name, implicit)
	}
	if tmpl.Harness != "codex" {
		t.Fatalf("expected harness carried, got %q", tmpl.Harness)
	}
	if tmpl.HarnessOptions["sandbox"] != "workspace-write" {
		t.Fatalf("expected harness_options carried, got %+v", tmpl.HarnessOptions)
	}
	if tmpl.Env["K"] != "V" {
		t.Fatalf("expected env carried, got %+v", tmpl.Env)
	}
}

func TestResolveTaskTargetMissingTemplate(t *testing.T) {
	cfg := &Config{Tasks: map[string]TaskConfig{"x": {Runtime: "persistent", Template: "nope"}}}
	if _, _, _, err := cfg.ResolveTaskTarget("x"); err == nil {
		t.Fatal("expected error for missing template")
	}
}

func TestResolveTaskTargetOneshotErrors(t *testing.T) {
	cfg := &Config{Tasks: map[string]TaskConfig{"o": {Runtime: "oneshot"}}}
	if _, _, _, err := cfg.ResolveTaskTarget("o"); err == nil {
		t.Fatal("expected error for non-persistent task")
	}
}

func TestValidateTaskTemplateMissing(t *testing.T) {
	cfg := &Config{
		Tasks: map[string]TaskConfig{
			"bad": {
				Runtime: "persistent", Template: "nope",
				Schedule: "0 7 * * *", PromptFile: "p.md",
			},
		},
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "templates.nope") {
		t.Fatalf("expected missing-template error, got %v", err)
	}
}

func TestValidateTaskTemplateChannelsSubset(t *testing.T) {
	cfg := &Config{
		Templates: map[string]TemplateConfig{
			"assistant": {Channels: []string{"plugin:a@b"}},
		},
		Tasks: map[string]TaskConfig{
			"bad": {
				Runtime: "persistent", Template: "assistant",
				Schedule: "0 7 * * *", PromptFile: "p.md",
				Channels: []string{"plugin:c@d"},
			},
		},
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "subset") {
		t.Fatalf("expected channel subset error, got %v", err)
	}
}

func TestValidateTaskTemplateDevChannelsSubset(t *testing.T) {
	cfg := &Config{
		Templates: map[string]TemplateConfig{
			"assistant": {DevChannels: []string{"plugin:a@b"}},
		},
		Tasks: map[string]TaskConfig{
			"bad": {
				Runtime: "persistent", Template: "assistant",
				Schedule: "0 7 * * *", PromptFile: "p.md",
				DevChannels: []string{"plugin:c@d"},
			},
		},
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "subset") {
		t.Fatalf("expected dev_channel subset error, got %v", err)
	}
}

func TestValidateTaskTemplateOneshotRejected(t *testing.T) {
	cfg := &Config{
		Templates: map[string]TemplateConfig{"assistant": {}},
		Tasks: map[string]TaskConfig{
			"bad": {
				Runtime: "oneshot", Template: "assistant",
				Schedule: "0 7 * * *", PromptFile: "p.md",
			},
		},
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "only valid when runtime: persistent") {
		t.Fatalf("expected runtime error, got %v", err)
	}
}
