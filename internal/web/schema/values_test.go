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
	lazy := config.TaskConfig{
		Schedule:   "30 8,15 * * 1-5",
		PromptFile: "prompts/trade.md",
		Model:      "sonnet",
		Enabled:    true,
		Silent:     true,
		Timeout:    "45m",
		Retries:    2,
		Channels:   []string{"plugin:telegram@claude-plugins-official"},
		Runtime:    "persistent",
		Session:    "daily",
		Lazy:       true,
		QueueMax:   9,
	}
	form := renderToForm(Values(&lazy, SectionTask, nil))
	var got config.TaskConfig
	if err := Apply(&got, SectionTask, form); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !reflect.DeepEqual(lazy, got) {
		t.Errorf("round trip mismatch:\n want %+v\n got  %+v", lazy, got)
	}
}

// TestRoundTripProcessEnv covers the Env round trip previously bundled into
// TestRoundTripProcessTriBool. The tri-bool (bypass_permissions/
// remote_control) round-trip coverage was removed along with those fields'
// web-form registration (Task 7: claude flat fields moved to
// harness_options — see internal/web/schema/registry.go's Excluded map).
// ProcessConfig currently has no other *bool field to exercise KindTriBool
// through the registry; schema_test.go's TestDeriveKind still covers the
// *bool -> KindTriBool type-derivation logic directly. Re-add a
// registry-backed round trip once a harness_options form field uses
// KindTriBool.
func TestRoundTripProcessEnv(t *testing.T) {
	proc := config.ProcessConfig{
		Enabled: true,
		Model:   "opus",
		Env:     map[string]string{"FOO": "bar", "BAZ": "qux"},
	}
	form := renderToForm(Values(&proc, SectionProcess, nil))
	var got config.ProcessConfig
	if err := Apply(&got, SectionProcess, form); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !reflect.DeepEqual(proc.Env, got.Env) {
		t.Errorf("Env = %v, want %v", got.Env, proc.Env)
	}
}

func TestApplyClearsEmptied(t *testing.T) {
	proc := config.ProcessConfig{MCPConfig: "mcp.json", Model: "opus"}
	form := renderToForm(Values(&proc, SectionProcess, nil))
	form.Set("mcp_config", "") // user cleared the field
	if err := Apply(&proc, SectionProcess, form); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if proc.MCPConfig != "" {
		t.Errorf("cleared field survived: %q", proc.MCPConfig)
	}
	if proc.Model != "opus" {
		t.Errorf("untouched field lost: %q", proc.Model)
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
	var proc config.ProcessConfig
	form := renderToForm(Values(&proc, SectionProcess, nil))
	form.Set("env", "FOO=bar\nnot-an-assignment\n")
	if err := Apply(&proc, SectionProcess, form); err == nil {
		t.Fatal("want error for env line without '=', got nil")
	}
}

func TestPtrIntExplicitZeroSurvives(t *testing.T) {
	zero := 0
	proc := config.ProcessConfig{StaleResumeHours: &zero}
	form := renderToForm(Values(&proc, SectionProcess, nil))
	var got config.ProcessConfig
	if err := Apply(&got, SectionProcess, form); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got.StaleResumeHours == nil {
		t.Fatal("StaleResumeHours = nil, want non-nil pointer to 0 (explicit disable)")
	}
	if *got.StaleResumeHours != 0 {
		t.Errorf("StaleResumeHours = %d, want 0", *got.StaleResumeHours)
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
