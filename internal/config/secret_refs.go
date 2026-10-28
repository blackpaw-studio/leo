package config

import (
	"cmp"
	"slices"
)

// SecretReferences is the set `leo validate` checks: every reference a
// configured launch resolves, i.e. each template's and task's effective env
// (its named environments beneath its own env:, shadowed keys dropped, as
// at spawn), plus the references of each environment nothing in config
// names, which only a spawn-time list can apply. Paths are unexpanded. One
// entry per scope and key, sorted by scope then key. It does no I/O.
func (c *Config) SecretReferences() []ScopedRef {
	named := map[string]bool{}
	var launches []EnvLayer
	launch := func(names []string, scope string, env EnvMap) {
		var merged EnvLayer
		for _, n := range names {
			named[n] = true
			if e, ok := c.Environments[n]; ok {
				merged = MergeLayers(merged, rawLayer("environments."+n, e))
			}
		}
		launches = append(launches, MergeLayers(merged, rawLayer(scope, env)))
	}
	for name, tmpl := range c.Templates {
		launch(c.EnvironmentNames(nil, tmpl.Environments), "templates."+name+".env", tmpl.Env)
	}
	for name, task := range c.Tasks {
		launch(c.EnvironmentNames(nil, task.Environments), "tasks."+name+".env", task.Env)
	}
	for name, env := range c.Environments {
		if !named[name] {
			launches = append(launches, rawLayer("environments."+name, env))
		}
	}

	seen := map[[2]string]bool{}
	var out []ScopedRef
	for _, l := range launches {
		for _, sr := range l.Secrets {
			if id := [2]string{sr.Scope, sr.Key}; !seen[id] {
				seen[id] = true
				out = append(out, sr)
			}
		}
	}
	slices.SortFunc(out, func(a, b ScopedRef) int {
		return cmp.Or(cmp.Compare(a.Scope, b.Scope), cmp.Compare(a.Key, b.Key))
	})
	return out
}

// rawLayer is env as a layer with nothing expanded: enough to tell which
// keys a merge leaves as secrets.
func rawLayer(scope string, env EnvMap) EnvLayer {
	l := EnvLayer{Values: env.Literals()}
	for k, r := range env.Refs() {
		if l.Secrets == nil {
			l.Secrets = map[string]ScopedRef{}
		}
		l.Secrets[k] = ScopedRef{Scope: scope, Key: k, Ref: r}
	}
	return l
}

// CheckSecret resolves sr through x, as a launch would, and discards the
// value. A failure is a *SecretResolutionError naming sr, or the expansion
// error of a file reference's path.
func (x Expander) CheckSecret(sr ScopedRef) error {
	l, err := x.LiteralLayer(sr.Scope, EnvMap{sr.Key: Secret(sr.Ref)})
	if err != nil {
		return err
	}
	_, err = x.ResolveLayer(l)
	return err
}
