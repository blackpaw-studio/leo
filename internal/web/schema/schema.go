// Package schema defines the single source of truth mapping Leo's config
// fields to web-UI form controls. Forms render and parse exclusively from
// this registry; registry_drift_test.go fails when a config field is
// neither registered nor explicitly excluded.
package schema

import (
	"reflect"
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/leotools"
)

// Kind is the form-control type used to render and parse a field.
type Kind int

const (
	KindAuto     Kind = iota // registry default: derive from the Go type
	KindText                 // <input type="text">
	KindNumber               // <input type="number">
	KindBool                 // toggle; hidden-false + checkbox-true pair
	KindTriBool              // *bool: select inherit / on / off
	KindSelect               // select with a named options source
	KindCSV                  // []string as comma-separated input
	KindEnvMap               // map[string]string as KEY=VALUE textarea lines
	KindCron                 // string with live cron preview
	KindDuration             // string like "30m", "2h"
	KindTextarea             // long string
	KindDatalist             // free-text input with a <datalist> of suggestions

	// KindCount is one past the last kind, so tests can walk every kind (the
	// JSON schema must be able to describe each).
	KindCount
)

// Section identifies which config struct a field list applies to.
type Section string

const (
	SectionDefaults   Section = "defaults"
	SectionTask       Section = "task"
	SectionTemplate   Section = "template"
	SectionClientHost Section = "client_host"
	SectionWeb        Section = "web"
	SectionClient     Section = "client"
	// SectionPermissions is the nested templates.*.permissions struct. It is
	// rendered as an extra field group inside the template form rather than
	// as a page of its own — see handleTemplateEditPage.
	SectionPermissions Section = "permissions"
	// SectionPushDefaults and SectionPushTemplate are the nested push block of
	// defaults and of a template. Both edit config.PushConfig; the defaults
	// form shows on/off toggles (unset reads as on) while a template form
	// shows inherit/on/off selects. Rendered as an extra field group inside
	// those forms, like SectionPermissions.
	SectionPushDefaults Section = "push_defaults"
	SectionPushTemplate Section = "push_template"
	// SectionEnvironment is one entry of the top-level environments map. The
	// map's values are bare env maps, so the form edits them through
	// EnvironmentEntry.
	SectionEnvironment Section = "environment"
)

// EnvironmentEntry is the form-facing view of one named environment: its env
// map under the single `env` key. It exists only so the schema machinery has
// a struct to render and apply; config stores the bare map.
type EnvironmentEntry struct {
	Env config.EnvMap `yaml:"env"`
}

// AllSections returns every section in stable order.
func AllSections() []Section {
	return []Section{
		SectionDefaults, SectionTask, SectionTemplate,
		SectionClientHost, SectionWeb,
		SectionClient, SectionPermissions, SectionPushDefaults, SectionPushTemplate,
		SectionEnvironment,
	}
}

// StructFor returns the config struct type a section's fields live on.
func StructFor(s Section) reflect.Type {
	switch s {
	case SectionDefaults:
		return reflect.TypeOf(config.DefaultsConfig{})
	case SectionTask:
		return reflect.TypeOf(config.TaskConfig{})
	case SectionTemplate:
		return reflect.TypeOf(config.TemplateConfig{})
	case SectionClientHost:
		return reflect.TypeOf(config.HostConfig{})
	case SectionWeb:
		return reflect.TypeOf(config.WebConfig{})
	case SectionClient:
		return reflect.TypeOf(config.ClientConfig{})
	case SectionPermissions:
		return reflect.TypeOf(leotools.Permissions{})
	case SectionPushDefaults, SectionPushTemplate:
		return reflect.TypeOf(config.PushConfig{})
	case SectionEnvironment:
		return reflect.TypeOf(EnvironmentEntry{})
	}
	panic("schema: unknown section " + string(s))
}

// Field describes one config field's web-form treatment.
type Field struct {
	Key      string // yaml key; doubles as the form input name
	Label    string
	Help     string
	Kind     Kind   // KindAuto derives from the struct field's Go type
	Options  string // named options source for KindSelect (see options.go)
	Group    string // form section heading, e.g. "Schedule", "Runtime"
	Advanced bool   // rendered inside the collapsed advanced <details>
	Warning  string // rendered as an inline warning (e.g. lockout risk)

	// The attributes below feed the JSON config schema only. All are optional
	// and set only where config itself defines the rule: the daemon stays
	// authoritative and Validate remains the only enforcement.

	// Default is the effective value when unset (e.g. web.port 8370).
	Default any
	// Constraints are rules Validate or the add handlers already enforce.
	Constraints Constraints
	// Secret marks a write-only field: JSON reads report {"set": bool} and
	// never the value.
	Secret bool
}

// Constraints are the validation rules a client may pre-check. A nil bound or
// a zero Required/Pattern means "none".
type Constraints struct {
	Required bool   `json:"required,omitempty"`
	Min      *int   `json:"min,omitempty"`
	Max      *int   `json:"max,omitempty"`
	Pattern  string `json:"pattern,omitempty"`
}

func intp(n int) *int { return &n }

// DeriveKind maps a Go type to its default form control.
func DeriveKind(t reflect.Type) Kind {
	switch {
	case t.Kind() == reflect.String:
		return KindText
	case t.Kind() == reflect.Int:
		return KindNumber
	case t.Kind() == reflect.Bool:
		return KindBool
	case t.Kind() == reflect.Pointer && t.Elem().Kind() == reflect.Bool:
		return KindTriBool
	case t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.String:
		return KindCSV
	case t == envMapType, t.Kind() == reflect.Map && t.Elem().Kind() == reflect.String:
		return KindEnvMap
	}
	panic("schema: no derivable kind for type " + t.String())
}

// effectiveKind resolves a field's KindAuto against its already-located
// struct field. Shared by EffectiveKind and the values.go render/apply path
// so there is exactly one place that decides a field's effective kind.
func effectiveKind(f Field, sf reflect.StructField) Kind {
	if f.Kind != KindAuto {
		return f.Kind
	}
	return DeriveKind(sf.Type)
}

// EffectiveKind resolves a field's KindAuto against its struct type.
func EffectiveKind(section Section, f Field) Kind {
	sf, ok := fieldByYAMLKey(StructFor(section), f.Key)
	if !ok {
		panic("schema: field " + f.Key + " not on struct for section " + string(section))
	}
	return effectiveKind(f, sf)
}

// yamlKey extracts the yaml tag name for a struct field ("" if untagged).
func yamlKey(sf reflect.StructField) string {
	tag := sf.Tag.Get("yaml")
	if tag == "" {
		return ""
	}
	return strings.Split(tag, ",")[0]
}

// fieldByYAMLKey finds the struct field carrying the given yaml key.
func fieldByYAMLKey(st reflect.Type, key string) (reflect.StructField, bool) {
	for i := 0; i < st.NumField(); i++ {
		if yamlKey(st.Field(i)) == key {
			return st.Field(i), true
		}
	}
	return reflect.StructField{}, false
}
