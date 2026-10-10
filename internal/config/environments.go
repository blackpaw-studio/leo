package config

import (
	"fmt"
	"maps"
	"sort"
	"strings"
)

// pathEnvKeys are the env keys whose values are filesystem paths that leo and
// the harnesses read verbatim — a leading "~" is never expanded.
var pathEnvKeys = []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME"}

// UnknownEnvironmentError reports a reference to an environment name that is
// not defined under the top-level `environments:` map.
type UnknownEnvironmentError struct{ Name string }

func (e *UnknownEnvironmentError) Error() string {
	return fmt.Sprintf("unknown environment %q", e.Name)
}

// EnvironmentNames returns the effective ordered environment list for a
// scope. The most specific level that is set (non-nil) replaces the list from
// less specific levels: override (spawn) > scope (task/template) > defaults.
// An explicitly empty list counts as set. The result is always a copy.
func (c *Config) EnvironmentNames(override, scope []string) []string {
	switch {
	case override != nil:
		return append([]string{}, override...)
	case scope != nil:
		return append([]string{}, scope...)
	default:
		return append([]string{}, c.Defaults.Environments...)
	}
}

// MergeEnvironments merges the named environments left to right; when two
// names set the same key the later one wins. An undefined name returns an
// *UnknownEnvironmentError. The result is a new map, nil when names is empty.
func (c *Config) MergeEnvironments(names []string) (map[string]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	merged := map[string]string{}
	for _, name := range names {
		env, ok := c.Environments[name]
		if !ok {
			return nil, &UnknownEnvironmentError{Name: name}
		}
		maps.Copy(merged, env)
	}
	return merged, nil
}

// ResolveEnv layers the final environment, lowest to highest precedence:
// merged named environments, the literal `env:` map of a template or task,
// then per-spawn `--env K=V` pairs. Inputs are not mutated. Returns nil when
// every layer is empty.
func (c *Config) ResolveEnv(names []string, literal, spawn map[string]string) (map[string]string, error) {
	named, err := c.MergeEnvironments(names)
	if err != nil {
		return nil, err
	}
	if len(named) == 0 && len(literal) == 0 && len(spawn) == 0 {
		return nil, nil
	}
	out := map[string]string{}
	maps.Copy(out, named)
	maps.Copy(out, literal)
	maps.Copy(out, spawn)
	return out, nil
}

// validateEnvironments checks the top-level environments map and every
// `environments:` list that references it.
func (c *Config) validateEnvironments() []string {
	var errs []string
	for name, env := range c.Environments {
		if name == "" || strings.ContainsAny(name, ", \t\n") {
			errs = append(errs, fmt.Sprintf("environments name %q must be non-empty and contain no commas or whitespace", name))
		}
		for k := range env {
			if !envKeyPattern.MatchString(k) {
				errs = append(errs, fmt.Sprintf("environments.%s key %q is not a valid environment variable name", name, k))
			}
		}
	}
	errs = append(errs, c.validateEnvironmentList("defaults.environments", c.Defaults.Environments)...)
	for name, tmpl := range c.Templates {
		errs = append(errs, c.validateEnvironmentList("templates."+name+".environments", tmpl.Environments)...)
	}
	for name, task := range c.Tasks {
		errs = append(errs, c.validateEnvironmentList("tasks."+name+".environments", task.Environments)...)
		if task.Runtime == "persistent" && task.Template != "" && len(task.Environments) > 0 {
			errs = append(errs, fmt.Sprintf("tasks.%s.environments is not allowed with template: %s (set environments on the template instead)", name, task.Template))
		}
	}
	return errs
}

func (c *Config) validateEnvironmentList(path string, names []string) []string {
	var errs []string
	seen := map[string]bool{}
	for _, n := range names {
		if _, ok := c.Environments[n]; !ok {
			errs = append(errs, fmt.Sprintf("%s: unknown environment %q", path, n))
		}
		if seen[n] {
			errs = append(errs, fmt.Sprintf("%s: duplicate environment %q", path, n))
		}
		seen[n] = true
	}
	return errs
}

// EnvironmentWarnings flags path-valued keys (CLAUDE_CONFIG_DIR, CODEX_HOME)
// that start with a literal "~": environment values are never expanded, so
// the harness would look for a directory literally named "~".
func (c *Config) EnvironmentWarnings() []string {
	var warnings []string
	names := make([]string, 0, len(c.Environments))
	for name := range c.Environments {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, key := range pathEnvKeys {
			if v := c.Environments[name][key]; strings.HasPrefix(v, "~") {
				warnings = append(warnings, fmt.Sprintf("environments.%s.%s = %q starts with \"~\", which is not expanded — use an absolute path", name, key, v))
			}
		}
	}
	return warnings
}

// TemplateEnv is the environment an agent, dispatch or consult of tmpl runs
// with when nothing is chosen at spawn time: the template's (or defaults')
// named environments beneath its literal `env:` map.
func (c *Config) TemplateEnv(tmpl TemplateConfig) (map[string]string, error) {
	return c.ResolveEnv(c.EnvironmentNames(nil, tmpl.Environments), tmpl.Env, nil)
}
