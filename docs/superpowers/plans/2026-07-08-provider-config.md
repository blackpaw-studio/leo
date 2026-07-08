# Provider Config Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let any Leo process, template, session, or task run its `claude` CLI against a third-party Anthropic-compatible endpoint (GLM/z.ai, OpenRouter, etc.) via a named `providers` config map and spawn-time env injection.

**Architecture:** A `providers` map in `leo.yaml` defines endpoints + key sources. A cascading `provider` field on each scope selects one. At spawn time Leo resolves the API key (env var or command) and injects `ANTHROPIC_BASE_URL`/`ANTHROPIC_AUTH_TOKEN` into the spawned claude's environment. The resolved model always flows through the existing `--model` flag (provider `default_model` slots into the model cascade), so `ANTHROPIC_MODEL` is never needed. Arg builders are otherwise untouched.

**Tech Stack:** Go, gopkg.in/yaml.v3, standard `go test` with table-driven tests.

**Spec:** `docs/superpowers/specs/2026-07-08-provider-config-design.md`

## Global Constraints

- Secrets NEVER appear in `leo.yaml` and are NEVER persisted to disk (agentstore records store the provider *name*, not resolved keys).
- Key-resolution failure policy: interactive paths (foreground `leo service <name>`, `leo run <task>`, agent spawn/resume) return an error; daemon boot paths (supervised processes, persistent sessions, agent restore) log a warning and skip that unit so the rest of the daemon boots.
- Unset `provider` = Anthropic, exactly as today. Zero behavior change for existing configs.
- Model validation relaxes to "any non-empty string" only when a provider is resolved for that scope; otherwise the existing `sonnet/opus/haiku/sonnet[1m]/opus[1m]` enum applies.
- Model cascade with a provider: `scope.model` → `provider.default_model` → `defaults.model` → `"sonnet"`. (Sessions keep their existing "empty means no `--model` flag" behavior: `session.model` → `provider.default_model` → empty.)
- All commands run from the repo root `/Users/evan/.leo/agents/leo`. Test with `go test -race ./internal/<pkg>/`. Full check before finishing: `make test && make lint`.
- Commit after every task with a conventional-commit message.

---

### Task 1: Provider config types, fields, and validation

**Files:**
- Create: `internal/config/provider.go`
- Create: `internal/config/provider_test.go`
- Modify: `internal/config/config.go` (struct fields at lines 76–86, 182–199, 201–221, 227–239, 241–263, 267–285; validation inside `Validate()` at lines 461–691)

**Interfaces:**
- Produces: `type ProviderConfig struct{BaseURL, APIKeyEnv, APIKeyCmd, DefaultModel string}`; `Config.Providers map[string]ProviderConfig`; `Provider string` field on `DefaultsConfig`, `ProcessConfig`, `TemplateConfig`, `SessionConfig`, `TaskConfig`; cascade helpers `(c *Config) ProcessProvider(ProcessConfig) string`, `TaskProvider(TaskConfig) string`, `TemplateProvider(TemplateConfig) string`, `SessionProvider(SessionConfig) string`; `(c *Config) ProviderKeyEnvNames() []string`.

- [ ] **Step 1: Write the failing tests**

Create `internal/config/provider_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -race -run 'TestValidateProviders|TestValidateProviderReferences|TestValidateModelRelaxation|TestProviderCascade|TestProviderKeyEnvNames' ./internal/config/`
Expected: FAIL to compile — `ProviderConfig`, `Providers`, `Provider` fields undefined.

- [ ] **Step 3: Add types and helpers**

Create `internal/config/provider.go`:

```go
package config

import "sort"

// ProviderConfig defines a third-party Anthropic-Messages-compatible endpoint
// (e.g. z.ai's GLM coding endpoint, OpenRouter). When a scope resolves to a
// provider, Leo injects ANTHROPIC_BASE_URL and ANTHROPIC_AUTH_TOKEN into the
// spawned claude's environment at launch. Secrets never live in leo.yaml:
// exactly one of APIKeyEnv (env var name) or APIKeyCmd (shell command whose
// trimmed stdout is the key) must be set.
type ProviderConfig struct {
	BaseURL      string `yaml:"base_url"`
	APIKeyEnv    string `yaml:"api_key_env,omitempty"`
	APIKeyCmd    string `yaml:"api_key_cmd,omitempty"`
	DefaultModel string `yaml:"default_model,omitempty"`
}

// providerOrDefault applies the scope → defaults provider cascade.
func (c *Config) providerOrDefault(p string) string {
	if p != "" {
		return p
	}
	return c.Defaults.Provider
}

// ProcessProvider returns the effective provider name for a process.
func (c *Config) ProcessProvider(p ProcessConfig) string { return c.providerOrDefault(p.Provider) }

// TaskProvider returns the effective provider name for a task.
func (c *Config) TaskProvider(t TaskConfig) string { return c.providerOrDefault(t.Provider) }

// TemplateProvider returns the effective provider name for a template.
func (c *Config) TemplateProvider(t TemplateConfig) string { return c.providerOrDefault(t.Provider) }

// SessionProvider returns the effective provider name for a session.
func (c *Config) SessionProvider(s SessionConfig) string { return c.providerOrDefault(s.Provider) }

// ProviderDefaultModel returns the default_model of the named provider, or ""
// when the name is empty, unknown, or the provider has no default.
func (c *Config) ProviderDefaultModel(name string) string {
	if pc, ok := c.Providers[name]; ok {
		return pc.DefaultModel
	}
	return ""
}

// ProviderKeyEnvNames returns the sorted api_key_env names across all
// providers. Used to extend the daemon's environment capture so keys set in
// the operator's shell survive into launchd/systemd-managed processes.
func (c *Config) ProviderKeyEnvNames() []string {
	var names []string
	for _, p := range c.Providers {
		if p.APIKeyEnv != "" {
			names = append(names, p.APIKeyEnv)
		}
	}
	sort.Strings(names)
	return names
}
```

In `internal/config/config.go`:

Add to `Config` (after `Sessions` field, line 82):

```go
	Providers map[string]ProviderConfig `yaml:"providers,omitempty"`
```

Add `Provider string` field to each scope struct (keep yaml tag `provider,omitempty`):
- `DefaultsConfig` (after `Model`, line 183): `Provider string \`yaml:"provider,omitempty"\``
- `ProcessConfig` (after `Model`, line 205): same
- `SessionConfig` (after `Model`, line 229): same
- `TaskConfig` (after `Model`, line 246): same
- `TemplateConfig` (after `Model`, line 271): same

- [ ] **Step 4: Add validation**

In `Validate()` (`internal/config/config.go`), add `"net/url"` to imports. Insert after the defaults checks (after line 481), a providers block and a defaults.provider ref check:

```go
	for name, p := range c.Providers {
		if p.BaseURL == "" {
			errs = append(errs, fmt.Sprintf("providers.%s.base_url is required", name))
		} else if u, err := url.Parse(p.BaseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, fmt.Sprintf("providers.%s.base_url %q must be an http(s) URL", name, p.BaseURL))
		}
		hasEnv := p.APIKeyEnv != ""
		hasCmd := strings.TrimSpace(p.APIKeyCmd) != ""
		if hasEnv == hasCmd {
			errs = append(errs, fmt.Sprintf("providers.%s must set exactly one of api_key_env or api_key_cmd", name))
		}
		if hasEnv && !envKeyPattern.MatchString(p.APIKeyEnv) {
			errs = append(errs, fmt.Sprintf("providers.%s.api_key_env %q is not a valid environment variable name", name, p.APIKeyEnv))
		}
	}
	checkProviderRef := func(scope, ref string) {
		if ref == "" {
			return
		}
		if _, ok := c.Providers[ref]; !ok {
			errs = append(errs, fmt.Sprintf("%s.provider %q is not defined in providers", scope, ref))
		}
	}
	checkProviderRef("defaults", c.Defaults.Provider)
```

Change the defaults model check (line 465) to relax when a provider is set:

```go
	if c.Defaults.Model != "" && c.Defaults.Provider == "" && !validModels[c.Defaults.Model] {
```

In each scope loop, add the ref check and relax the model check. The pattern, applied four times:

Processes (replace line 508 condition; add ref check inside the loop):
```go
		checkProviderRef("processes."+name, proc.Provider)
		if proc.Model != "" && c.ProcessProvider(proc) == "" && !validModels[proc.Model] {
```

Templates (line 543):
```go
		checkProviderRef("templates."+name, tmpl.Provider)
		if tmpl.Model != "" && c.TemplateProvider(tmpl) == "" && !validModels[tmpl.Model] {
```

Sessions (line 583):
```go
		checkProviderRef("sessions."+name, sess.Provider)
		if sess.Model != "" && c.SessionProvider(sess) == "" && !validModels[sess.Model] {
```

Tasks (line 620):
```go
		checkProviderRef("tasks."+name, task.Provider)
		if task.Model != "" && c.TaskProvider(task) == "" && !validModels[task.Model] {
```

Note: `checkProviderRef` must be declared before the scope loops — declare it (and the providers block) immediately after the defaults section so it's in scope.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test -race ./internal/config/`
Expected: PASS (all existing tests too — no existing config exercises `Provider`, so behavior is unchanged).

- [ ] **Step 6: Commit**

```bash
git add internal/config/provider.go internal/config/provider_test.go internal/config/config.go
git commit -m "feat(config): providers map, cascading provider field, validation"
```

---

### Task 2: Provider-aware model resolution

**Files:**
- Modify: `internal/config/config.go` (`ProcessModel` lines 321–329, `TaskModel` lines 424–432)
- Modify: `internal/config/provider.go` (add `TemplateModel`, `SessionModel`)
- Modify: `internal/agent/args.go` (lines 23–31, inline model cascade)
- Modify: `internal/service/session.go` (`SessionSpecsFromConfig` model fields, lines 130 and 161)
- Test: `internal/config/provider_test.go` (append), existing `internal/agent` and `internal/service` tests must stay green

**Interfaces:**
- Consumes: `ProviderDefaultModel(name string) string`, cascade helpers from Task 1.
- Produces: `(c *Config) TemplateModel(t TemplateConfig) string` (cascade: template → provider default → defaults → `DefaultModel`); `(c *Config) SessionModel(s SessionConfig) string` (cascade: session → provider default → **empty**, preserving "no --model flag" behavior). `ProcessModel`/`TaskModel` gain the provider-default step between scope and defaults.

- [ ] **Step 1: Write the failing tests**

Append to `internal/config/provider_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -race -run TestModelResolutionWithProvider ./internal/config/`
Expected: FAIL to compile — `TemplateModel`, `SessionModel` undefined.

- [ ] **Step 3: Implement**

In `internal/config/config.go`, `ProcessModel` becomes:

```go
// ProcessModel returns the effective model for a process.
// Cascade: process → provider default_model → defaults → DefaultModel.
func (c *Config) ProcessModel(p ProcessConfig) string {
	if p.Model != "" {
		return p.Model
	}
	if m := c.ProviderDefaultModel(c.ProcessProvider(p)); m != "" {
		return m
	}
	if c.Defaults.Model != "" {
		return c.Defaults.Model
	}
	return DefaultModel
}
```

`TaskModel` gets the same inserted step:

```go
// TaskModel returns the effective model for a task.
// Cascade: task → provider default_model → defaults → DefaultModel.
func (c *Config) TaskModel(t TaskConfig) string {
	if t.Model != "" {
		return t.Model
	}
	if m := c.ProviderDefaultModel(c.TaskProvider(t)); m != "" {
		return m
	}
	if c.Defaults.Model != "" {
		return c.Defaults.Model
	}
	return DefaultModel
}
```

Append to `internal/config/provider.go`:

```go
// TemplateModel returns the effective model for a template.
// Cascade: template → provider default_model → defaults → DefaultModel.
func (c *Config) TemplateModel(t TemplateConfig) string {
	if t.Model != "" {
		return t.Model
	}
	if m := c.ProviderDefaultModel(c.TemplateProvider(t)); m != "" {
		return m
	}
	if c.Defaults.Model != "" {
		return c.Defaults.Model
	}
	return DefaultModel
}

// SessionModel returns the effective model for a persistent session.
// Cascade: session → provider default_model → "" (an empty result means the
// launcher omits --model and claude uses its own default, matching the
// pre-provider behavior).
func (c *Config) SessionModel(s SessionConfig) string {
	if s.Model != "" {
		return s.Model
	}
	return c.ProviderDefaultModel(c.SessionProvider(s))
}
```

In `internal/agent/args.go`, replace the inline cascade (lines 23–31):

```go
	args = append(args, "--model", cfg.TemplateModel(tmpl))
```

In `internal/service/session.go` `SessionSpecsFromConfig`, explicit sessions (line 130): `Model: cfg.SessionModel(sc),` — and implicit task sessions (line 161):

```go
		model := task.Model
		if model == "" {
			model = cfg.ProviderDefaultModel(cfg.TaskProvider(task))
		}
```

then use `Model: model,` in the SessionSpec literal.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/config/ ./internal/agent/ ./internal/service/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/ internal/agent/args.go internal/service/session.go
git commit -m "feat(config): provider default_model in model resolution cascade"
```

---

### Task 3: internal/provider package — spawn-time env resolution

**Files:**
- Create: `internal/provider/provider.go`
- Create: `internal/provider/provider_test.go`

**Interfaces:**
- Consumes: `config.Config.Providers`, `config.ProviderConfig`.
- Produces: `provider.Env(cfg *config.Config, name string) (map[string]string, error)` — returns `nil, nil` for empty name; otherwise `{"ANTHROPIC_BASE_URL": ..., "ANTHROPIC_AUTH_TOKEN": ...}` or an error. Test seams: `var lookupEnv = os.LookupEnv`, `var runCommand func(ctx, string) ([]byte, error)`.

- [ ] **Step 1: Write the failing tests**

Create `internal/provider/provider_test.go`:

```go
package provider

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

func testCfg(pc config.ProviderConfig) *config.Config {
	return &config.Config{Providers: map[string]config.ProviderConfig{"glm": pc}}
}

func withSeams(t *testing.T, env map[string]string, cmdOut string, cmdErr error) {
	t.Helper()
	origLookup, origRun := lookupEnv, runCommand
	t.Cleanup(func() { lookupEnv, runCommand = origLookup, origRun })
	lookupEnv = func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	runCommand = func(ctx context.Context, cmd string) ([]byte, error) { return []byte(cmdOut), cmdErr }
}

func TestEnv(t *testing.T) {
	t.Run("empty name is a no-op", func(t *testing.T) {
		got, err := Env(testCfg(config.ProviderConfig{}), "")
		if got != nil || err != nil {
			t.Fatalf("got %v, %v; want nil, nil", got, err)
		}
	})
	t.Run("unknown provider errors", func(t *testing.T) {
		_, err := Env(testCfg(config.ProviderConfig{}), "nope")
		if err == nil || !strings.Contains(err.Error(), `provider "nope" not found`) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("api_key_env resolves", func(t *testing.T) {
		withSeams(t, map[string]string{"GLM_API_KEY": " sk-abc \n"}, "", nil)
		got, err := Env(testCfg(config.ProviderConfig{BaseURL: "https://x.example", APIKeyEnv: "GLM_API_KEY"}), "glm")
		if err != nil {
			t.Fatal(err)
		}
		if got["ANTHROPIC_BASE_URL"] != "https://x.example" || got["ANTHROPIC_AUTH_TOKEN"] != "sk-abc" {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("api_key_env unset errors", func(t *testing.T) {
		withSeams(t, map[string]string{}, "", nil)
		_, err := Env(testCfg(config.ProviderConfig{BaseURL: "https://x.example", APIKeyEnv: "GLM_API_KEY"}), "glm")
		if err == nil || !strings.Contains(err.Error(), "GLM_API_KEY is not set") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("api_key_cmd resolves", func(t *testing.T) {
		withSeams(t, nil, "sk-cmd\n", nil)
		got, err := Env(testCfg(config.ProviderConfig{BaseURL: "https://x.example", APIKeyCmd: "op read x"}), "glm")
		if err != nil {
			t.Fatal(err)
		}
		if got["ANTHROPIC_AUTH_TOKEN"] != "sk-cmd" {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("api_key_cmd failure errors", func(t *testing.T) {
		withSeams(t, nil, "", fmt.Errorf("exit status 1"))
		_, err := Env(testCfg(config.ProviderConfig{BaseURL: "https://x.example", APIKeyCmd: "op read x"}), "glm")
		if err == nil || !strings.Contains(err.Error(), "api_key_cmd failed") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("api_key_cmd empty output errors", func(t *testing.T) {
		withSeams(t, nil, "  \n", nil)
		_, err := Env(testCfg(config.ProviderConfig{BaseURL: "https://x.example", APIKeyCmd: "op read x"}), "glm")
		if err == nil || !strings.Contains(err.Error(), "empty output") {
			t.Fatalf("got %v", err)
		}
	})
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -race ./internal/provider/`
Expected: FAIL to compile — package doesn't exist yet (create the dir with the test file first; the compile error names `Env`, `lookupEnv`, `runCommand`).

- [ ] **Step 3: Implement**

Create `internal/provider/provider.go`:

```go
// Package provider resolves the spawn-time environment for third-party
// Anthropic-compatible endpoints. Resolution happens once per spawn and the
// resolved key lives only in memory / the launched process's environment —
// callers must never persist it (see agentstore, which stores the provider
// name instead).
package provider

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
)

// apiKeyCmdTimeout bounds api_key_cmd execution so a hung secret-manager call
// (e.g. `op read` waiting on a locked keychain) can't stall a spawn forever.
const apiKeyCmdTimeout = 30 * time.Second

// Test seams.
var (
	lookupEnv  = os.LookupEnv
	runCommand = func(ctx context.Context, command string) ([]byte, error) {
		return exec.CommandContext(ctx, "sh", "-c", command).Output()
	}
)

// Env returns the environment variables to inject into a spawned claude for
// the named provider: ANTHROPIC_BASE_URL and ANTHROPIC_AUTH_TOKEN. An empty
// name means "no provider" and returns (nil, nil). The resolved model is NOT
// part of this map — it always flows through the --model flag via the config
// model-resolution cascade.
func Env(cfg *config.Config, name string) (map[string]string, error) {
	if name == "" {
		return nil, nil
	}
	pc, ok := cfg.Providers[name]
	if !ok {
		return nil, fmt.Errorf("provider %q not found in config", name)
	}
	key, err := resolveAPIKey(pc)
	if err != nil {
		return nil, fmt.Errorf("provider %q: %w", name, err)
	}
	return map[string]string{
		"ANTHROPIC_BASE_URL":   pc.BaseURL,
		"ANTHROPIC_AUTH_TOKEN": key,
	}, nil
}

func resolveAPIKey(pc config.ProviderConfig) (string, error) {
	if pc.APIKeyEnv != "" {
		v, ok := lookupEnv(pc.APIKeyEnv)
		if !ok || strings.TrimSpace(v) == "" {
			return "", fmt.Errorf("api_key_env %s is not set", pc.APIKeyEnv)
		}
		return strings.TrimSpace(v), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), apiKeyCmdTimeout)
	defer cancel()
	out, err := runCommand(ctx, pc.APIKeyCmd)
	if err != nil {
		return "", fmt.Errorf("api_key_cmd failed: %w", err)
	}
	key := strings.TrimSpace(string(out))
	if key == "" {
		return "", fmt.Errorf("api_key_cmd produced empty output")
	}
	return key, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/provider/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/provider/
git commit -m "feat(provider): spawn-time env resolution for Anthropic-compatible endpoints"
```

---

### Task 4: Daemon env capture for api_key_env

**Files:**
- Modify: `internal/env/env.go` (`Capture`, lines 16–46)
- Modify: `internal/cli/service.go` (`buildServiceConfig`, line 597)
- Test: `internal/env/env_test.go` (append or create)

**Interfaces:**
- Produces: `env.Capture(extraKeys ...string) map[string]string` — variadic, so the existing no-arg call sites compile unchanged.
- Consumes: `cfg.ProviderKeyEnvNames()` from Task 1.

Rationale: the daemon under launchd/systemd only receives the environment captured at `leo service start --daemon` time. Without this, an `api_key_env` var set in the operator's shell never reaches daemon-spawned claudes.

- [ ] **Step 1: Write the failing test**

Append to `internal/env/env_test.go` (create the file with `package env` if it doesn't exist):

```go
func TestCaptureExtraKeys(t *testing.T) {
	t.Setenv("LEO_TEST_PROVIDER_KEY", "sk-test")
	env := Capture("LEO_TEST_PROVIDER_KEY", "LEO_TEST_UNSET_KEY")
	if env["LEO_TEST_PROVIDER_KEY"] != "sk-test" {
		t.Errorf("extra key not captured: %v", env)
	}
	if _, ok := env["LEO_TEST_UNSET_KEY"]; ok {
		t.Error("unset extra key should be omitted")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -race -run TestCaptureExtraKeys ./internal/env/`
Expected: FAIL to compile — `Capture` takes no arguments.

- [ ] **Step 3: Implement**

In `internal/env/env.go`, change the signature and key loop:

```go
// Capture returns a map of environment variables relevant to Leo's daemon
// and cron processes. It ensures common user/Homebrew bin directories are in
// PATH. extraKeys adds caller-specific variables (e.g. provider api_key_env
// names) to the capture set; unset keys are omitted.
func Capture(extraKeys ...string) map[string]string {
	home, _ := userHomeDirFn()
	env := make(map[string]string)
	keys := append([]string{
		"ANTHROPIC_API_KEY",
		"CLAUDE_CODE_ENTRYPOINT",
		"HOME",
		"PATH",
		"SHELL",
		"USER",
	}, extraKeys...)
	for _, key := range keys {
		if v := os.Getenv(key); v != "" {
			env[key] = v
		}
	}
	// ... (PATH block unchanged)
```

In `internal/cli/service.go` line 597:

```go
	environ := env.Capture(cfg.ProviderKeyEnvNames()...)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/env/ ./internal/cli/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/env/ internal/cli/service.go
git commit -m "feat(env): capture provider api_key_env vars for daemon environment"
```

---

### Task 5: One-shot task runner injection

**Files:**
- Modify: `internal/run/runner.go` (`Run` lines 84–224, `notifyFailure` lines 230–253, `executeCommand` lines 265–304)
- Test: `internal/run/runner_test.go` (append)

**Interfaces:**
- Consumes: `provider.Env`, `cfg.TaskProvider(task)`.
- Produces: `executeCommand(ctx, workDir, args, channels, devChannels []string, extraEnv map[string]string)` — new trailing param; `notifyFailure(taskName, task, workspace, taskErr, attempts, extraEnv)` — new trailing param.

- [ ] **Step 1: Write the failing test**

Append to `internal/run/runner_test.go`. The package already stubs `execCommand` with a fake; here the fake returns `exec.Command("env")` (prints its environment), and `executeCommand` captures stdout, so the assertion reads the spawned process's actual env:

```go
func TestExecuteCommandInjectsExtraEnv(t *testing.T) {
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, args ...string) *exec.Cmd { return exec.Command("env") }

	out, err := executeCommand(context.Background(), t.TempDir(), nil, nil, nil,
		map[string]string{"ANTHROPIC_BASE_URL": "https://x.example", "ANTHROPIC_AUTH_TOKEN": "sk-t"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "ANTHROPIC_BASE_URL=https://x.example") ||
		!strings.Contains(string(out), "ANTHROPIC_AUTH_TOKEN=sk-t") {
		t.Fatalf("provider env missing from spawned env:\n%s", out)
	}
}

func TestExecuteCommandNoExtraEnv(t *testing.T) {
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, args ...string) *exec.Cmd { return exec.Command("env") }

	out, err := executeCommand(context.Background(), t.TempDir(), nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "ANTHROPIC_BASE_URL=") {
		t.Fatalf("unexpected provider env:\n%s", out)
	}
}
```

Ensure `TestExecuteCommandNoExtraEnv` isn't fooled by an ambient `ANTHROPIC_BASE_URL` in the host environment: call `t.Setenv("ANTHROPIC_BASE_URL", "")` first (registers restore), then `os.Unsetenv("ANTHROPIC_BASE_URL")`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -race -run TestExecuteCommand ./internal/run/`
Expected: FAIL to compile — `executeCommand` takes 5 args, not 6.

- [ ] **Step 3: Implement**

`executeCommand` (line 265) gains the param and appends it:

```go
func executeCommand(ctx context.Context, workDir string, args []string, channels, devChannels []string, extraEnv map[string]string) ([]byte, error) {
	cmd := execCommand("claude", args...)
	cmd.Dir = workDir
	env := append(os.Environ(), "CLAUDE_CODE_ENTRYPOINT=cli")
	if len(channels) > 0 {
		env = append(env, "LEO_CHANNELS="+strings.Join(channels, ","))
	}
	if len(devChannels) > 0 {
		env = append(env, "LEO_DEV_CHANNELS="+strings.Join(devChannels, ","))
	}
	for k, v := range extraEnv {
		env = append(env, k+"="+v)
	}
	cmd.Env = env
	// ... rest unchanged
```

In `Run`, after the persistent-task dispatch (line 96) and lock acquisition, resolve once (import `github.com/blackpaw-studio/leo/internal/provider`):

```go
	provEnv, err := provider.Env(cfg, cfg.TaskProvider(task))
	if err != nil {
		return fmt.Errorf("resolving provider env: %w", err)
	}
```

Pass `provEnv` to both `executeCommand` calls in the attempt loop (lines 152 and 165) and to `notifyFailure` (line 216), which gains a trailing `extraEnv map[string]string` param and forwards it to its own `executeCommand` call (line 250).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/run/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/run/
git commit -m "feat(run): inject provider env into one-shot task invocations"
```

---

### Task 6: Supervised and foreground process injection

**Files:**
- Modify: `internal/cli/service.go` (`runService` foreground path lines 100–123, `processEnviron` lines 128–140, `buildAllProcessSpecs` lines 177–204)
- Test: `internal/cli/service_test.go` (append; follow existing test patterns in that file)

**Interfaces:**
- Consumes: `provider.Env`, `cfg.ProcessProvider(proc)`.
- Produces: `processEnviron(proc config.ProcessConfig, extraEnv map[string]string) []string` — new param; `buildAllProcessSpecs` merges provider env into each `ProcessSpec.Env`, warns and **skips** a process whose provider fails to resolve.

- [ ] **Step 1: Write the failing tests**

Append to `internal/cli/service_test.go`:

```go
func TestBuildAllProcessSpecsProviderEnv(t *testing.T) {
	t.Setenv("LEO_TEST_GLM_KEY", "sk-glm")
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"glm": {BaseURL: "https://x.example", APIKeyEnv: "LEO_TEST_GLM_KEY"},
		},
		Processes: map[string]config.ProcessConfig{
			"bot":   {Enabled: true, Provider: "glm"},
			"plain": {Enabled: true},
		},
		HomePath: t.TempDir(),
	}
	specs := buildAllProcessSpecs(cfg, "/usr/bin/claude", "")
	byName := map[string]service.ProcessSpec{}
	for _, s := range specs {
		byName[s.Name] = s
	}
	if got := byName["bot"].Env["ANTHROPIC_BASE_URL"]; got != "https://x.example" {
		t.Errorf("bot ANTHROPIC_BASE_URL = %q", got)
	}
	if got := byName["bot"].Env["ANTHROPIC_AUTH_TOKEN"]; got != "sk-glm" {
		t.Errorf("bot ANTHROPIC_AUTH_TOKEN = %q", got)
	}
	if _, ok := byName["plain"].Env["ANTHROPIC_BASE_URL"]; ok {
		t.Error("plain process must not get provider env")
	}
}

func TestBuildAllProcessSpecsSkipsUnresolvableProvider(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"glm": {BaseURL: "https://x.example", APIKeyEnv: "LEO_TEST_DEFINITELY_UNSET_KEY"},
		},
		Processes: map[string]config.ProcessConfig{
			"bot":   {Enabled: true, Provider: "glm"},
			"plain": {Enabled: true},
		},
		HomePath: t.TempDir(),
	}
	specs := buildAllProcessSpecs(cfg, "/usr/bin/claude", "")
	if len(specs) != 1 || specs[0].Name != "plain" {
		t.Fatalf("expected only plain to survive, got %+v", specs)
	}
}
```

(Adjust imports to match the file; `service` is already imported in this package.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -race -run TestBuildAllProcessSpecs ./internal/cli/`
Expected: FAIL — no `ANTHROPIC_*` keys in Env / both processes present.

- [ ] **Step 3: Implement**

In `buildAllProcessSpecs` (import `github.com/blackpaw-studio/leo/internal/provider`), inside the loop after the enabled check:

```go
		provEnv, provErr := provider.Env(cfg, cfg.ProcessProvider(proc))
		if provErr != nil {
			warn.Printf("  [%s] provider env unavailable: %v — skipping process\n", name, provErr)
			continue
		}
```

and replace `Env: mergeChannelsIntoEnv(proc)` with:

```go
		procEnv := mergeChannelsIntoEnv(proc)
		for k, v := range provEnv {
			procEnv[k] = v
		}
```

(`Env: procEnv` in the spec literal.)

Foreground path in `runService` (line 121): `processEnviron` gains a param —

```go
// processEnviron augments the current environment with LEO_CHANNELS and
// LEO_DEV_CHANNELS (if any), per-process env vars, and any provider env.
// Returned slice is safe to pass to syscall.Exec.
func processEnviron(proc config.ProcessConfig, extraEnv map[string]string) []string {
	env := os.Environ()
	if len(proc.Channels) > 0 {
		env = append(env, "LEO_CHANNELS="+strings.Join(proc.Channels, ","))
	}
	if len(proc.DevChannels) > 0 {
		env = append(env, "LEO_DEV_CHANNELS="+strings.Join(proc.DevChannels, ","))
	}
	for k, v := range proc.Env {
		env = append(env, k+"="+v)
	}
	for k, v := range extraEnv {
		env = append(env, k+"="+v)
	}
	return env
}
```

and the caller (interactive → fail hard):

```go
	provEnv, err := provider.Env(cfg, cfg.ProcessProvider(proc))
	if err != nil {
		return fmt.Errorf("resolving provider env: %w", err)
	}

	info.Printf("Starting session (%s)...\n", procName)
	procEnv := processEnviron(proc, provEnv)
	return syscall.Exec(claudePath, append([]string{"claude"}, claudeArgs...), procEnv)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/cli/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/
git commit -m "feat(service): inject provider env into supervised and foreground processes"
```

---

### Task 7: Persistent session injection

**Files:**
- Modify: `internal/service/session.go` (`SessionSpecsFromConfig` lines 115–170)
- Test: `internal/service/session_test.go` (append; follow existing patterns)

**Interfaces:**
- Consumes: `provider.Env`, `cfg.SessionProvider(sc)`, `cfg.TaskProvider(task)`; Task 2 already set `SessionSpec.Model`.
- Produces: `SessionSpec.Env` includes resolved provider vars; unresolvable provider → warn to `os.Stderr` and skip that session (others still boot). No signature changes.

- [ ] **Step 1: Write the failing tests**

Append to `internal/service/session_test.go`:

```go
func TestSessionSpecsProviderEnv(t *testing.T) {
	t.Setenv("LEO_TEST_GLM_KEY", "sk-glm")
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"glm": {BaseURL: "https://x.example", APIKeyEnv: "LEO_TEST_GLM_KEY", DefaultModel: "glm-5.2"},
		},
		Sessions: map[string]config.SessionConfig{
			"research": {Workspace: "/tmp/w", Provider: "glm", Env: map[string]string{"FOO": "bar"}},
		},
	}
	specs, err := SessionSpecsFromConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 1 {
		t.Fatalf("got %d specs", len(specs))
	}
	s := specs[0]
	if s.Env["ANTHROPIC_BASE_URL"] != "https://x.example" || s.Env["ANTHROPIC_AUTH_TOKEN"] != "sk-glm" {
		t.Errorf("provider env missing: %v", s.Env)
	}
	if s.Env["FOO"] != "bar" {
		t.Errorf("configured env lost: %v", s.Env)
	}
	if s.Model != "glm-5.2" {
		t.Errorf("Model = %q, want provider default", s.Model)
	}
}

func TestSessionSpecsSkipsUnresolvableProvider(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"glm": {BaseURL: "https://x.example", APIKeyEnv: "LEO_TEST_DEFINITELY_UNSET_KEY"},
		},
		Sessions: map[string]config.SessionConfig{
			"broken": {Workspace: "/tmp/w", Provider: "glm"},
			"fine":   {Workspace: "/tmp/w2"},
		},
	}
	specs, err := SessionSpecsFromConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 1 || specs[0].Name != "fine" {
		t.Fatalf("expected only fine to survive, got %+v", specs)
	}
}

func TestImplicitSessionProviderEnv(t *testing.T) {
	t.Setenv("LEO_TEST_GLM_KEY", "sk-glm")
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"glm": {BaseURL: "https://x.example", APIKeyEnv: "LEO_TEST_GLM_KEY"},
		},
		Tasks: map[string]config.TaskConfig{
			"digest": {Schedule: "0 * * * *", PromptFile: "p.md", Runtime: "persistent", Provider: "glm"},
		},
	}
	specs, err := SessionSpecsFromConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 1 || specs[0].Env["ANTHROPIC_AUTH_TOKEN"] != "sk-glm" {
		t.Fatalf("implicit session missing provider env: %+v", specs)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -race -run 'TestSessionSpecs|TestImplicitSession' ./internal/service/`
Expected: FAIL — no `ANTHROPIC_*` keys / both sessions present.

- [ ] **Step 3: Implement**

In `SessionSpecsFromConfig` (import `os` and `github.com/blackpaw-studio/leo/internal/provider`), add a merge helper usage in both loops. Explicit sessions:

```go
	for name, sc := range cfg.Sessions {
		provEnv, provErr := provider.Env(cfg, cfg.SessionProvider(sc))
		if provErr != nil {
			fmt.Fprintf(os.Stderr, "warning: session %q provider env unavailable: %v — skipping session\n", name, provErr)
			continue
		}
		env := make(map[string]string, len(sc.Env)+len(provEnv))
		for k, v := range sc.Env {
			env[k] = v
		}
		for k, v := range provEnv {
			env[k] = v
		}
		out = append(out, SessionSpec{
			// ... existing fields, with:
			Env: env,
		})
	}
```

Implicit task sessions (TaskConfig has no Env):

```go
		provEnv, provErr := provider.Env(cfg, cfg.TaskProvider(task))
		if provErr != nil {
			fmt.Fprintf(os.Stderr, "warning: task %q provider env unavailable: %v — skipping session\n", name, provErr)
			continue
		}
```

and set `Env: provEnv` in the implicit SessionSpec literal (nil when no provider — same as today).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/service/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/service/
git commit -m "feat(service): inject provider env into persistent task sessions"
```

---

### Task 8: Ephemeral agents — spawn, resume, restore

**Files:**
- Modify: `internal/agentstore/store.go` (`Record` struct, line ~33)
- Modify: `internal/agent/manager.go` (`spawnShared` ~lines 190–245, `spawnWorktree` ~lines 300–365, `Resume` ~lines 540–585)
- Modify: `internal/service/agents.go` (`RestoreAgents` line 42 and spec construction ~line 124)
- Modify: `internal/service/process.go` (call site line 512)
- Test: `internal/agent/manager_test.go`, `internal/service/agents_test.go` (append; follow existing fake-supervisor patterns in those files)

**Interfaces:**
- Consumes: `provider.Env`, `cfg.TemplateProvider(tmpl)`.
- Produces: `agentstore.Record.Provider string \`json:"provider,omitempty"\`` (name only — NEVER the key); `RestoreAgents(homePath, tmuxPath, webToken string, sv agentSpawner, resolveEnv func(string) (map[string]string, error)) int` — nil `resolveEnv` skips provider resolution.

Critical constraint: the resolved key goes ONLY into the in-memory `SpawnRequest.Env` / `daemon.AgentSpawnSpec.Env` passed to the supervisor. The `agentstore.Record.Env` persisted to disk keeps the *configured* env exactly as today, plus the new `Provider` name field.

- [ ] **Step 1: Write the failing tests**

Append to `internal/agent/manager_test.go` (use the existing fake supervisor/cfgLoader helpers in that file; the test below sketches the assertions — adapt construction to the file's established helpers):

```go
func TestSpawnInjectsProviderEnvWithoutPersistingKey(t *testing.T) {
	t.Setenv("LEO_TEST_GLM_KEY", "sk-glm")
	// Arrange: cfg with providers.glm + a template with Provider: "glm",
	// fake supervisor capturing the SpawnRequest, temp HomePath.
	// (Use the package's existing newTestManager/fake-supervisor pattern.)

	// Act: m.Spawn(ctx, SpawnSpec{Template: "tpl", Name: "a1", ...})

	// Assert 1: captured SpawnRequest.Env contains ANTHROPIC_BASE_URL and
	// ANTHROPIC_AUTH_TOKEN=sk-glm.
	// Assert 2: the persisted agentstore record has Provider == "glm" and its
	// Env does NOT contain ANTHROPIC_AUTH_TOKEN or ANTHROPIC_BASE_URL.
}

func TestSpawnFailsWhenProviderUnresolvable(t *testing.T) {
	// Arrange: same, but api_key_env points at an unset var.
	// Act + Assert: Spawn returns an error mentioning the provider; the fake
	// supervisor saw no SpawnAgent call; no agentstore record was written.
}
```

Append to `internal/service/agents_test.go`:

```go
func TestRestoreAgentsResolvesProviderEnv(t *testing.T) {
	// Arrange: write an agentstore record with Provider: "glm" and
	// Env: {"FOO": "bar"}; fake agentSpawner capturing specs.
	// resolveEnv := func(name string) (map[string]string, error) {
	//     if name != "glm" { t.Fatalf("unexpected provider %q", name) }
	//     return map[string]string{"ANTHROPIC_AUTH_TOKEN": "sk-glm"}, nil
	// }
	// Act: RestoreAgents(home, "/usr/bin/tmux", "", fake, resolveEnv)
	// Assert: captured spec.Env has both FOO=bar and ANTHROPIC_AUTH_TOKEN=sk-glm.
}

func TestRestoreAgentsSkipsUnresolvableProvider(t *testing.T) {
	// resolveEnv returns an error → the agent is NOT spawned, the record is
	// NOT removed (so a later daemon restart with the key present recovers it),
	// and RestoreAgents returns 0 for it. Other records still restore.
}

func TestRestoreAgentsNilResolver(t *testing.T) {
	// A record with Provider set + nil resolveEnv → restored with rec.Env
	// as-is (no provider vars, no crash).
}
```

Flesh these out against the real helper shapes in each test file — the assertions above are the contract; the arrangement code must follow the package's existing fakes.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -race ./internal/agent/ ./internal/service/`
Expected: FAIL to compile — `Record.Provider` undefined, `RestoreAgents` arity mismatch.

- [ ] **Step 3: Implement**

`internal/agentstore/store.go` — add to `Record` after `Env`:

```go
	// Provider is the leo.yaml provider name resolved from the template at
	// spawn time. Persisted so restore/resume re-resolves the same endpoint.
	// The resolved API key is intentionally NOT persisted — it is re-resolved
	// on every spawn and lives only in the launched process's environment.
	Provider string `json:"provider,omitempty"`
```

`internal/agent/manager.go` — in `spawnShared` (after `env := mergeEnv(tmpl.Env, spec.Env)`, line 199) and identically in `spawnWorktree` (line 306):

```go
	provName := cfg.TemplateProvider(tmpl)
	provEnv, err := provider.Env(cfg, provName)
	if err != nil {
		return Record{}, fmt.Errorf("resolving provider env: %w", err)
	}
	runEnv := mergeEnv(env, provEnv)
```

Pass `runEnv` as `Env:` in the `SpawnRequest`; keep `env` (configured only) in the `agentstore.Record` and manager `Record` literals, and add `Provider: provName` to the `agentstore.Record`.

Note: in `spawnWorktree` the provider resolution must happen BEFORE the supervisor reservation/worktree creation if practical, or with the existing cleanup path on error — follow the function's existing error-unwind pattern so a failed resolution doesn't leak a reservation or worktree.

In `Resume` (~line 557), after loading `rec`:

```go
	provEnv, err := provider.Env(cfg, rec.Provider)
	if err != nil {
		return Record{}, fmt.Errorf("resolving provider env: %w", err)
	}
```

and `Env: mergeEnv(rec.Env, provEnv)` in its `SpawnRequest`.

`internal/service/agents.go` — signature:

```go
func RestoreAgents(homePath, tmuxPath, webToken string, sv agentSpawner, resolveEnv func(string) (map[string]string, error)) int {
```

In the per-record loop, before building the spec:

```go
		specEnv := rec.Env
		if rec.Provider != "" && resolveEnv != nil {
			provEnv, provErr := resolveEnv(rec.Provider)
			if provErr != nil {
				fmt.Fprintf(os.Stderr, "restore: agent %q provider env unavailable: %v — skipping (record kept)\n", name, provErr)
				continue
			}
			merged := make(map[string]string, len(rec.Env)+len(provEnv))
			for k, v := range rec.Env {
				merged[k] = v
			}
			for k, v := range provEnv {
				merged[k] = v
			}
			specEnv = merged
		}
```

and `Env: specEnv` in the `daemon.AgentSpawnSpec`.

`internal/service/process.go` line 512 — build the resolver from the already-available `configPath` (a `cfg` load already happens just above for `StartWeb`; hoist it or load again):

```go
	resolveEnv := func(name string) (map[string]string, error) {
		cfg, err := config.Load(configPath)
		if err != nil {
			return nil, fmt.Errorf("loading config: %w", err)
		}
		return provider.Env(cfg, name)
	}
	restored := RestoreAgents(homePath, tmuxPath, webToken, supervisor, resolveEnv)
```

(Import `github.com/blackpaw-studio/leo/internal/provider` in `internal/service`.) Update any other `RestoreAgents` callers/tests to pass `nil`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/agent/ ./internal/agentstore/ ./internal/service/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/ internal/agentstore/ internal/service/
git commit -m "feat(agent): provider env for ephemeral agents; persist provider name, never keys"
```

---

### Task 9: Docs + full verification

**Files:**
- Create: `docs/configuration/providers.md`
- Modify: `mkdocs.yml` (nav section, ~line 76 — add Providers under Configuration)
- Modify: `docs/configuration/config-reference.md` (add `providers` section + `provider` field rows following the file's existing table format)
- Modify: `CLAUDE.md` (Config section: add `providers` to the key-sections list; add a one-line note that `provider` cascades and injects `ANTHROPIC_BASE_URL`/`ANTHROPIC_AUTH_TOKEN`)

- [ ] **Step 1: Write `docs/configuration/providers.md`**

Content (match the tone/format of the sibling docs — check `persistent-tasks.md` for heading conventions):

```markdown
# Providers — Third-Party Models

Leo can run any process, template, session, or task against a third-party
model exposed through an Anthropic-Messages-compatible endpoint (z.ai GLM,
OpenRouter, Moonshot, DeepSeek, MiniMax, …). Leo keeps using the stock
`claude` CLI — it just points it at a different backend via environment
variables at spawn time.

## Defining providers

```yaml
providers:
  glm:
    base_url: https://api.z.ai/api/coding/paas/v4
    api_key_env: GLM_API_KEY          # name of an env var holding the key
    default_model: glm-5.2
  openrouter:
    base_url: https://openrouter.ai/api
    api_key_cmd: op read "op://Vault/OpenRouter/api-key"   # stdout = key
```

| Field | Required | Meaning |
|---|---|---|
| `base_url` | yes | Anthropic-compatible endpoint; injected as `ANTHROPIC_BASE_URL` |
| `api_key_env` | one of | Env var holding the API key |
| `api_key_cmd` | one of | Shell command whose trimmed stdout is the key |
| `default_model` | no | Model used when the scope doesn't set `model:` |

Exactly one of `api_key_env` / `api_key_cmd` is required. Keys never live in
`leo.yaml` and are never written to Leo's state files.

## Selecting a provider

`provider:` is available on `defaults`, `processes.*`, `templates.*`,
`sessions.*`, and `tasks.*`, and cascades from `defaults` like every other
setting. Unset means Anthropic, exactly as before.

```yaml
processes:
  scout:
    provider: glm
    model: glm-5.2      # any string is allowed once a provider is set
```

Model cascade with a provider: `model:` on the scope → the provider's
`default_model` → `defaults.model` → `sonnet`.

## Switching and failure behavior

Switching is manual: edit `provider:` and restart the process / respawn the
agent. There is no automatic rate-limit failover.

If a key can't be resolved at spawn time, interactive commands fail with an
error; at daemon boot the affected process/session/agent is skipped with a
warning and everything else starts normally.

`api_key_env` caveat: the daemon captures its environment when you run
`leo service start --daemon`. After exporting a new key var, run that command
again — or use `api_key_cmd` (e.g. `op read …`), which resolves fresh on
every spawn.

## What to expect from third-party models

Client-side features (channel plugins, skills, hooks, MCP, sessions) work
unchanged. Tool-call fidelity may degrade at long context on non-Claude
models, vision is unavailable on most third-party endpoints, and
Claude-specific API features depend on the backend.
```

- [ ] **Step 2: Wire into mkdocs nav and config reference**

Add `- Providers: configuration/providers.md` to the Configuration nav group in `mkdocs.yml`. In `docs/configuration/config-reference.md`, add the `providers` map and per-scope `provider` field following the file's existing structure. In `CLAUDE.md`, extend the Config section list with `providers` and the per-scope `provider` field.

- [ ] **Step 3: Full verification**

Run: `make test && make lint`
Expected: all packages PASS, no lint findings.

Also run: `mkdocs build --strict 2>/dev/null || true` — if mkdocs is installed locally, confirm no nav warnings; if not installed, skip (CI covers it).

- [ ] **Step 4: Commit**

```bash
git add docs/configuration/providers.md docs/configuration/config-reference.md mkdocs.yml CLAUDE.md
git commit -m "docs: provider configuration guide"
```
