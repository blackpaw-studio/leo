package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/web/schema"
)

// roundTripOverrides pin the sample for fields whose values config validates
// semantically (a cron expression, a model name, a reference to something that
// exists). Every other field is sampled from its JSON type alone, so a new
// registered field is round-tripped without touching this table.
var roundTripOverrides = map[string]any{
	"schedule":             "*/5 * * * *",
	"model":                "opus",
	"workspace":            "/tmp/roundtrip",
	"environments":         []any{"base"},
	"default_host":         "remote",
	"can_spawn":            []any{"reviewer"},
	"can_consult":          []any{"reviewer"},
	"deny_tools":           []any{"leo_spawn_agent"},
	"channels":             []any{"plugin:telegram@official"},
	"dev_channels":         []any{"plugin:telegram@official"},
	"ssh":                  "me@roundtrip.example",
	"template":             "reviewer",
	"bind":                 "127.0.0.2",
	"timezone":             "America/New_York",
	"allowed_hosts":        []any{"leo.example.com"},
	"public_urls":          []any{"https://leo.example.com"},
	"trusted_proxies":      []any{"10.0.0.0/8"},
	"idle_suspend_after":   "45m",
	"timeout":              "45m",
	"remote_control_name":  "roundtrip",
	"isolation":            "worktree",
	"runtime":              "persistent",
	"prompt_file":          "roundtrip.md",
	"permission_mode":      "plan",
	"queue_max":            float64(4),
	"allowed_tools":        []any{"Read"},
	"disallowed_tools":     []any{"Bash"},
	"append_system_prompt": "be brief",
}

// roundTripCompanions are values config only accepts together with another
// field (a task template needs the persistent runtime).
var roundTripCompanions = map[string]map[string]any{
	"tasks.template": {"runtime": "persistent"},
}

// sampleFor builds a non-default value for a field from its schema document.
func sampleFor(doc schema.FieldDoc) any {
	if v, ok := roundTripOverrides[doc.Key]; ok {
		return v
	}
	switch doc.Type {
	case "enum", "reference":
		for i := 0; i < len(doc.Options); i++ {
			if doc.Options[i].Value != "" {
				return doc.Options[i].Value
			}
		}
		return "roundtrip"
	case "int":
		n := 3
		if doc.Constraints.Min != nil && *doc.Constraints.Min >= n {
			n = *doc.Constraints.Min + 1
		}
		return float64(n)
	case "bool":
		if def, ok := doc.Default.(bool); ok {
			return !def
		}
		return true
	case "list":
		return []any{"rt-a", "rt-b"}
	case "map":
		return map[string]any{"RT_KEY": "rt-value"}
	case "duration":
		return "45m"
	default:
		return "rt-" + doc.Key
	}
}

// sampleOption builds a value for one harness option.
func sampleOption(f harness.OptionField) any {
	switch f.Type {
	case harness.OptionBool:
		return true
	case harness.OptionEnum:
		return f.EnumValues[0]
	case harness.OptionStringList:
		return []any{"rt-a", "rt-b"}
	case harness.OptionYAMLMap:
		return "bash: ask"
	case harness.OptionString, harness.OptionText:
		return "rt-" + f.Key
	case harness.OptionTypeCount:
		return nil
	}
	return nil
}

// normalize round-trips v through JSON so sample and read-back compare equal
// regardless of int/float and slice type.
func normalize(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// itemPath is the URL of the fixture item a resource's fields are written to.
func itemPath(res resourceDoc) string {
	names := map[string]string{"templates": "coder", "tasks": "heartbeat", "environments": "base", "hosts": "remote"}
	if res.Kind == "collection" {
		return "/config/" + res.Key + "/" + names[res.Key]
	}
	return "/config/" + res.Key
}

func (h *apiHarness) schemaDoc() schemaDocument {
	h.t.Helper()
	res := h.do(http.MethodGet, "/config/schema", "", nil)
	wantStatus(h.t, res, http.StatusOK)
	var doc schemaDocument
	raw, _ := json.Marshal(res.Body["data"])
	if err := json.Unmarshal(raw, &doc); err != nil {
		h.t.Fatal(err)
	}
	return doc
}

// readValues GETs an item and returns its values object.
func (h *apiHarness) readValues(path string) map[string]any {
	h.t.Helper()
	res := h.do(http.MethodGet, path, "", nil)
	wantStatus(h.t, res, http.StatusOK)
	return res.data()["values"].(map[string]any)
}

// TestConfigAPIRoundTrip writes a non-default sample of every registered
// field — flat fields and subsections of every resource — through the JSON
// API and reads it back, so a field the registry gains but the API cannot
// carry fails here, not in the app.
func TestConfigAPIRoundTrip(t *testing.T) {
	doc := newAPIHarness(t).schemaDoc()
	for _, res := range doc.Resources {
		if res.Kind == "bespoke" {
			continue
		}
		for _, f := range res.Fields {
			t.Run(res.Key+"/"+f.Key, func(t *testing.T) {
				roundTrip(t, res, "", f, schemaSecret(res, "", f.Key))
			})
		}
		for _, sub := range res.Subsections {
			for _, f := range sub.Fields {
				t.Run(res.Key+"/"+sub.Key+"."+f.Key, func(t *testing.T) {
					roundTrip(t, res, sub.Key, f, false)
				})
			}
		}
	}
}

// schemaSecret reports whether a flat field is write-only.
func schemaSecret(res resourceDoc, _ string, key string) bool {
	for _, f := range schema.FieldsFor(schema.Section(res.Section)) {
		if f.Key == key {
			return f.Secret
		}
	}
	return false
}

// roundTrip writes one field's sample into a fresh config (fields do not
// leak into one another) and reads it back.
func roundTrip(t *testing.T, res resourceDoc, sub string, f schema.FieldDoc, secret bool) {
	t.Helper()
	h := newAPIHarness(t)
	path := itemPath(res)
	sample := sampleFor(f)
	body := map[string]any{f.Key: sample}
	for k, v := range roundTripCompanions[res.Key+"."+f.Key] {
		body[k] = v
	}
	if sub != "" {
		body = map[string]any{sub: body}
	}
	if put := h.put(path, body); put.Status != http.StatusOK {
		t.Fatalf("PUT %s %v: status %d %s", path, body, put.Status, put.Raw)
	}
	values := h.readValues(path)
	got := values[f.Key]
	if sub != "" {
		got = values[sub].(map[string]any)[f.Key]
	}
	want := normalize(t, sample)
	if secret {
		want = map[string]any{"set": true}
	}
	if f.Type == "map" {
		// A map patch merges entry by entry: the sample's entries are present
		// (others the item already had are kept).
		gotMap, _ := got.(map[string]any)
		for k, v := range want.(map[string]any) {
			if !reflect.DeepEqual(gotMap[k], v) {
				t.Errorf("wrote %s=%#v, read back %#v", k, v, gotMap[k])
			}
		}
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("wrote %#v, read back %#v", want, got)
	}
}

// harnessModels are valid models per harness: validation is the harness's, so
// a switch must come with a model it accepts.
var harnessModels = map[string]string{"claude": "opus", "codex": "gpt-5", "opencode": "anthropic/claude-sonnet-5"}

// TestConfigAPIRoundTripHarnessOptions does the same for every option of
// every registered harness, on a template and on a task.
func TestConfigAPIRoundTripHarnessOptions(t *testing.T) {
	for _, name := range harness.Names() {
		h, err := harness.Get(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, target := range []string{"/config/templates/coder", "/config/tasks/heartbeat"} {
			for _, f := range h.OptionsSchema() {
				t.Run(fmt.Sprintf("%s/%s/%s", name, strings.TrimPrefix(target, "/config/"), f.Key), func(t *testing.T) {
					api := newAPIHarness(t)
					clear := api.put(target, map[string]any{"harness": name, "model": harnessModels[name], "harness_options": nil})
					if clear.Status != http.StatusOK {
						t.Fatalf("switch to %s: %d %s", name, clear.Status, clear.Raw)
					}
					sample := sampleOption(f)
					put := api.put(target, map[string]any{"harness_options": map[string]any{f.Key: sample}})
					if put.Status != http.StatusOK {
						t.Fatalf("PUT %s = %v: %d %s", f.Key, sample, put.Status, put.Raw)
					}
					opts, _ := api.readValues(target)["harness_options"].(map[string]any)
					if got, want := opts[f.Key], normalize(t, sample); !reflect.DeepEqual(got, want) {
						t.Errorf("%s.%s: wrote %#v, read back %#v", name, f.Key, want, got)
					}
				})
			}
		}
	}
}
