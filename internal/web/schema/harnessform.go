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
	set := func(k string, v any) {
		if out == nil {
			out = map[string]any{}
		}
		out[k] = v
	}
	for _, f := range h.OptionsSchema() {
		raw := strings.TrimSpace(form.Get(HarnessOptionPrefix + f.Key))
		if raw == "" {
			continue
		}
		switch f.Type {
		case harness.OptionBool:
			b, err := strconv.ParseBool(raw)
			if err != nil {
				return nil, fmt.Errorf("harness option %s: %q is not true/false", f.Key, raw)
			}
			set(f.Key, b)
		case harness.OptionStringList:
			items := parseCSV(raw)
			vals := make([]any, 0, len(items))
			for _, it := range items {
				vals = append(vals, it)
			}
			set(f.Key, vals)
		case harness.OptionYAMLMap:
			var m map[string]any
			if err := yaml.Unmarshal([]byte(raw), &m); err != nil {
				return nil, fmt.Errorf("harness option %s: %v", f.Key, err)
			}
			if len(m) > 0 {
				set(f.Key, m)
			}
		default: // OptionString, OptionText, OptionEnum
			set(f.Key, raw)
		}
	}
	return out, nil
}
