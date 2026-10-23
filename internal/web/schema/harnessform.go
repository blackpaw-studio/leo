package schema

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/blackpaw-studio/leo/internal/harness"
	"gopkg.in/yaml.v3"
)

// HarnessOptionPrefix namespaces harness-options form inputs so they can
// never collide with registry field names.
const HarnessOptionPrefix = "harness_options."

// HarnessFieldValue is one harness_options key resolved for rendering.
type HarnessFieldValue struct {
	harness.OptionField
	InputName string   // HarnessOptionPrefix + Key
	Value     string   // rendered current value ("" = unset)
	Inherited string   // defaults-cascade placeholder ("" = none)
	Opts      []Option // enum values or a resolved Source list; nil = plain control
}

// HarnessOptionValues renders h's options schema against a scope's own
// harness_options map. inherited supplies the defaults-cascade values shown
// as placeholders — pass nil when the cascade doesn't apply (the defaults
// form itself, sessions, or a scope whose harness differs from defaults;
// mirrors config's scopeHarnessOptions rules).
func HarnessOptionValues(h harness.Harness, own, inherited map[string]any, src OptionSources) []HarnessFieldValue {
	var out []HarnessFieldValue
	for _, f := range h.OptionsSchema() {
		v := HarnessFieldValue{
			OptionField: f,
			InputName:   HarnessOptionPrefix + f.Key,
			Value:       renderOptionValue(own[f.Key], f.Type),
			Inherited:   renderOptionValue(inherited[f.Key], f.Type),
		}
		switch {
		case f.Type == harness.OptionEnum:
			for _, ev := range f.EnumValues {
				v.Opts = append(v.Opts, Option{ev, ev})
			}
		case f.Source != "":
			if opts := src.TryFor(f.Source); len(opts) > 1 { // >1: source resolved beyond its empty entry
				v.Opts = opts[1:] // drop the source's own empty entry; the template renders inherit itself
			}
		}
		out = append(out, v)
	}
	return out
}

// renderOptionValue converts a stored harness_options value to its form
// representation. Tolerant of hand-edited config (never panics): unknown
// shapes fall back to fmt.Sprint.
func renderOptionValue(v any, t harness.OptionType) string {
	if v == nil {
		return ""
	}
	switch t {
	case harness.OptionBool:
		if b, ok := v.(bool); ok {
			return strconv.FormatBool(b)
		}
	case harness.OptionStringList:
		if items, ok := v.([]any); ok {
			parts := make([]string, 0, len(items))
			for _, it := range items {
				parts = append(parts, fmt.Sprint(it))
			}
			return strings.Join(parts, ", ")
		}
	case harness.OptionYAMLMap:
		b, err := yaml.Marshal(v)
		if err != nil {
			return fmt.Sprint(v)
		}
		return strings.TrimSpace(string(b))
	}
	return fmt.Sprint(v)
}

// ApplyHarnessOptions parses the harness_options.* inputs in form against
// h's schema. Empty inputs omit their key; an all-empty form returns nil so
// saved YAML never carries an empty harness_options: {}. Value shapes match
// the YAML decoder's (string, bool, []any, map[string]any) because the
// adapters' DecodeOptions type-assert exactly those.
func ApplyHarnessOptions(h harness.Harness, form url.Values) (map[string]any, error) {
	var out map[string]any
	for _, f := range h.OptionsSchema() {
		v, ok, err := parseOption(f, form.Get(HarnessOptionPrefix+f.Key))
		if err != nil {
			return nil, err
		}
		if ok {
			if out == nil {
				out = map[string]any{}
			}
			out[f.Key] = v
		}
	}
	return out, nil
}

// ApplyHarnessOptionKeys merges the named options from form into a copy of
// current: an empty input deletes its key, anything else sets it, and every
// option not named is left exactly as it was. current is not modified.
func ApplyHarnessOptionKeys(h harness.Harness, current map[string]any, form url.Values, keys []string) (map[string]any, error) {
	byKey := map[string]harness.OptionField{}
	for _, f := range h.OptionsSchema() {
		byKey[f.Key] = f
	}
	var out map[string]any
	if len(current) > 0 {
		out = make(map[string]any, len(current))
		for k, v := range current {
			out[k] = v
		}
	}
	for _, key := range keys {
		f, ok := byKey[key]
		if !ok {
			return nil, &FieldError{Key: HarnessOptionPrefix + key, Err: fmt.Errorf("not an option of the %s harness", h.Name())}
		}
		v, set, err := parseOption(f, form.Get(HarnessOptionPrefix+key))
		if err != nil {
			return nil, err
		}
		if !set {
			delete(out, key)
			continue
		}
		if out == nil {
			out = map[string]any{}
		}
		out[key] = v
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// parseOption parses one option's form input; ok is false for an empty input.
func parseOption(f harness.OptionField, rawInput string) (value any, ok bool, err error) {
	raw := strings.TrimSpace(rawInput)
	if raw == "" {
		return nil, false, nil
	}
	switch f.Type {
	case harness.OptionBool:
		b, perr := strconv.ParseBool(raw)
		if perr != nil {
			return nil, false, &FieldError{Key: HarnessOptionPrefix + f.Key, Err: fmt.Errorf("harness option %s: %q is not true/false", f.Key, raw)}
		}
		return b, true, nil
	case harness.OptionStringList:
		items := parseCSV(raw)
		vals := make([]any, 0, len(items))
		for _, it := range items {
			vals = append(vals, it)
		}
		return vals, true, nil
	case harness.OptionYAMLMap:
		var m map[string]any
		if uerr := yaml.Unmarshal([]byte(raw), &m); uerr != nil {
			return nil, false, &FieldError{Key: HarnessOptionPrefix + f.Key, Err: fmt.Errorf("harness option %s: %v", f.Key, uerr)}
		}
		if len(m) == 0 {
			return nil, false, nil
		}
		return m, true, nil
	default: // OptionString, OptionText, OptionEnum
		return raw, true, nil
	}
}
