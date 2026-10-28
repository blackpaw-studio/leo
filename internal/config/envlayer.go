package config

import (
	"errors"
	"maps"
	"slices"

	"github.com/blackpaw-studio/leo/internal/secretenv"
)

// SecretResolver resolves a secret reference to its value.
type SecretResolver interface {
	Resolve(ref secretenv.Ref) (string, error)
}

// ScopedRef is a secret reference located in config: the env entry Scope.Key
// (e.g. "environments.acct" and "OP_SERVICE_ACCOUNT_TOKEN").
type ScopedRef struct {
	Scope, Key string
	Ref        secretenv.Ref
}

// EnvLayer is one layer of a launch environment after variable expansion:
// literal values ready to use, and secret references still to resolve (a
// file reference's path already expanded). The two maps never share a key.
//
// Layers merge before anything resolves, so a reference a higher layer
// shadows never reaches its backend: a keychain reference a Linux host
// overrides locally must neither run there nor fail its launches.
type EnvLayer struct {
	Values  map[string]string
	Secrets map[string]ScopedRef
}

// ValuesLayer is a layer of plain values (harness env, spawn --env pairs).
func ValuesLayer(m map[string]string) EnvLayer { return EnvLayer{Values: m} }

// MergeLayers merges layers lowest precedence first: a later layer's key
// replaces an earlier one's, value or secret alike. It does no I/O and
// mutates nothing; a map is nil when it would be empty.
func MergeLayers(layers ...EnvLayer) EnvLayer {
	var out EnvLayer
	for _, l := range layers {
		for k, v := range l.Values {
			if out.Values == nil {
				out.Values = map[string]string{}
			}
			out.Values[k] = v
			delete(out.Secrets, k)
		}
		for k, r := range l.Secrets {
			if out.Secrets == nil {
				out.Secrets = map[string]ScopedRef{}
			}
			out.Secrets[k] = r
			delete(out.Values, k)
		}
	}
	if len(out.Values) == 0 {
		out.Values = nil
	}
	if len(out.Secrets) == 0 {
		out.Secrets = nil
	}
	return out
}

// LiteralLayer expands an `env:` map whose entries live under scope (e.g.
// "templates.coder.env"): literals have variables expanded, and references
// stay unresolved with a file reference's path expanded.
func (x Expander) LiteralLayer(scope string, env EnvMap) (EnvLayer, error) {
	var out EnvLayer
	for _, k := range env.Keys() {
		if ref, ok := env[k].Ref(); ok {
			if ref.Path != "" {
				p, err := x.Expand(ref.Path)
				if err != nil {
					return EnvLayer{}, scoped(err, scope, k)
				}
				ref.Path = p
			}
			if out.Secrets == nil {
				out.Secrets = map[string]ScopedRef{}
			}
			out.Secrets[k] = ScopedRef{Scope: scope, Key: k, Ref: ref}
			continue
		}
		lit, _ := env[k].AsLiteral()
		expanded, err := x.Expand(lit)
		if err != nil {
			return EnvLayer{}, scoped(err, scope, k)
		}
		if out.Values == nil {
			out.Values = map[string]string{}
		}
		out.Values[k] = expanded
	}
	return out, nil
}

// NamedLayer merges the named environments left to right, each expanded by x
// before merging (so an unset variable in a key a later environment overrides
// still fails). References stay unresolved.
func (c *Config) NamedLayer(names []string, x Expander) (EnvLayer, error) {
	var out EnvLayer
	for _, name := range names {
		env, err := c.LookupEnvironment(name)
		if err != nil {
			return EnvLayer{}, err
		}
		layer, err := x.LiteralLayer("environments."+name, env)
		if err != nil {
			return EnvLayer{}, err
		}
		out = MergeLayers(out, layer)
	}
	return out, nil
}

// ResolveLayer returns l's launch environment: its values plus each secret
// resolved through x. A resolved value is used verbatim, never expanded.
// The first failure, in key order, is a *SecretResolutionError. Returns nil
// when l is empty.
func (x Expander) ResolveLayer(l EnvLayer) (map[string]string, error) {
	if len(l.Values) == 0 && len(l.Secrets) == 0 {
		return nil, nil
	}
	out := maps.Clone(l.Values)
	if out == nil {
		out = map[string]string{}
	}
	for _, k := range slices.Sorted(maps.Keys(l.Secrets)) {
		sr := l.Secrets[k]
		v, err := x.secrets().Resolve(sr.Ref)
		if err != nil {
			return nil, &SecretResolutionError{Scope: sr.Scope, Key: sr.Key, Ref: sr.Ref, Reason: resolutionReason(err)}
		}
		out[k] = v
	}
	return out, nil
}

// resolutionReason keeps only a *secretenv.ResolutionError's reason, which
// never carries a value; any other resolver error is reported generically,
// since its text is not known to be value-free.
func resolutionReason(err error) string {
	var re *secretenv.ResolutionError
	if errors.As(err, &re) {
		return re.Reason
	}
	return "resolution failed"
}

func (x Expander) secrets() SecretResolver {
	if x.Secrets != nil {
		return x.Secrets
	}
	return secretenv.System{}
}
