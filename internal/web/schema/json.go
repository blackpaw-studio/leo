package schema

import (
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// This file is the JSON face of the registry: the field documents the app
// renders its forms from, the typed values a GET returns, and the overlay that
// turns a PUT body back into the form values schema.Apply already parses. The
// JSON API therefore has no parse path of its own.

// FieldDoc describes one field to a client: how to render it, what it
// defaults to, and which rules it can pre-check.
type FieldDoc struct {
	Key         string      `json:"key"`
	Label       string      `json:"label"`
	Help        string      `json:"help"`
	Warning     string      `json:"warning"`
	Group       string      `json:"group"`
	Advanced    bool        `json:"advanced"`
	Type        string      `json:"type"`
	Default     any         `json:"default"`
	Constraints Constraints `json:"constraints"`
	// Restart is what a change needs to take effect: "agents", "service" or
	// "none". The web layer stamps it from the resource.
	Restart string `json:"restart"`

	Multiline      bool                 `json:"multiline,omitempty"`
	Format         string               `json:"format,omitempty"`
	Nullable       bool                 `json:"nullable,omitempty"`
	Options        []OptionDoc          `json:"options,omitempty"`
	References     string               `json:"references,omitempty"`
	EmptyLabel     string               `json:"empty_label,omitempty"`
	HintsByHarness map[string]ModelHint `json:"hints_by_harness,omitempty"`
}

// OptionDoc is one choice of an enum field; Value "" means inherit / none.
type OptionDoc struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// ModelHint is a harness's help for the free-form model field.
type ModelHint struct {
	Suggestions []string `json:"suggestions,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
}

// Problem is a client-input problem found while overlaying a JSON body: the
// field key and a message. Unknown marks a key the schema does not define at
// all (a malformed request) as opposed to a bad value.
type Problem struct {
	Field   string `json:"field"`
	Message string `json:"message"`
	Unknown bool   `json:"-"`
}

// referenceSources are option sources that list another config collection's
// names; the schema calls such a field a reference rather than an enum.
var referenceSources = map[string]string{"templates": "templates"}

// typeInfo is the part of a FieldDoc a Kind determines.
type typeInfo struct {
	Type      string
	Multiline bool
	Format    string
	Nullable  bool
}

// jsonType maps a registry Kind to its JSON type. There is deliberately no
// default branch: a Kind the schema JSON cannot describe returns ok=false,
// which TestJSONTypeDescribesEveryKind turns into a failure.
func jsonType(f Field, k Kind, ptrBool bool) (typeInfo, bool) {
	switch k {
	case KindText, KindDatalist:
		return typeInfo{Type: "string"}, true
	case KindTextarea:
		return typeInfo{Type: "string", Multiline: true}, true
	case KindCron:
		return typeInfo{Type: "string", Format: "cron"}, true
	case KindDuration:
		return typeInfo{Type: "duration"}, true
	case KindNumber:
		return typeInfo{Type: "int"}, true
	case KindBool:
		return typeInfo{Type: "bool"}, true
	case KindTriBool:
		return typeInfo{Type: "bool", Nullable: true}, true
	case KindSelect:
		if _, ok := referenceSources[f.Options]; ok {
			return typeInfo{Type: "reference"}, true
		}
		return typeInfo{Type: "enum"}, true
	case KindCSV:
		return typeInfo{Type: "list"}, true
	case KindEnvMap:
		return typeInfo{Type: "map"}, true
	case KindAuto, KindCount:
		return typeInfo{}, false
	}
	return typeInfo{}, false
}

// optionJSONType is jsonType for a harness option, equally exhaustive.
func optionJSONType(t harness.OptionType) (typeInfo, bool) {
	switch t {
	case harness.OptionString:
		return typeInfo{Type: "string"}, true
	case harness.OptionText:
		return typeInfo{Type: "string", Multiline: true}, true
	case harness.OptionBool:
		return typeInfo{Type: "bool", Nullable: true}, true
	case harness.OptionEnum:
		return typeInfo{Type: "enum"}, true
	case harness.OptionStringList:
		return typeInfo{Type: "list"}, true
	case harness.OptionYAMLMap:
		return typeInfo{Type: "yaml"}, true
	case harness.OptionTypeCount:
		return typeInfo{}, false
	}
	return typeInfo{}, false
}

// isPointerBool reports whether the field is a *bool toggle (unset = on).
func isPointerBool(sf reflect.StructField) bool {
	return sf.Type.Kind() == reflect.Pointer && sf.Type.Elem().Kind() == reflect.Bool
}

// FieldDocs describes every registered field of section. src resolves option
// lists; its Cfg may be nil when no reference source is involved.
func FieldDocs(section Section, src OptionSources) []FieldDoc {
	st := StructFor(section)
	docs := make([]FieldDoc, 0, len(FieldsFor(section)))
	for _, f := range FieldsFor(section) {
		sf := mustFieldByYAMLKey(st, section, f.Key)
		kind := effectiveKind(f, sf)
		ptrBool := kind == KindBool && isPointerBool(sf)
		info, ok := jsonType(f, kind, ptrBool)
		if !ok {
			panic(fmt.Sprintf("schema: kind %d of %s.%s has no JSON type", kind, section, f.Key))
		}
		doc := FieldDoc{
			Key: f.Key, Label: f.Label, Help: f.Help, Warning: f.Warning, Group: f.Group, Advanced: f.Advanced,
			Type: info.Type, Multiline: info.Multiline, Format: info.Format, Nullable: info.Nullable,
			Default: f.Default, Constraints: f.Constraints, Restart: "none",
		}
		if ptrBool {
			doc.Default = true
		}
		switch {
		case f.Secret:
			doc.Type = "secret"
		case kind == KindSelect:
			fillSelect(&doc, f, src)
		case kind == KindDatalist:
			doc.HintsByHarness = modelHints()
		}
		docs = append(docs, doc)
	}
	return docs
}

// fillSelect resolves a select field's options or reference target.
func fillSelect(doc *FieldDoc, f Field, src OptionSources) {
	if target, ok := referenceSources[f.Options]; ok {
		doc.References = target
		doc.EmptyLabel = "none"
		return
	}
	for _, o := range src.For(f.Options) {
		doc.Options = append(doc.Options, OptionDoc(o))
	}
}

// modelHints lists, per registered harness, the suggestions and format hint
// for the free-form model field.
func modelHints() map[string]ModelHint {
	hints := map[string]ModelHint{}
	for _, name := range harness.Names() {
		var h ModelHint
		for _, o := range ModelSuggestions(name) {
			h.Suggestions = append(h.Suggestions, o.Value)
		}
		h.Placeholder = ModelPlaceholder(name)
		if h.Suggestions != nil || h.Placeholder != "" {
			hints[name] = h
		}
	}
	return hints
}

// HarnessDocs describes a harness's harness_options keys. Every option is
// nullable (unset inherits); a Source hint resolves to enum options when the
// source yields more than its empty entry, else the field stays a plain
// control.
func HarnessDocs(h harness.Harness, src OptionSources) []FieldDoc {
	var docs []FieldDoc
	for _, f := range h.OptionsSchema() {
		info, ok := optionJSONType(f.Type)
		if !ok {
			panic(fmt.Sprintf("schema: option type %d of %s.%s has no JSON type", f.Type, h.Name(), f.Key))
		}
		doc := FieldDoc{
			Key: f.Key, Label: f.Label, Help: f.Help, Group: "Harness options",
			Type: info.Type, Multiline: info.Multiline, Nullable: true,
			Restart: "none",
		}
		switch {
		case f.Type == harness.OptionEnum:
			for _, v := range f.EnumValues {
				doc.Options = append(doc.Options, OptionDoc{Value: v, Label: v})
			}
		case f.Source != "" && (f.Source != "templates" || src.Cfg != nil):
			if opts := src.TryFor(f.Source); len(opts) > 1 {
				doc.Type = "enum"
				for _, o := range opts[1:] {
					doc.Options = append(doc.Options, OptionDoc(o))
				}
			}
		}
		docs = append(docs, doc)
	}
	return docs
}

// ValuesJSON reads every registered field of section off target (a pointer to
// the section's struct) in its JSON type. Unset ints and tri-bools are nil; a
// secret reports only whether it is set.
func ValuesJSON(target any, section Section, defaults any) map[string]any {
	v := reflect.ValueOf(target).Elem()
	out := make(map[string]any, len(FieldsFor(section)))
	for _, f := range FieldsFor(section) {
		sf := mustFieldByYAMLKey(v.Type(), section, f.Key)
		val := v.FieldByIndex(sf.Index)
		if f.Secret {
			out[f.Key] = map[string]any{"set": val.String() != ""}
			continue
		}
		switch effectiveKind(f, sf) {
		case KindBool:
			if val.Kind() == reflect.Pointer {
				out[f.Key] = val.IsNil() || val.Elem().Bool()
			} else {
				out[f.Key] = val.Bool()
			}
		case KindTriBool:
			if val.IsNil() {
				out[f.Key] = nil
			} else {
				out[f.Key] = val.Elem().Bool()
			}
		case KindNumber:
			out[f.Key] = numberJSON(val)
		case KindCSV:
			out[f.Key] = append([]string{}, val.Interface().([]string)...)
		case KindEnvMap:
			m := map[string]string{}
			for k, s := range val.Interface().(map[string]string) {
				m[k] = s
			}
			out[f.Key] = m
		default:
			out[f.Key] = val.String()
		}
	}
	return out
}

// numberJSON mirrors renderNumber: a nil pointer and a plain 0 are unset.
func numberJSON(val reflect.Value) any {
	if val.Kind() == reflect.Pointer {
		if val.IsNil() {
			return nil
		}
		return int(val.Elem().Int())
	}
	if val.Int() == 0 {
		return nil
	}
	return int(val.Int())
}

// InheritedJSON returns the defaults-cascade placeholders for the fields left
// empty on target. Only non-empty ones appear.
func InheritedJSON(target any, section Section, defaults any) map[string]any {
	out := map[string]any{}
	if defaults == nil {
		return out
	}
	for _, fv := range Values(target, section, defaults) {
		if fv.Inherited != "" {
			out[fv.Key] = fv.Inherited
		}
	}
	return out
}

// HarnessValuesJSON reads the harness_options keys set on a scope, typed per
// h's option schema. Keys h does not define are not reported (the next save
// drops them, as the web form does).
func HarnessValuesJSON(h harness.Harness, own map[string]any) map[string]any {
	out := map[string]any{}
	for _, f := range h.OptionsSchema() {
		raw, ok := own[f.Key]
		if !ok || raw == nil {
			continue
		}
		switch f.Type {
		case harness.OptionBool:
			if b, ok := raw.(bool); ok {
				out[f.Key] = b
				continue
			}
			out[f.Key] = renderOptionValue(raw, f.Type)
		case harness.OptionStringList:
			if items, ok := raw.([]any); ok {
				list := make([]string, 0, len(items))
				for _, it := range items {
					list = append(list, fmt.Sprint(it))
				}
				out[f.Key] = list
				continue
			}
			out[f.Key] = renderOptionValue(raw, f.Type)
		default:
			out[f.Key] = renderOptionValue(raw, f.Type)
		}
	}
	return out
}

// HarnessInheritedJSON returns the defaults-cascade placeholders for h's
// options, rendered as strings; only non-empty ones appear.
func HarnessInheritedJSON(h harness.Harness, inherited map[string]any) map[string]any {
	out := map[string]any{}
	for _, f := range h.OptionsSchema() {
		if s := renderOptionValue(inherited[f.Key], f.Type); s != "" {
			out[f.Key] = s
		}
	}
	return out
}

// OverlayForm applies a JSON body's values over base — the form a browser
// would POST for the current state — and returns the result. A present key
// replaces the value, null clears it, an absent key keeps base's. The result
// feeds Apply unchanged, so validation is the web form's.
func OverlayForm(section Section, base url.Values, values map[string]any) (url.Values, []Problem) {
	form := cloneForm(base)
	var problems []Problem
	for _, key := range sortedKeys(values) {
		f, ok := fieldOf(section, key)
		if !ok {
			problems = append(problems, Problem{Field: key, Message: "unknown field", Unknown: true})
			continue
		}
		sf := mustFieldByYAMLKey(StructFor(section), section, key)
		kind := effectiveKind(f, sf)
		s, err := formString(f, kind, isPointerBool(sf), values[key])
		if err != nil {
			problems = append(problems, Problem{Field: key, Message: err.Error()})
			continue
		}
		form.Set(key, s)
	}
	return form, problems
}

// OverlayHarnessForm is OverlayForm for a scope's harness_options, keyed
// without the "harness_options." prefix. A key h does not define is a
// problem on harness_options.<key>.
func OverlayHarnessForm(h harness.Harness, base url.Values, values map[string]any) (url.Values, []Problem) {
	form := cloneForm(base)
	byKey := map[string]harness.OptionField{}
	for _, f := range h.OptionsSchema() {
		byKey[f.Key] = f
	}
	var problems []Problem
	for _, key := range sortedKeys(values) {
		field := HarnessOptionPrefix + key
		f, ok := byKey[key]
		if !ok {
			problems = append(problems, Problem{Field: field, Message: fmt.Sprintf("not an option of the %s harness", h.Name())})
			continue
		}
		s, err := optionString(f.Type, values[key])
		if err != nil {
			problems = append(problems, Problem{Field: field, Message: err.Error()})
			continue
		}
		form.Set(field, s)
	}
	return form, problems
}

// fieldOf looks a registered field up by key.
func fieldOf(section Section, key string) (Field, bool) {
	for _, f := range FieldsFor(section) {
		if f.Key == key {
			return f, true
		}
	}
	return Field{}, false
}

func cloneForm(base url.Values) url.Values {
	form := make(url.Values, len(base))
	for k, v := range base {
		form[k] = append([]string(nil), v...)
	}
	return form
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// formString converts a JSON value to the string schema.Apply parses for the
// field's kind. null clears: the empty string, or the unset state for a
// default-on toggle.
func formString(f Field, kind Kind, ptrBool bool, v any) (string, error) {
	if f.Secret {
		return secretString(v)
	}
	switch kind {
	case KindBool:
		if v == nil {
			if ptrBool {
				return "true", nil
			}
			return "false", nil
		}
		return boolString(v)
	case KindTriBool:
		if v == nil {
			return "", nil
		}
		return boolString(v)
	case KindNumber:
		if v == nil {
			return "", nil
		}
		return intString(v)
	case KindCSV:
		if v == nil {
			return "", nil
		}
		return listString(v)
	case KindEnvMap:
		if v == nil {
			return "", nil
		}
		return mapString(v)
	case KindText, KindSelect, KindCron, KindDuration, KindTextarea, KindDatalist:
		return plainString(v)
	case KindAuto, KindCount:
		return "", fmt.Errorf("unsupported field kind")
	}
	return "", fmt.Errorf("unsupported field kind")
}

// optionString is formString for a harness option.
func optionString(t harness.OptionType, v any) (string, error) {
	if v == nil {
		return "", nil
	}
	switch t {
	case harness.OptionBool:
		return boolString(v)
	case harness.OptionStringList:
		return listString(v)
	case harness.OptionString, harness.OptionText, harness.OptionEnum, harness.OptionYAMLMap:
		return plainString(v)
	case harness.OptionTypeCount:
		return "", fmt.Errorf("unsupported option type")
	}
	return "", fmt.Errorf("unsupported option type")
}

func plainString(v any) (string, error) {
	if v == nil {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("must be a string")
	}
	return s, nil
}

func boolString(v any) (string, error) {
	b, ok := v.(bool)
	if !ok {
		return "", fmt.Errorf("must be true or false")
	}
	return strconv.FormatBool(b), nil
}

func intString(v any) (string, error) {
	switch n := v.(type) {
	case json.Number:
		i, err := strconv.Atoi(n.String())
		if err != nil {
			return "", fmt.Errorf("must be a whole number")
		}
		return strconv.Itoa(i), nil
	case float64:
		if n != float64(int(n)) {
			return "", fmt.Errorf("must be a whole number")
		}
		return strconv.Itoa(int(n)), nil
	case int:
		return strconv.Itoa(n), nil
	}
	return "", fmt.Errorf("must be a whole number")
}

func listString(v any) (string, error) {
	items, ok := v.([]any)
	if !ok {
		if strs, isStrs := v.([]string); isStrs {
			items = make([]any, len(strs))
			for i, s := range strs {
				items[i] = s
			}
		} else {
			return "", fmt.Errorf("must be a list of strings")
		}
	}
	parts := make([]string, 0, len(items))
	for _, it := range items {
		s, ok := it.(string)
		if !ok {
			return "", fmt.Errorf("must be a list of strings")
		}
		if strings.ContainsAny(s, ",\n") {
			return "", fmt.Errorf("item %q must not contain a comma or newline", s)
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ", "), nil
}

func mapString(v any) (string, error) {
	var m map[string]any
	switch typed := v.(type) {
	case map[string]any:
		m = typed
	case map[string]string:
		m = make(map[string]any, len(typed))
		for k, s := range typed {
			m[k] = s
		}
	default:
		return "", fmt.Errorf("must be an object of strings")
	}
	var b strings.Builder
	for _, k := range sortedKeys(m) {
		s, ok := m[k].(string)
		if !ok {
			return "", fmt.Errorf("value of %q must be a string", k)
		}
		if strings.TrimSpace(k) == "" || strings.ContainsAny(k, "=\n") {
			return "", fmt.Errorf("key %q must be non-empty and contain no '=' or newline", k)
		}
		if strings.Contains(s, "\n") {
			return "", fmt.Errorf("value of %q must not contain a newline", k)
		}
		fmt.Fprintf(&b, "%s=%s\n", k, s)
	}
	return b.String(), nil
}

// secretString reads a secret's write action: {"replace": "..."} or
// {"clear": true}. A bare value is refused so a client cannot send a secret
// to a field it believes is plain.
func secretString(v any) (string, error) {
	m, ok := v.(map[string]any)
	if !ok || len(m) != 1 {
		return "", fmt.Errorf(`a secret takes {"replace": "..."} or {"clear": true}`)
	}
	if r, ok := m["replace"]; ok {
		s, ok := r.(string)
		if !ok || s == "" {
			return "", fmt.Errorf("replace must be a non-empty string")
		}
		return s, nil
	}
	if c, ok := m["clear"]; ok {
		if b, ok := c.(bool); ok && b {
			return "", nil
		}
	}
	return "", fmt.Errorf(`a secret takes {"replace": "..."} or {"clear": true}`)
}

// MarkSecretForTest flags an existing registry field as a secret until the
// returned restore runs. It is a test seam: no registered field is a secret
// today, so the redaction path needs one to exercise. Tests using it must not
// run in parallel.
func MarkSecretForTest(section Section, key string) (restore func()) {
	fields := registry[section]
	for i := range fields {
		if fields[i].Key == key {
			prev := fields[i].Secret
			fields[i].Secret = true
			return func() { fields[i].Secret = prev }
		}
	}
	panic("schema: no field " + key + " in section " + string(section))
}
