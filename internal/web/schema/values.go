package schema

import (
	"fmt"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// FieldValue is a Field resolved against a concrete struct value, ready for
// template rendering.
type FieldValue struct {
	Field
	Kind      Kind   // effective (never KindAuto)
	Value     string // rendered current value ("" for unset)
	Checked   bool   // KindBool only
	Inherited string // effective default when Value is empty (placeholder)
}

// Values renders every registered field of section against target (a pointer
// to the section's struct). defaults, when non-nil, is a pointer to
// config.DefaultsConfig used to compute Inherited placeholders.
func Values(target any, section Section, defaults any) []FieldValue {
	v := reflect.ValueOf(target).Elem()
	var out []FieldValue
	for _, f := range FieldsFor(section) {
		sf, _ := fieldByYAMLKey(v.Type(), f.Key)
		fv := FieldValue{Field: f, Kind: effectiveKindFor(section, f, sf.Type)}
		val := v.FieldByIndex(sf.Index)
		switch fv.Kind {
		case KindBool:
			fv.Checked = val.Bool()
		case KindTriBool:
			if !val.IsNil() {
				fv.Value = strconv.FormatBool(val.Elem().Bool())
			}
		case KindNumber:
			fv.Value = renderNumber(val)
		case KindCSV:
			fv.Value = strings.Join(val.Interface().([]string), ", ")
		case KindEnvMap:
			fv.Value = renderEnvMap(val.Interface().(map[string]string))
		default: // text-ish kinds
			fv.Value = val.String()
		}
		if defaults != nil && fv.Value == "" && isTextKind(fv.Kind) {
			fv.Inherited = inheritedFrom(defaults, f.Key)
		}
		out = append(out, fv)
	}
	return out
}

// Apply parses form into target, writing every registered field of section.
// All registered fields always render, so absence in a submitted form only
// happens for KindBool (unchecked checkbox), which the hidden-false input
// covers. Returns the first parse error encountered.
func Apply(target any, section Section, form url.Values) error {
	v := reflect.ValueOf(target).Elem()
	for _, f := range FieldsFor(section) {
		sf, _ := fieldByYAMLKey(v.Type(), f.Key)
		val := v.FieldByIndex(sf.Index)
		raw := form.Get(f.Key)
		switch effectiveKindFor(section, f, sf.Type) {
		case KindBool:
			vals := form[f.Key]
			val.SetBool(len(vals) > 0 && vals[len(vals)-1] == "true")
		case KindTriBool:
			switch raw {
			case "true", "false":
				b := raw == "true"
				val.Set(reflect.ValueOf(&b))
			default:
				val.Set(reflect.Zero(sf.Type))
			}
		case KindNumber:
			if err := applyNumber(val, sf, f.Key, raw); err != nil {
				return err
			}
		case KindCSV:
			val.Set(reflect.ValueOf(parseCSV(raw)))
		case KindEnvMap:
			m, err := parseEnvLines(raw)
			if err != nil {
				return fmt.Errorf("%s: %w", f.Key, err)
			}
			val.Set(reflect.ValueOf(m))
		default:
			val.SetString(strings.TrimSpace(raw))
		}
	}
	return nil
}

func effectiveKindFor(section Section, f Field, t reflect.Type) Kind {
	if f.Kind != KindAuto {
		return f.Kind
	}
	return DeriveKind(t)
}

func isTextKind(k Kind) bool {
	switch k {
	case KindText, KindSelect, KindCron, KindDuration, KindTextarea:
		return true
	}
	return false
}

func renderNumber(val reflect.Value) string {
	if val.Kind() == reflect.Ptr {
		if val.IsNil() {
			return ""
		}
		val = val.Elem()
	}
	if val.Int() == 0 {
		return ""
	}
	return strconv.FormatInt(val.Int(), 10)
}

func applyNumber(val reflect.Value, sf reflect.StructField, key, raw string) error {
	raw = strings.TrimSpace(raw)
	isPtr := sf.Type.Kind() == reflect.Ptr
	if raw == "" {
		if isPtr {
			val.Set(reflect.Zero(sf.Type))
		} else {
			val.SetInt(0)
		}
		return nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fmt.Errorf("%s: %q is not a number", key, raw)
	}
	if isPtr {
		val.Set(reflect.ValueOf(&n))
	} else {
		val.SetInt(int64(n))
	}
	return nil
}

func parseCSV(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseEnvLines(raw string) (map[string]string, error) {
	var m map[string]string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("line %q is not KEY=VALUE", line)
		}
		if m == nil {
			m = map[string]string{}
		}
		m[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return m, nil
}

func renderEnvMap(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, m[k])
	}
	return b.String()
}

// inheritedFrom reads the same yaml key off the defaults struct, returning
// its string form when set.
func inheritedFrom(defaults any, key string) string {
	dv := reflect.ValueOf(defaults).Elem()
	sf, ok := fieldByYAMLKey(dv.Type(), key)
	if !ok || sf.Type.Kind() != reflect.String {
		return ""
	}
	return dv.FieldByIndex(sf.Index).String()
}
