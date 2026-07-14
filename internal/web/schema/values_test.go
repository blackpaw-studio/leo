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
		Env:   map[string]string{"FOO": "bar", "BAZ": "qux"},
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
