// Package schematest asserts a harness adapter's OptionsSchema() and
// DecodeOptions agree. Imported by each adapter's tests — not production
// code — so the contract lives in exactly one place.
package schematest

import (
	"reflect"
	"sort"
	"testing"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// Run asserts: (1) OptionsSchema keys == optionKeys (the adapter's accepted
// set); (2) every EnumValues entry decodes cleanly and a bogus enum value
// fails; (3) one well-formed sample per field decodes cleanly. samples
// overrides the built-in per-type sample for keys whose valid values are
// constrained beyond their type (e.g. opencode "permission").
func Run(t *testing.T, h harness.Harness, optionKeys []string, samples map[string]any) {
	t.Helper()
	fields := h.OptionsSchema()

	got := make([]string, 0, len(fields))
	for _, f := range fields {
		got = append(got, f.Key)
	}
	sort.Strings(got)
	want := append([]string(nil), optionKeys...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("OptionsSchema keys = %v, want %v", got, want)
	}

	for _, f := range fields {
		if f.Type == harness.OptionEnum {
			if len(f.EnumValues) == 0 {
				t.Errorf("enum field %q has no EnumValues", f.Key)
				continue
			}
			for _, v := range f.EnumValues {
				if _, err := h.DecodeOptions(map[string]any{f.Key: v}); err != nil {
					t.Errorf("enum value %q for %q rejected by DecodeOptions: %v", v, f.Key, err)
				}
			}
			if _, err := h.DecodeOptions(map[string]any{f.Key: "bogus-enum-value"}); err == nil {
				t.Errorf("bogus enum value for %q accepted — schema and validator disagree", f.Key)
			}
		}
		if _, err := h.DecodeOptions(map[string]any{f.Key: sampleFor(f, samples)}); err != nil {
			t.Errorf("sample for %q (type %d) rejected by DecodeOptions: %v", f.Key, f.Type, err)
		}
	}
}

func sampleFor(f harness.OptionField, samples map[string]any) any {
	if s, ok := samples[f.Key]; ok {
		return s
	}
	switch f.Type {
	case harness.OptionBool:
		return true
	case harness.OptionEnum:
		return f.EnumValues[0]
	case harness.OptionStringList:
		return []any{"x"}
	case harness.OptionYAMLMap:
		return map[string]any{}
	default:
		return "x"
	}
}
