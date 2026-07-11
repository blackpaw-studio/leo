package schema

import (
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/harness/claude"
	"github.com/blackpaw-studio/leo/internal/harness/codex"
	"github.com/blackpaw-studio/leo/internal/harness/opencode"
)

func TestHarnessOptionValuesRendersOwnAndInherited(t *testing.T) {
	own := map[string]any{"permission_mode": "plan", "allowed_tools": []any{"Bash", "Read"}}
	inherited := map[string]any{"bypass_permissions": true, "permission_mode": "auto"}
	vals := HarnessOptionValues(claude.Claude{}, own, inherited, OptionSources{})
	byKey := map[string]HarnessFieldValue{}
	for _, v := range vals {
		byKey[v.Key] = v
	}
	if got := byKey["permission_mode"]; got.Value != "plan" || got.Inherited != "auto" {
		t.Errorf("permission_mode = %+v, want Value=plan Inherited=auto", got)
	}
	if got := byKey["allowed_tools"].Value; got != "Bash, Read" {
		t.Errorf("allowed_tools Value = %q, want CSV join", got)
	}
	if got := byKey["bypass_permissions"]; got.Value != "" || got.Inherited != "true" {
		t.Errorf("bypass_permissions = %+v, want unset with Inherited=true", got)
	}
	if got := byKey["permission_mode"].InputName; got != "harness_options.permission_mode" {
		t.Errorf("InputName = %q", got)
	}
}

func TestHarnessOptionValuesYAMLMapRoundTrips(t *testing.T) {
	own := map[string]any{"permission": map[string]any{"bash": map[string]any{"git *": "allow"}}}
	vals := HarnessOptionValues(opencode.Opencode{}, own, nil, OptionSources{})
	if len(vals) != 1 {
		t.Fatalf("opencode fields = %d, want 1", len(vals))
	}
	form := url.Values{"harness_options.permission": {vals[0].Value}}
	got, err := ApplyHarnessOptions(opencode.Opencode{}, form)
	if err != nil {
		t.Fatalf("apply rendered YAML: %v", err)
	}
	if !reflect.DeepEqual(got, own) {
		t.Errorf("round-trip = %#v, want %#v", got, own)
	}
}

func TestApplyHarnessOptionsShapesAndOmissions(t *testing.T) {
	form := url.Values{
		"harness_options.permission_mode":    {"acceptEdits"},
		"harness_options.bypass_permissions": {"true"},
		"harness_options.allowed_tools":      {"Bash, Read"},
		"harness_options.agent":              {""}, // empty → omitted
		"harness_options.remote_control":     {""}, // tri-state inherit → omitted
	}
	got, err := ApplyHarnessOptions(claude.Claude{}, form)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"permission_mode":    "acceptEdits",
		"bypass_permissions": true,
		"allowed_tools":      []any{"Bash", "Read"}, // []any, NOT []string — DecodeOptions asserts this
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
	if _, err := (claude.Claude{}).DecodeOptions(got); err != nil {
		t.Errorf("apply output rejected by DecodeOptions: %v", err)
	}
}

func TestApplyHarnessOptionsBadBoolIsKeyNamedError(t *testing.T) {
	form := url.Values{"harness_options.bypass_permissions": {"bogus"}}
	if _, err := ApplyHarnessOptions(claude.Claude{}, form); err == nil {
		t.Fatal("want error for non-bool input")
	} else if want := "bypass_permissions"; !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not name the key %q", err, want)
	}
}

func TestApplyHarnessOptionsEmptyFormReturnsNil(t *testing.T) {
	got, err := ApplyHarnessOptions(codex.Codex{}, url.Values{})
	if err != nil || got != nil {
		t.Fatalf("got %v, %v; want nil, nil", got, err)
	}
}

func TestApplyHarnessOptionsBadYAMLIsKeyNamedError(t *testing.T) {
	form := url.Values{"harness_options.permission": {": not yaml ["}}
	if _, err := ApplyHarnessOptions(opencode.Opencode{}, form); err == nil {
		t.Fatal("want error for bad YAML")
	} else if want := "permission"; !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not name the key %q", err, want)
	}
}

func TestModelSuggestionsAndPlaceholder(t *testing.T) {
	if got := ModelSuggestions("claude"); len(got) == 0 {
		t.Error("claude suggestions empty")
	}
	if got := ModelSuggestions("codex"); got != nil {
		t.Errorf("codex suggestions = %v, want nil", got)
	}
	if ModelPlaceholder("codex") == "" || ModelPlaceholder("opencode") == "" {
		t.Error("codex/opencode placeholders must hint the model format")
	}
	if ModelPlaceholder("claude") != "" {
		t.Error("claude placeholder must be empty (Inherited placeholder wins)")
	}
}
