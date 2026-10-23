package schema

import (
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/harness"
	_ "github.com/blackpaw-studio/leo/internal/harness/claude"
	_ "github.com/blackpaw-studio/leo/internal/harness/codex"
	_ "github.com/blackpaw-studio/leo/internal/harness/opencode"
)

func TestApplyErrorsCarryTheFieldKey(t *testing.T) {
	var task config.TaskConfig
	err := Apply(&task, SectionTask, url.Values{"retries": {"many"}})
	var fe *FieldError
	if !errors.As(err, &fe) || fe.Key != "retries" {
		t.Fatalf("Apply error = %#v, want *FieldError{Key: retries}", err)
	}
	if !strings.Contains(err.Error(), "retries") {
		t.Errorf("Error() text lost the key: %q", err.Error())
	}

	var env config.TemplateConfig
	err = Apply(&env, SectionTemplate, url.Values{"env": {"NOEQUALS"}})
	if !errors.As(err, &fe) || fe.Key != "env" {
		t.Fatalf("env error = %#v, want key env", err)
	}
}

func TestApplyHarnessOptionsErrorsCarryTheFieldKey(t *testing.T) {
	h, _ := harness.Get("claude")
	_, err := ApplyHarnessOptions(h, url.Values{"harness_options.bypass_permissions": {"maybe"}})
	var fe *FieldError
	if !errors.As(err, &fe) || fe.Key != "harness_options.bypass_permissions" {
		t.Fatalf("error = %#v, want key harness_options.bypass_permissions", err)
	}
}

func TestJSONTypeDescribesEveryKind(t *testing.T) {
	for k := KindText; k < KindCount; k++ {
		if _, ok := jsonType(Field{Key: "x"}, k, false); !ok {
			t.Errorf("Kind %d has no JSON type: the schema JSON cannot describe it", k)
		}
	}
	if _, ok := jsonType(Field{Key: "x"}, KindCount, false); ok {
		t.Error("an unknown kind must not be described")
	}
}

func TestJSONTypeDescribesEveryOptionType(t *testing.T) {
	for ot := harness.OptionString; ot < harness.OptionTypeCount; ot++ {
		if _, ok := optionJSONType(ot); !ok {
			t.Errorf("OptionType %d has no JSON type", ot)
		}
	}
}

func docByKey(docs []FieldDoc, key string) FieldDoc {
	for _, d := range docs {
		if d.Key == key {
			return d
		}
	}
	return FieldDoc{}
}

func TestFieldDocsTypesAndConstraints(t *testing.T) {
	src := OptionSources{Cfg: &config.Config{Templates: map[string]config.TemplateConfig{"coder": {}}}}
	docs := FieldDocs(SectionTask, src)

	if d := docByKey(docs, "runtime"); d.Type != "enum" || d.Default != "oneshot" || len(d.Options) != 2 {
		t.Errorf("runtime = %+v, want enum default oneshot with 2 options", d)
	}
	if d := docByKey(docs, "template"); d.Type != "reference" || d.References != "templates" || d.EmptyLabel != "none" {
		t.Errorf("template = %+v, want reference to templates", d)
	}
	if d := docByKey(docs, "schedule"); d.Type != "string" || d.Format != "cron" || !d.Constraints.Required {
		t.Errorf("schedule = %+v, want required cron string", d)
	}
	if d := docByKey(docs, "timeout"); d.Type != "duration" {
		t.Errorf("timeout type = %q, want duration", d.Type)
	}
	if d := docByKey(docs, "retries"); d.Type != "int" || d.Constraints.Min == nil || *d.Constraints.Min != 0 {
		t.Errorf("retries = %+v, want int min 0", d)
	}
	if d := docByKey(docs, "env"); d.Type != "map" {
		t.Errorf("env type = %q, want map", d.Type)
	}
	if d := docByKey(docs, "channels"); d.Type != "list" {
		t.Errorf("channels type = %q, want list", d.Type)
	}
	if d := docByKey(docs, "queue_max"); d.Default != float64(5) && d.Default != 5 {
		t.Errorf("queue_max default = %v, want 5", d.Default)
	}
	model := docByKey(docs, "model")
	if model.Type != "string" || model.HintsByHarness["claude"].Suggestions == nil || model.HintsByHarness["codex"].Placeholder == "" {
		t.Errorf("model = %+v, want per-harness hints", model)
	}
	if d := docByKey(FieldDocs(SectionWeb, src), "port"); d.Default != 8370 || d.Constraints.Max == nil || *d.Constraints.Max != 65535 {
		t.Errorf("web.port = %+v, want default 8370 max 65535", d)
	}
}

func TestFieldDocsBoolShapes(t *testing.T) {
	src := OptionSources{Cfg: &config.Config{}}
	if d := docByKey(FieldDocs(SectionTask, src), "enabled"); d.Type != "bool" || d.Nullable {
		t.Errorf("task.enabled = %+v, want plain bool", d)
	}
	if d := docByKey(FieldDocs(SectionPushDefaults, src), "finished"); d.Type != "bool" || d.Default != true {
		t.Errorf("push defaults finished = %+v, want bool default true", d)
	}
	if d := docByKey(FieldDocs(SectionPushTemplate, src), "finished"); d.Type != "bool" || !d.Nullable {
		t.Errorf("push template finished = %+v, want nullable bool", d)
	}
}

func TestHarnessDocsMapOptionTypes(t *testing.T) {
	src := OptionSources{Cfg: &config.Config{}, Agents: func() []string { return []string{"scout"} }}
	h, _ := harness.Get("claude")
	docs := HarnessDocs(h, src)
	if d := docByKey(docs, "bypass_permissions"); d.Type != "bool" || !d.Nullable {
		t.Errorf("bypass_permissions = %+v, want nullable bool", d)
	}
	if d := docByKey(docs, "permission_mode"); d.Type != "enum" || len(d.Options) == 0 {
		t.Errorf("permission_mode = %+v, want enum", d)
	}
	if d := docByKey(docs, "allowed_tools"); d.Type != "list" {
		t.Errorf("allowed_tools = %+v, want list", d)
	}
	if d := docByKey(docs, "append_system_prompt"); d.Type != "string" || !d.Multiline {
		t.Errorf("append_system_prompt = %+v, want multiline string", d)
	}
	if d := docByKey(docs, "agent"); d.Type != "enum" || len(d.Options) != 1 {
		t.Errorf("agent = %+v, want enum resolved from the agents source", d)
	}
	plain := HarnessDocs(h, OptionSources{Cfg: &config.Config{}})
	if d := docByKey(plain, "agent"); d.Type != "string" {
		t.Errorf("unresolvable source must fall back to string, got %+v", d)
	}
	opencode, _ := harness.Get("opencode")
	var yamlSeen bool
	for _, d := range HarnessDocs(opencode, src) {
		if d.Type == "yaml" {
			yamlSeen = true
		}
	}
	if !yamlSeen {
		t.Error("opencode has a YAML-map option; expected a yaml-typed doc")
	}
}

func TestValuesJSONTypedPerKind(t *testing.T) {
	tasks := config.TaskConfig{Schedule: "* * * * *", Retries: 0, Channels: nil, Env: nil}
	vals := ValuesJSON(&tasks, SectionTask, nil)
	if vals["retries"] != nil {
		t.Errorf("unset int = %v, want nil (null)", vals["retries"])
	}
	if got, ok := vals["channels"].([]string); !ok || got == nil || len(got) != 0 {
		t.Errorf("empty list = %#v, want empty non-nil []string", vals["channels"])
	}
	if got, ok := vals["env"].(map[string]string); !ok || got == nil {
		t.Errorf("empty map = %#v, want non-nil map", vals["env"])
	}
	if vals["enabled"] != false {
		t.Errorf("enabled = %v, want false", vals["enabled"])
	}

	tmpl := config.TemplateConfig{Push: config.PushConfig{}}
	tv := ValuesJSON(&tmpl.Push, SectionPushTemplate, nil)
	if tv["finished"] != nil {
		t.Errorf("unset tribool = %v, want nil", tv["finished"])
	}
	off := false
	tmpl.Push.Finished = &off
	if ValuesJSON(&tmpl.Push, SectionPushTemplate, nil)["finished"] != false {
		t.Error("explicit false tribool must read false")
	}
	if ValuesJSON(&config.PushConfig{}, SectionPushDefaults, nil)["finished"] != true {
		t.Error("unset default-on bool must read true")
	}
}

func TestInheritedJSONOnlyCarriesSetDefaults(t *testing.T) {
	defaults := &config.DefaultsConfig{Model: "opus"}
	got := InheritedJSON(&config.TaskConfig{}, SectionTask, defaults)
	if got["model"] != "opus" {
		t.Errorf("inherited model = %v, want opus", got["model"])
	}
	if _, ok := got["harness"]; ok {
		t.Error("an empty defaults key must not appear")
	}
	if got := InheritedJSON(&config.TaskConfig{Model: "haiku"}, SectionTask, defaults); got["model"] != nil {
		t.Errorf("a set value inherits nothing, got %v", got["model"])
	}
}

func overlay(t *testing.T, section Section, base url.Values, body string) (url.Values, []Problem) {
	t.Helper()
	var values map[string]any
	dec := json.NewDecoder(strings.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&values); err != nil {
		t.Fatal(err)
	}
	return OverlayForm(section, base, values)
}

func TestOverlayFormConvertsEachKind(t *testing.T) {
	base := url.Values{"schedule": {"* * * * *"}, "retries": {"3"}, "enabled": {"false"}}
	form, errs := overlay(t, SectionTask, base, `{"retries": null, "enabled": true, "channels": ["a","b"], "env": {"K":"v"}, "timeout": "5m"}`)
	if len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	want := map[string]string{"schedule": "* * * * *", "retries": "", "enabled": "true", "channels": "a, b", "env": "K=v\n", "timeout": "5m"}
	for k, v := range want {
		if form.Get(k) != v {
			t.Errorf("form[%s] = %q, want %q", k, form.Get(k), v)
		}
	}
}

func TestOverlayFormAbsentKeysKeepTheBase(t *testing.T) {
	base := url.Values{"schedule": {"0 * * * *"}, "enabled": {"true"}}
	form, _ := overlay(t, SectionTask, base, `{"timezone": "UTC"}`)
	if form.Get("schedule") != "0 * * * *" || form.Get("enabled") != "true" {
		t.Errorf("absent keys changed: %v", form)
	}
}

func TestOverlayFormRejectsBadValues(t *testing.T) {
	cases := []struct {
		name, body, field string
	}{
		{"wrong type", `{"retries": "three"}`, "retries"},
		{"fractional int", `{"retries": 1.5}`, "retries"},
		{"comma in list item", `{"channels": ["a,b"]}`, "channels"},
		{"non-string list item", `{"channels": [1]}`, "channels"},
		{"env key with equals", `{"env": {"A=B": "x"}}`, "env"},
		{"env value with newline", `{"env": {"A": "x\ny"}}`, "env"},
		{"bool as string", `{"enabled": "yes"}`, "enabled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, errs := overlay(t, SectionTask, url.Values{}, tc.body)
			if len(errs) != 1 || errs[0].Field != tc.field {
				t.Errorf("errors = %v, want one on %q", errs, tc.field)
			}
		})
	}
}

func TestOverlayFormRejectsUnknownKeys(t *testing.T) {
	_, errs := overlay(t, SectionTask, url.Values{}, `{"nonsense": 1}`)
	if len(errs) != 1 || errs[0].Field != "nonsense" || !errs[0].Unknown {
		t.Errorf("errors = %v, want unknown key nonsense", errs)
	}
}

func TestSecretFieldsAreWriteOnly(t *testing.T) {
	restore := MarkSecretForTest(SectionWeb, "bind")
	defer restore()

	web := config.WebConfig{Bind: "10.0.0.1"}
	vals := ValuesJSON(&web, SectionWeb, nil)
	got, ok := vals["bind"].(map[string]any)
	if !ok || got["set"] != true || len(got) != 1 {
		t.Errorf("secret value = %#v, want {set: true} and nothing else", vals["bind"])
	}
	if d := docByKey(FieldDocs(SectionWeb, OptionSources{Cfg: &config.Config{}}), "bind"); d.Type != "secret" {
		t.Errorf("secret type = %q, want secret", d.Type)
	}

	base := url.Values{"bind": {"10.0.0.1"}}
	form, errs := overlay(t, SectionWeb, base, `{"bind": {"replace": "10.0.0.2"}}`)
	if len(errs) != 0 || form.Get("bind") != "10.0.0.2" {
		t.Errorf("replace: form=%v errs=%v", form, errs)
	}
	form, _ = overlay(t, SectionWeb, base, `{"bind": {"clear": true}}`)
	if form.Get("bind") != "" {
		t.Errorf("clear left %q", form.Get("bind"))
	}
	if _, errs := overlay(t, SectionWeb, base, `{"bind": "10.0.0.9"}`); len(errs) != 1 {
		t.Errorf("a bare string must not be accepted for a secret, errs=%v", errs)
	}
}

func TestHarnessValuesJSONOnlySetKeysAndShapes(t *testing.T) {
	h, _ := harness.Get("claude")
	own := map[string]any{
		"bypass_permissions": true,
		"allowed_tools":      []any{"Bash", "Read"},
		"permission_mode":    "plan",
	}
	got := HarnessValuesJSON(h, own)
	want := map[string]any{"bypass_permissions": true, "allowed_tools": []string{"Bash", "Read"}, "permission_mode": "plan"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("harness values = %#v, want %#v", got, want)
	}
}

func TestOverlayHarnessForm(t *testing.T) {
	h, _ := harness.Get("claude")
	base := url.Values{"harness_options.permission_mode": {"plan"}}
	var values map[string]any
	dec := json.NewDecoder(strings.NewReader(`{"bypass_permissions": true, "allowed_tools": ["a","b"], "permission_mode": null}`))
	dec.UseNumber()
	_ = dec.Decode(&values)
	form, errs := OverlayHarnessForm(h, base, values)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	if form.Get("harness_options.bypass_permissions") != "true" || form.Get("harness_options.allowed_tools") != "a, b" || form.Get("harness_options.permission_mode") != "" {
		t.Errorf("form = %v", form)
	}
	_, errs = OverlayHarnessForm(h, base, map[string]any{"not_a_claude_option": "x"})
	if len(errs) != 1 || errs[0].Field != "harness_options.not_a_claude_option" {
		t.Errorf("errors = %v, want unknown harness option", errs)
	}
}
