package config

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

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
// *UnknownEnvironmentError. Values are raw (unexpanded): use it to check
// names, and ResolveNamed to build a launch environment. The result is a new
// map, nil when names is empty.
func (c *Config) MergeEnvironments(names []string) (EnvMap, error) {
	if len(names) == 0 {
		return nil, nil
	}
	merged := EnvMap{}
	for _, name := range names {
		env, err := c.LookupEnvironment(name)
		if err != nil {
			return nil, err
		}
		maps.Copy(merged, env)
	}
	return merged, nil
}

// ResolveNamed merges the named environments like MergeEnvironments, with
// every value expanded by x. Each environment is expanded before merging, so
// an unset variable in a key a later environment overrides still fails. An
// *UnsetVariableError names the environment, key and variable. Secret
// references are resolved after merging, so only the effective one of each
// key reaches its backend.
func (c *Config) ResolveNamed(names []string, x Expander) (map[string]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	layer, err := c.NamedLayer(names, x)
	if err != nil {
		return nil, err
	}
	return nonNil(x.ResolveLayer(layer))
}

// ExpandLiteral expands the values of a literal `env:` map (a template's or
// task's) with x and resolves its secret references. The result is a new
// map; nil in, nil out.
func ExpandLiteral(literal EnvMap, x Expander) (map[string]string, error) {
	if literal == nil {
		return nil, nil
	}
	layer, err := x.LiteralLayer("env", literal)
	if err != nil {
		return nil, err
	}
	return nonNil(x.ResolveLayer(layer))
}

func nonNil(env map[string]string, err error) (map[string]string, error) {
	if err == nil && env == nil {
		env = map[string]string{}
	}
	return env, err
}

// ResolveEnv layers the final environment, lowest to highest precedence:
// merged named environments, the literal `env:` map of a template or task,
// then per-spawn `--env K=V` pairs. The named and literal layers are expanded
// by x (see Expander); spawn pairs are passed through as given, since the
// caller's shell has already had its say. Secret references resolve once the
// layers merge, so a shadowed one never runs. Inputs are not mutated.
// Returns nil when every layer is empty.
func (c *Config) ResolveEnv(names []string, literal EnvMap, spawn map[string]string, x Expander) (map[string]string, error) {
	named, err := c.NamedLayer(names, x)
	if err != nil {
		return nil, err
	}
	lit, err := x.LiteralLayer("env", literal)
	if err != nil {
		return nil, err
	}
	return x.ResolveLayer(MergeLayers(named, lit, ValuesLayer(spawn)))
}

// validateEnvironments checks the top-level environments map and every
// `environments:` list that references it.
func (c *Config) validateEnvironments() []string {
	var errs []string
	for name, env := range c.Environments {
		errs = append(errs, validateEnvironment(name, env)...)
	}
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

// validateEnvironment checks one named environment's name, keys and secret
// references.
func validateEnvironment(name string, env EnvMap) []string {
	var errs []string
	if name == "" || strings.ContainsAny(name, ", \t\n") {
		errs = append(errs, fmt.Sprintf("environments name %q must be non-empty and contain no commas or whitespace", name))
	}
	for k := range env {
		if !envKeyPattern.MatchString(k) {
			errs = append(errs, fmt.Sprintf("environments.%s key %q is not a valid environment variable name", name, k))
		}
	}
	return append(errs, validateEnvRefs("environments."+name, env)...)
}

func (c *Config) validateEnvironmentList(path string, names []string) []string {
	var errs []string
	seen := map[string]bool{}
	for _, n := range names {
		if _, ok := c.Environments[n]; !ok && !c.isInvalidLead(KindEnvironment, n) {
			errs = append(errs, fmt.Sprintf("%s: unknown environment %q", path, n))
		}
		if seen[n] {
			errs = append(errs, fmt.Sprintf("%s: duplicate environment %q", path, n))
		}
		seen[n] = true
	}
	return errs
}

// TaskEnv is the environment a task's process runs with: the task's (or
// defaults') named environments beneath its literal `env:` map.
func (c *Config) TaskEnv(t TaskConfig, x Expander) (map[string]string, error) {
	return c.ResolveEnv(c.EnvironmentNames(nil, t.Environments), t.Env, nil, x)
}

// ValidateEnvironmentNames checks an ordered list of environment names a
// caller supplied (a spawn flag, an MCP argument): each must be defined and
// none may repeat. Config files get the same checks from Validate.
func (c *Config) ValidateEnvironmentNames(names []string) error {
	if errs := c.validateEnvironmentList("environments", names); len(errs) > 0 {
		return &InvalidEnvironmentsError{Problems: errs}
	}
	return nil
}

// InvalidEnvironmentsError reports a caller-supplied environment list that is
// malformed — a repeated or undefined name. It is the caller's mistake, not a
// server fault, so API layers map it to 400.
type InvalidEnvironmentsError struct{ Problems []string }

func (e *InvalidEnvironmentsError) Error() string { return strings.Join(e.Problems, "; ") }

// EnvironmentWarnings flags every environment value (named environments and
// the literal `env:` of templates and tasks) that references a variable x does
// not have, one warning per key. It is a warning, not an error: `leo validate`
// sees only its own shell, and the daemon that launches agents may have a
// different environment. A launch with the variable unset still fails.
func (c *Config) EnvironmentWarnings(x Expander) []string {
	var warnings []string
	check := func(scope string, env EnvMap) {
		literals := env.Literals()
		for _, k := range slices.Sorted(maps.Keys(literals)) {
			if _, err := x.Expand(literals[k]); err != nil {
				warnings = append(warnings, scoped(err, scope, k).Error()+" in this process (the launching daemon's environment may differ)")
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(c.Environments)) {
		check("environments."+name, c.Environments[name])
	}
	for _, name := range slices.Sorted(maps.Keys(c.Templates)) {
		check("templates."+name+".env", c.Templates[name].Env)
	}
	for _, name := range slices.Sorted(maps.Keys(c.Tasks)) {
		check("tasks."+name+".env", c.Tasks[name].Env)
	}
	return warnings
}

// TemplateEnv is the environment an agent, dispatch or consult of tmpl runs
// with when nothing is chosen at spawn time: the template's (or defaults')
// named environments beneath its literal `env:` map.
func (c *Config) TemplateEnv(tmpl TemplateConfig, x Expander) (map[string]string, error) {
	return c.ResolveEnv(c.EnvironmentNames(nil, tmpl.Environments), tmpl.Env, nil, x)
}

// DispatchEnvironmentNames is the effective ordered environment list of a
// dispatch of tmpl: the template's (or defaults') list, then the inherited
// list (the caller's, or an explicit per-dispatch override) on top. A name in
// both keeps its later, inherited position, so the caller's layer still wins.
func (c *Config) DispatchEnvironmentNames(tmpl TemplateConfig, inherited []string) []string {
	names := append(c.EnvironmentNames(nil, tmpl.Environments), inherited...)
	out := make([]string, 0, len(names))
	for i, n := range names {
		if !slices.Contains(names[i+1:], n) {
			out = append(out, n)
		}
	}
	return out
}

// DispatchEnv is the environment a dispatch of tmpl runs with: TemplateEnv
// (the template's named environments beneath its literal `env:`), with the
// inherited named environments layered on top, so the caller wins any key
// both set. With no inherited names it equals TemplateEnv.
func (c *Config) DispatchEnv(tmpl TemplateConfig, inherited []string, x Expander) (map[string]string, error) {
	named, err := c.NamedLayer(c.EnvironmentNames(nil, tmpl.Environments), x)
	if err != nil {
		return nil, err
	}
	lit, err := x.LiteralLayer("env", tmpl.Env)
	if err != nil {
		return nil, err
	}
	top, err := c.NamedLayer(inherited, x)
	if err != nil {
		return nil, err
	}
	return x.ResolveLayer(MergeLayers(named, lit, top))
}

// EnvironmentKeys lists, sorted, every variable a named environment or a
// template's literal `env:` can set: the keys a leo agent's tmux session may
// carry that a dispatch child must not pick up implicitly.
func (c *Config) EnvironmentKeys() []string {
	keys := map[string]bool{}
	for _, env := range c.Environments {
		for k := range env {
			keys[k] = true
		}
	}
	for _, tmpl := range c.Templates {
		for k := range tmpl.Env {
			keys[k] = true
		}
	}
	return slices.Sorted(maps.Keys(keys))
}
