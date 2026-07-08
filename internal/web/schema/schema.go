// Package schema defines the single source of truth mapping Leo's config
// fields to web-UI form controls. Forms render and parse exclusively from
// this registry; registry_drift_test.go fails when a config field is
// neither registered nor explicitly excluded.
package schema

import (
	"reflect"
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
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
)

// Section identifies which config struct a field list applies to.
type Section string

const (
	SectionDefaults   Section = "defaults"
	SectionProcess    Section = "process"
	SectionTask       Section = "task"
	SectionTemplate   Section = "template"
	SectionSession    Section = "session"
	SectionProvider   Section = "provider"
	SectionClientHost Section = "client_host"
	SectionWeb        Section = "web"
	SectionClient     Section = "client"
)

// AllSections returns every section in stable order.
func AllSections() []Section {
	return []Section{
		SectionDefaults, SectionProcess, SectionTask, SectionTemplate,
		SectionSession, SectionProvider, SectionClientHost, SectionWeb,
		SectionClient,
	}
}

// StructFor returns the config struct type a section's fields live on.
func StructFor(s Section) reflect.Type {
	switch s {
	case SectionDefaults:
		return reflect.TypeOf(config.DefaultsConfig{})
	case SectionProcess:
		return reflect.TypeOf(config.ProcessConfig{})
	case SectionTask:
		return reflect.TypeOf(config.TaskConfig{})
	case SectionTemplate:
		return reflect.TypeOf(config.TemplateConfig{})
	case SectionSession:
		return reflect.TypeOf(config.SessionConfig{})
	case SectionProvider:
		return reflect.TypeOf(config.ProviderConfig{})
	case SectionClientHost:
		return reflect.TypeOf(config.HostConfig{})
	case SectionWeb:
		return reflect.TypeOf(config.WebConfig{})
	case SectionClient:
		return reflect.TypeOf(config.ClientConfig{})
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
}

// DeriveKind maps a Go type to its default form control.
func DeriveKind(t reflect.Type) Kind {
	switch {
	case t.Kind() == reflect.String:
		return KindText
	case t.Kind() == reflect.Int:
		return KindNumber
	case t.Kind() == reflect.Bool:
		return KindBool
	case t.Kind() == reflect.Ptr && t.Elem().Kind() == reflect.Bool:
		return KindTriBool
	case t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.String:
		return KindCSV
	case t.Kind() == reflect.Map && t.Elem().Kind() == reflect.String:
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
