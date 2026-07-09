package schema

import (
	"testing"
)

// TestRegistryCoversConfig fails whenever a yaml-tagged field on a config
// struct has neither a registry entry nor an explicit exclusion. This is the
// drift gate: adding a config field without deciding its web treatment is a
// CI failure.
func TestRegistryCoversConfig(t *testing.T) {
	for _, section := range AllSections() {
		section := section
		t.Run(string(section), func(t *testing.T) {
			st := StructFor(section)
			fields := map[string]bool{}
			for _, f := range FieldsFor(section) {
				fields[f.Key] = true
			}
			excluded := map[string]bool{}
			for _, k := range Excluded[section] {
				excluded[k] = true
			}
			for i := 0; i < st.NumField(); i++ {
				tag := yamlKey(st.Field(i))
				if tag == "" || tag == "-" {
					continue
				}
				if fields[tag] && excluded[tag] {
					t.Errorf("field %q is both registered and excluded", tag)
				}
				if !fields[tag] && !excluded[tag] {
					t.Errorf("config field %q (%s.%s) has no registry entry and no exclusion — add one to internal/web/schema/registry.go", tag, st.Name(), st.Field(i).Name)
				}
			}
			// Reverse direction: every registry key must exist on the struct.
			for _, f := range FieldsFor(section) {
				if _, ok := fieldByYAMLKey(st, f.Key); !ok {
					t.Errorf("registry key %q has no matching struct field on %s", f.Key, st.Name())
				}
			}
		})
	}
}
