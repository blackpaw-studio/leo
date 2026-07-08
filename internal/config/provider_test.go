package config

import (
	"strings"
	"testing"
)

func validProviderCfg() *Config {
	return &Config{
		Providers: map[string]ProviderConfig{
			"glm": {BaseURL: "https://api.z.ai/api/coding/paas/v4", APIKeyEnv: "GLM_API_KEY", DefaultModel: "glm-5.2"},
		},
	}
}

func TestValidateProviders(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string // substring expected in error; "" = expect valid
	}{
		{name: "valid provider passes", mutate: func(c *Config) {}},
		{name: "missing base_url", mutate: func(c *Config) {
			c.Providers["glm"] = ProviderConfig{APIKeyEnv: "GLM_API_KEY"}
		}, wantErr: "providers.glm.base_url is required"},
		{name: "non-http base_url", mutate: func(c *Config) {
			c.Providers["glm"] = ProviderConfig{BaseURL: "ftp://x", APIKeyEnv: "K"}
		}, wantErr: "must be an http(s) URL"},
		{name: "unparseable base_url", mutate: func(c *Config) {
			c.Providers["glm"] = ProviderConfig{BaseURL: "https://", APIKeyEnv: "K"}
		}, wantErr: "must be an http(s) URL"},
		{name: "both key sources", mutate: func(c *Config) {
			c.Providers["glm"] = ProviderConfig{BaseURL: "https://x.example", APIKeyEnv: "K", APIKeyCmd: "op read x"}
		}, wantErr: "exactly one of api_key_env or api_key_cmd"},
		{name: "neither key source", mutate: func(c *Config) {
			c.Providers["glm"] = ProviderConfig{BaseURL: "https://x.example"}
		}, wantErr: "exactly one of api_key_env or api_key_cmd"},
		{name: "invalid api_key_env name", mutate: func(c *Config) {
			c.Providers["glm"] = ProviderConfig{BaseURL: "https://x.example", APIKeyEnv: "BAD-NAME"}
		}, wantErr: "is not a valid environment variable name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validProviderCfg()
			tt.mutate(cfg)
			err := cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("expected valid, got: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got: %v", tt.wantErr, err)
			}
		})
	}
}

func TestValidateProviderReferences(t *testing.T) {
	t.Run("dangling process provider ref fails", func(t *testing.T) {
		cfg := validProviderCfg()
		cfg.Processes = map[string]ProcessConfig{"bot": {Provider: "nope", Enabled: true}}
		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), `processes.bot.provider "nope" is not defined`) {
			t.Fatalf("expected dangling ref error, got: %v", err)
		}
	})
	t.Run("dangling refs fail on every scope", func(t *testing.T) {
		cfg := validProviderCfg()
		cfg.Defaults.Provider = "nope"
		cfg.Tasks = map[string]TaskConfig{"job": {Schedule: "0 * * * *", PromptFile: "p.md", Provider: "nope"}}
		cfg.Templates = map[string]TemplateConfig{"tpl": {Provider: "nope"}}
		cfg.Sessions = map[string]SessionConfig{"sess": {Workspace: "/tmp/w", Provider: "nope"}}
		err := cfg.Validate()
		if err == nil {
			t.Fatal("expected error")
		}
		for _, want := range []string{"defaults.provider", "tasks.job.provider", "templates.tpl.provider", "sessions.sess.provider"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("missing %q in: %v", want, err)
			}
		}
	})
}

func TestValidateModelRelaxation(t *testing.T) {
	t.Run("non-claude model with provider passes", func(t *testing.T) {
		cfg := validProviderCfg()
		cfg.Processes = map[string]ProcessConfig{"bot": {Provider: "glm", Model: "glm-5.2", Enabled: true}}
		cfg.Tasks = map[string]TaskConfig{"job": {Schedule: "0 * * * *", PromptFile: "p.md", Provider: "glm", Model: "glm-5.2"}}
		cfg.Templates = map[string]TemplateConfig{"tpl": {Provider: "glm", Model: "glm-5.2"}}
		cfg.Sessions = map[string]SessionConfig{"sess": {Workspace: "/tmp/w", Provider: "glm", Model: "glm-5.2"}}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("expected valid, got: %v", err)
		}
	})
	t.Run("non-claude model without provider still fails", func(t *testing.T) {
		cfg := validProviderCfg()
		cfg.Processes = map[string]ProcessConfig{"bot": {Model: "glm-5.2", Enabled: true}}
		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), "processes.bot.model") {
			t.Fatalf("expected model error, got: %v", err)
		}
	})
	t.Run("defaults.provider relaxes all scopes", func(t *testing.T) {
		cfg := validProviderCfg()
		cfg.Defaults.Provider = "glm"
		cfg.Defaults.Model = "glm-5.2"
		cfg.Processes = map[string]ProcessConfig{"bot": {Model: "glm-5-turbo", Enabled: true}}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("expected valid, got: %v", err)
		}
	})
}

func TestProviderCascade(t *testing.T) {
	cfg := validProviderCfg()
	cfg.Defaults.Provider = "glm"
	if got := cfg.ProcessProvider(ProcessConfig{}); got != "glm" {
		t.Errorf("ProcessProvider fallback = %q, want glm", got)
	}
	if got := cfg.ProcessProvider(ProcessConfig{Provider: "other"}); got != "other" {
		t.Errorf("ProcessProvider override = %q, want other", got)
	}
	if got := cfg.TaskProvider(TaskConfig{}); got != "glm" {
		t.Errorf("TaskProvider fallback = %q, want glm", got)
	}
	if got := cfg.TemplateProvider(TemplateConfig{Provider: "other"}); got != "other" {
		t.Errorf("TemplateProvider override = %q, want other", got)
	}
	if got := cfg.SessionProvider(SessionConfig{}); got != "glm" {
		t.Errorf("SessionProvider fallback = %q, want glm", got)
	}
}

func TestModelResolutionWithProvider(t *testing.T) {
	cfg := validProviderCfg() // glm has DefaultModel "glm-5.2"
	cfg.Defaults.Model = "opus"

	t.Run("scope model wins", func(t *testing.T) {
		if got := cfg.ProcessModel(ProcessConfig{Provider: "glm", Model: "glm-5-turbo"}); got != "glm-5-turbo" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("provider default beats global default", func(t *testing.T) {
		if got := cfg.ProcessModel(ProcessConfig{Provider: "glm"}); got != "glm-5.2" {
			t.Errorf("got %q, want glm-5.2", got)
		}
		if got := cfg.TaskModel(TaskConfig{Provider: "glm"}); got != "glm-5.2" {
			t.Errorf("got %q, want glm-5.2", got)
		}
		if got := cfg.TemplateModel(TemplateConfig{Provider: "glm"}); got != "glm-5.2" {
			t.Errorf("got %q, want glm-5.2", got)
		}
	})
	t.Run("no provider keeps existing cascade", func(t *testing.T) {
		if got := cfg.ProcessModel(ProcessConfig{}); got != "opus" {
			t.Errorf("got %q, want opus", got)
		}
	})
	t.Run("provider without default_model falls to defaults", func(t *testing.T) {
		cfg2 := validProviderCfg()
		cfg2.Providers["glm"] = ProviderConfig{BaseURL: "https://x.example", APIKeyEnv: "K"}
		cfg2.Defaults.Model = "opus"
		if got := cfg2.ProcessModel(ProcessConfig{Provider: "glm"}); got != "opus" {
			t.Errorf("got %q, want opus", got)
		}
	})
	t.Run("session model stays empty without provider default", func(t *testing.T) {
		if got := cfg.SessionModel(SessionConfig{}); got != "" {
			t.Errorf("got %q, want empty", got)
		}
		if got := cfg.SessionModel(SessionConfig{Provider: "glm"}); got != "glm-5.2" {
			t.Errorf("got %q, want glm-5.2", got)
		}
	})
}

func TestProviderKeyEnvNames(t *testing.T) {
	cfg := &Config{Providers: map[string]ProviderConfig{
		"b": {BaseURL: "https://b.example", APIKeyEnv: "B_KEY"},
		"a": {BaseURL: "https://a.example", APIKeyEnv: "A_KEY"},
		"c": {BaseURL: "https://c.example", APIKeyCmd: "op read x"},
	}}
	got := cfg.ProviderKeyEnvNames()
	if len(got) != 2 || got[0] != "A_KEY" || got[1] != "B_KEY" {
		t.Fatalf("ProviderKeyEnvNames = %v, want [A_KEY B_KEY]", got)
	}
}
