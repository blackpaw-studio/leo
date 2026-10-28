package schema

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
)

// envMapType is the type of every env field the schema edits.
var envMapType = reflect.TypeOf(config.EnvMap(nil))

// secretEnvKey decides which env entries the JSON API treats as credentials:
// the key (case-insensitive) contains TOKEN, SECRET, PASSWORD, PASSWD or
// CREDENTIAL; contains API_KEY, PRIVATE_KEY or ACCESS_KEY (underscore or dash
// optional); or has AUTH as a whole segment (ANTHROPIC_AUTH_TOKEN,
// SERVICE_AUTH, but not AUTHOR). The value, not the key, is what is hidden.
var secretEnvKey = regexp.MustCompile(`(?i)TOKEN|SECRET|PASSWORD|PASSWD|CREDENTIAL|API[_-]?KEY|PRIVATE[_-]?KEY|ACCESS[_-]?KEY|(^|[_.-])AUTH([_.-]|$)`)

// IsSecretEnvKey reports whether the env entry named key is a credential whose
// value the JSON API never returns (it reads {"set": bool} instead).
func IsSecretEnvKey(key string) bool { return secretEnvKey.MatchString(key) }

// envJSON is an env map as the JSON API reads it: plain entries verbatim,
// credential entries as {"set": bool}, and secret references as
// {"set": true, "ref": {...}} whatever their key. A reference names a store
// item, never a value, and nothing here resolves one.
func envJSON(m config.EnvMap) map[string]any {
	out := make(map[string]any, len(m))
	for k, ev := range m {
		if ref, isRef := ev.Ref(); isRef {
			out[k] = map[string]any{"set": true, "ref": ref}
			continue
		}
		v, _ := ev.AsLiteral()
		if IsSecretEnvKey(k) {
			out[k] = map[string]any{"set": v != ""}
			continue
		}
		out[k] = v
	}
	return out
}

// KindOf reports a registered field's effective kind.
func KindOf(section Section, key string) (Kind, bool) {
	f, ok := fieldOf(section, key)
	if !ok {
		return KindAuto, false
	}
	return EffectiveKind(section, f), true
}

// ApplyEnvPatch merges a JSON patch into the env map field key of target
// (a pointer to the section's struct), entry by entry:
//
//	"value"        set or replace the entry with a literal
//	{"set": bool}  keep the entry's current value or reference (what a read
//	               returned for a credential); an error when there is none
//	{"ref": {...}} set the entry to a secret reference, e.g.
//	               {"secret": "keychain", "service": "X"}; "set" may ride
//	               along, so echoing a read back is a no-op
//	null           delete the entry
//	(omitted)      keep the entry
//
// A null patch clears the whole map. Credentials therefore round-trip: a
// client that never saw the value echoes the redacted shape back to keep it.
func ApplyEnvPatch(target any, section Section, key string, patch any) error {
	v := reflect.ValueOf(target).Elem()
	sf := mustFieldByYAMLKey(v.Type(), section, key)
	val := v.FieldByIndex(sf.Index)
	if patch == nil {
		val.Set(reflect.Zero(sf.Type))
		return nil
	}
	obj, ok := patch.(map[string]any)
	if !ok {
		return &FieldError{Key: key, Err: fmt.Errorf("%s must be an object of env entries", key)}
	}
	current := val.Interface().(config.EnvMap)
	next := make(config.EnvMap, len(current)+len(obj))
	for k, s := range current {
		next[k] = s
	}
	for _, name := range sortedKeys(obj) {
		if strings.TrimSpace(name) == "" || strings.ContainsAny(name, "=\n") {
			return &FieldError{Key: key, Err: fmt.Errorf("env key %q must be non-empty and contain no '=' or newline", name)}
		}
		switch entry := obj[name].(type) {
		case nil:
			delete(next, name)
		case string:
			if strings.Contains(entry, "\n") {
				return &FieldError{Key: key, Err: fmt.Errorf("value of %q must not contain a newline", name)}
			}
			next[name] = config.Literal(entry)
		case map[string]any:
			if raw, hasRef := entry["ref"]; hasRef {
				v, err := refEntry(name, entry, raw)
				if err != nil {
					return &FieldError{Key: key, Err: err}
				}
				next[name] = v
				continue
			}
			if _, isSet := entry["set"].(bool); !isSet || len(entry) != 1 {
				return &FieldError{Key: key, Err: errEnvEntryShape(name)}
			}
			if _, has := current[name]; !has {
				return &FieldError{Key: key, Err: fmt.Errorf("env %q has no current value to keep", name)}
			}
		default:
			return &FieldError{Key: key, Err: errEnvEntryShape(name)}
		}
	}
	if len(next) == 0 {
		val.Set(reflect.Zero(sf.Type))
		return nil
	}
	val.Set(reflect.ValueOf(next))
	return nil
}

func errEnvEntryShape(name string) error {
	return fmt.Errorf(`env %q takes a string, null, {"set": bool} to keep its value, or {"ref": {...}} for a secret reference`, name)
}

// refEntry decodes a {"ref": {...}} patch entry (optionally with "set") into a
// validated secret reference.
func refEntry(name string, entry map[string]any, raw any) (config.EnvValue, error) {
	for k := range entry {
		if k != "ref" && k != "set" {
			return config.EnvValue{}, errEnvEntryShape(name)
		}
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return config.EnvValue{}, fmt.Errorf("env %q: ref must be an object", name)
	}
	data, err := json.Marshal(obj)
	if err != nil {
		return config.EnvValue{}, fmt.Errorf("env %q: ref: %w", name, err)
	}
	var v config.EnvValue
	if err := json.Unmarshal(data, &v); err != nil {
		return config.EnvValue{}, fmt.Errorf("env %q: ref: %w", name, err)
	}
	ref, _ := v.Ref()
	if err := ref.Validate(); err != nil {
		return config.EnvValue{}, fmt.Errorf("env %q: secret: %w", name, err)
	}
	return v, nil
}
