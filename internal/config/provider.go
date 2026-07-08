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
