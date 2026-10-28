package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"gopkg.in/yaml.v3"

	"github.com/blackpaw-studio/leo/internal/secretenv"
)

// EnvValue is one `env:` value: a literal string, or a secret reference that
// is resolved only when a process is spawned. A literal encodes exactly as a
// plain string did, so ref-free configs read and write byte-identically. A
// reference encodes as its mapping, e.g. {secret: keychain, service: X}.
type EnvValue struct {
	literal string
	ref     *secretenv.Ref
}

// Literal is an EnvValue holding s verbatim.
func Literal(s string) EnvValue { return EnvValue{literal: s} }

// Secret is an EnvValue holding a reference to r.
func Secret(r secretenv.Ref) EnvValue {
	r.Attributes = maps.Clone(r.Attributes)
	r.Argv = slices.Clone(r.Argv)
	r.Unknown = slices.Clone(r.Unknown)
	return EnvValue{ref: &r}
}

// Ref reports the value's secret reference, if it is one.
func (v EnvValue) Ref() (secretenv.Ref, bool) {
	if v.ref == nil {
		return secretenv.Ref{}, false
	}
	return *v.ref, true
}

// AsLiteral reports the value's literal text, if it is not a reference.
func (v EnvValue) AsLiteral() (string, bool) { return v.literal, v.ref == nil }

// String is a literal's text, or a reference's descriptor ("<secret …>"); it
// never resolves anything.
func (v EnvValue) String() string {
	if v.ref != nil {
		return "<secret " + v.ref.String() + ">"
	}
	return v.literal
}

const envValueShape = "env value must be a string or a secret reference mapping"

// UnmarshalYAML reads any scalar as a literal (null → "") and a mapping as a
// reference. Unknown mapping keys are kept in Ref.Unknown for Validate.
func (v *EnvValue) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.AliasNode && node.Alias != nil {
		node = node.Alias
	}
	switch node.Kind {
	case yaml.ScalarNode:
		var s string
		if err := node.Decode(&s); err != nil {
			return err
		}
		*v = Literal(s)
		return nil
	case yaml.MappingNode:
		ref, err := decodeRefYAML(node)
		if err != nil {
			return err
		}
		*v = EnvValue{ref: &ref}
		return nil
	}
	return fmt.Errorf("line %d: %s", node.Line, envValueShape)
}

func decodeRefYAML(node *yaml.Node) (secretenv.Ref, error) {
	known := map[string]bool{}
	var unknown []string
	for i := 0; i+1 < len(node.Content); i += 2 {
		k := node.Content[i].Value
		if known[k] || slices.Contains(unknown, k) {
			return secretenv.Ref{}, fmt.Errorf("line %d: secret reference key %q is repeated", node.Content[i].Line, k)
		}
		if refKeys[k] {
			known[k] = true
		} else {
			unknown = append(unknown, k)
		}
	}
	var ref secretenv.Ref
	if err := node.Decode(&ref); err != nil {
		return secretenv.Ref{}, err
	}
	ref.Unknown = unknown
	return ref, nil
}

// refKeys are the mapping keys secretenv.Ref decodes.
var refKeys = map[string]bool{
	"secret": true, "service": true, "account": true, "attributes": true,
	"name": true, "path": true, "argv": true,
}

// MarshalYAML writes a literal as a plain string and a reference as its
// mapping.
func (v EnvValue) MarshalYAML() (any, error) {
	if v.ref != nil {
		return *v.ref, nil
	}
	return v.literal, nil
}

// UnmarshalJSON reads a string, number, bool or null (→ "") as a literal and
// an object as a reference.
func (v *EnvValue) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	switch {
	case len(data) == 0:
		return fmt.Errorf("%s", envValueShape)
	case data[0] == '"':
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*v = Literal(s)
	case data[0] == '{':
		ref, err := decodeRefJSON(data)
		if err != nil {
			return err
		}
		*v = EnvValue{ref: &ref}
	case data[0] == '[':
		return fmt.Errorf("%s", envValueShape)
	case string(data) == "null":
		*v = Literal("")
	default: // number or bool: keep its text, as a YAML scalar would
		*v = Literal(string(data))
	}
	return nil
}

func decodeRefJSON(data []byte) (secretenv.Ref, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return secretenv.Ref{}, err
	}
	var ref secretenv.Ref
	if err := json.Unmarshal(data, &ref); err != nil {
		return secretenv.Ref{}, err
	}
	for _, k := range slices.Sorted(maps.Keys(raw)) {
		if !refKeys[k] {
			ref.Unknown = append(ref.Unknown, k)
		}
	}
	return ref, nil
}

// MarshalJSON writes a literal as a JSON string and a reference as an object.
func (v EnvValue) MarshalJSON() ([]byte, error) {
	if v.ref != nil {
		return json.Marshal(*v.ref)
	}
	return json.Marshal(v.literal)
}

// EnvMap is an `env:` map whose values may be secret references.
type EnvMap map[string]EnvValue

// LiteralEnv wraps a plain string map; nil in, nil out.
func LiteralEnv(m map[string]string) EnvMap {
	if m == nil {
		return nil
	}
	out := make(EnvMap, len(m))
	for k, v := range m {
		out[k] = Literal(v)
	}
	return out
}

// Keys lists the map's keys, sorted.
func (m EnvMap) Keys() []string { return slices.Sorted(maps.Keys(m)) }

// Literals returns the literal entries, skipping references; nil when there
// are none.
func (m EnvMap) Literals() map[string]string {
	var out map[string]string
	for k, v := range m {
		if s, ok := v.AsLiteral(); ok {
			if out == nil {
				out = map[string]string{}
			}
			out[k] = s
		}
	}
	return out
}

// Refs returns the reference entries; nil when there are none.
func (m EnvMap) Refs() map[string]secretenv.Ref {
	var out map[string]secretenv.Ref
	for k, v := range m {
		if r, ok := v.Ref(); ok {
			if out == nil {
				out = map[string]secretenv.Ref{}
			}
			out[k] = r
		}
	}
	return out
}

// validateEnvRefs checks the shape of every reference in env; each message
// reads "<scope>.KEY: secret: <reason>".
func validateEnvRefs(scope string, env EnvMap) []string {
	var errs []string
	for _, k := range env.Keys() {
		if r, ok := env[k].Ref(); ok {
			if err := r.Validate(); err != nil {
				errs = append(errs, fmt.Sprintf("%s.%s: secret: %v", scope, k, err))
			}
		}
	}
	return errs
}
