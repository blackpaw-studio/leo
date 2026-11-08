package schema

import (
	"net/url"
	"reflect"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

// renderToForm converts Values output back into url.Values the way a browser
// submitting the rendered form would.
func renderToForm(fvs []FieldValue) url.Values {
	form := url.Values{}
	for _, fv := range fvs {
		switch fv.Kind {
		case KindBool:
			form.Add(fv.Key, "false")
			if fv.Checked {
				form.Add(fv.Key, "true")
			}
		default:
			form.Set(fv.Key, fv.Value)
		}
	}
	return form
}

func TestRoundTripTask(t *testing.T) {
	persistent := config.TaskConfig{
		Schedule:   "30 8,15 * * 1-5",
		PromptFile: "prompts/trade.md",
		Model:      "sonnet",
		Enabled:    true,
		Silent:     true,
		Timeout:    "45m",
		Retries:    2,
		Channels:   []string{"plugin:telegram@claude-plugins-official"},
		Runtime:    "persistent",
		QueueMax:   9,
	}
	form := renderToForm(Values(&persistent, SectionTask, nil))
	var got config.TaskConfig
	if err := Apply(&got, SectionTask, form); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !reflect.DeepEqual(persistent, got) {
		t.Errorf("round trip mismatch:\n want %+v\n got  %+v", persistent, got)
	}
}

// TestRoundTripTemplateEnv covers the Env round trip previously bundled into
// TestRoundTripProcessTriBool (renamed/reworked when the Processes section
// was removed; TemplateConfig now carries the vehicle struct since it has
// the same Env field shape). The tri-bool (bypass_permissions/
// remote_control) round-trip coverage was removed along with those fields'
// web-form registration (Task 7: claude flat fields moved to
// harness_options — see internal/web/schema/registry.go's Excluded map).
// schema_test.go's TestDeriveKind still covers the *bool -> KindTriBool
// type-derivation logic directly. Re-add a registry-backed round trip once a
// harness_options form field uses KindTriBool.
func TestRoundTripTemplateEnv(t *testing.T) {
	tmpl := config.TemplateConfig{
		Model: "opus",
		Env:   config.LiteralEnv(map[string]string{"FOO": "bar", "BAZ": "qux"}),
	}
	form := renderToForm(Values(&tmpl, SectionTemplate, nil))
	var got config.TemplateConfig
	if err := Apply(&got, SectionTemplate, form); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !reflect.DeepEqual(tmpl.Env, got.Env) {
		t.Errorf("Env = %v, want %v", got.Env, tmpl.Env)
	}
}

func TestApplyClearsEmptied(t *testing.T) {
	tmpl := config.TemplateConfig{MCPConfig: "mcp.json", Model: "opus"}
	form := renderToForm(Values(&tmpl, SectionTemplate, nil))
	form.Set("mcp_config", "") // user cleared the field
	if err := Apply(&tmpl, SectionTemplate, form); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if tmpl.MCPConfig != "" {
		t.Errorf("cleared field survived: %q", tmpl.MCPConfig)
	}
	if tmpl.Model != "opus" {
		t.Errorf("untouched field lost: %q", tmpl.Model)
	}
}

func TestApplyRejectsBadNumber(t *testing.T) {
	var task config.TaskConfig
	form := renderToForm(Values(&task, SectionTask, nil))
	form.Set("max_turns", "lots")
	if err := Apply(&task, SectionTask, form); err == nil {
		t.Fatal("want error for non-numeric max_turns, got nil")
	}
}

func TestApplyRejectsBadEnvLine(t *testing.T) {
	var tmpl config.TemplateConfig
	form := renderToForm(Values(&tmpl, SectionTemplate, nil))
	form.Set("env", "FOO=bar\nnot-an-assignment\n")
	if err := Apply(&tmpl, SectionTemplate, form); err == nil {
		t.Fatal("want error for env line without '=', got nil")
	}
}

func TestInheritedPlaceholder(t *testing.T) {
	defaults := config.DefaultsConfig{Model: "sonnet"}
	var task config.TaskConfig
	for _, fv := range Values(&task, SectionTask, &defaults) {
		if fv.Key == "model" {
			if fv.Inherited != "sonnet" {
				t.Errorf("Inherited = %q, want %q", fv.Inherited, "sonnet")
			}
			return
		}
	}
	t.Fatal("model field not rendered")
}

// A *bool toggle (push defaults) reads unset as on, clears on checked, writes
// an explicit false on unchecked, and ignores a form that omits it.
func TestApplyDefaultOnPointerBool(t *testing.T) {
	var p config.PushConfig
	if v := Values(&p, SectionPushDefaults, nil); !v[0].Checked {
		t.Fatal("unset toggle should render checked")
	}

	if err := Apply(&p, SectionPushDefaults, url.Values{"finished": {"false"}}); err != nil {
		t.Fatal(err)
	}
	if p.Finished == nil || *p.Finished || p.NeedsInput != nil {
		t.Fatalf("after unchecking finished: %+v", p)
	}

	if err := Apply(&p, SectionPushDefaults, url.Values{}); err != nil {
		t.Fatal(err)
	}
	if p.Finished == nil || *p.Finished {
		t.Fatalf("omitted field must stay untouched: %+v", p)
	}

	if err := Apply(&p, SectionPushDefaults, url.Values{"finished": {"false", "true"}}); err != nil {
		t.Fatal(err)
	}
	if p.Finished != nil {
		t.Fatalf("checked toggle must clear the key: %+v", p)
	}
}

// A browser select submits its selected option, so an unset session only
// stays unset if the select offers a blank option and the field declares no
// effective default that would pre-select "resume". Saving through the form
// must not write session: resume, which would override defaults.session.
func TestUnsetSessionRoundTripsAsUnset(t *testing.T) {
	cases := []struct {
		section Section
		target  any
	}{
		{SectionTask, &config.TaskConfig{}},
		{SectionDefaults, &config.DefaultsConfig{}},
	}
	for _, tc := range cases {
		t.Run(string(tc.section), func(t *testing.T) {
			var field Field
			for _, f := range FieldsFor(tc.section) {
				if f.Key == "session" {
					field = f
				}
			}
			if field.Key == "" {
				t.Fatal("no session field")
			}
			if field.Default != nil {
				t.Errorf("session Default = %v, want nil so unset stays unset", field.Default)
			}
			blank := false
			for _, o := range (OptionSources{Cfg: &config.Config{}}).For(field.Options) {
				blank = blank || o.Value == ""
			}
			if !blank {
				t.Fatalf("options %q have no blank value; the browser would submit resume", field.Options)
			}
			form := renderToForm(Values(tc.target, tc.section, nil))
			if err := Apply(tc.target, tc.section, form); err != nil {
				t.Fatal(err)
			}
			var got string
			switch v := tc.target.(type) {
			case *config.TaskConfig:
				got = v.Session
			case *config.DefaultsConfig:
				got = v.Session
			}
			if got != "" {
				t.Errorf("session after round trip = %q, want unset", got)
			}
		})
	}
}
