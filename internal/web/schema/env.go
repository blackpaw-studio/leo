package schema

import (
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
// credential entries as {"set": bool}.
func envJSON(m config.EnvMap) map[string]any {
	out := make(map[string]any, len(m))
	for k, ev := range m {
		v, isLiteral := ev.AsLiteral()
		if !isLiteral {
			out[k] = map[string]any{"set": true}
			continue
		}
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
//	"value"        set or replace the entry
//	{"set": bool}  keep the entry's current value (what a read returned for a
//	               credential); an error when there is no current value
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
			if _, isSet := entry["set"].(bool); !isSet || len(entry) != 1 {
				return &FieldError{Key: key, Err: fmt.Errorf(`env %q takes a string, null, or {"set": bool} to keep its value`, name)}
			}
			if _, has := current[name]; !has {
				return &FieldError{Key: key, Err: fmt.Errorf("env %q has no current value to keep", name)}
			}
		default:
			return &FieldError{Key: key, Err: fmt.Errorf(`env %q takes a string, null, or {"set": bool} to keep its value`, name)}
		}
	}
	if len(next) == 0 {
		val.Set(reflect.Zero(sf.Type))
		return nil
	}
	val.Set(reflect.ValueOf(next))
	return nil
}
