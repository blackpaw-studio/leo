package schema

import (
	"net/url"
	"reflect"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/harness"
)

func TestIsSecretEnvKey(t *testing.T) {
	for key, want := range map[string]bool{
		"ANTHROPIC_AUTH_TOKEN":     true,
		"OP_SERVICE_ACCOUNT_TOKEN": true,
		"GITHUB_TOKEN":             true,
		"aws_secret_access_key":    true,
		"DB_PASSWORD":              true,
		"STRIPE_API_KEY":           true,
		"OPENAI_APIKEY":            true,
		"SSH_PRIVATE_KEY":          true,
		"AUTH":                     true,
		"SERVICE_AUTH":             true,
		"ANTHROPIC_BASE_URL":       false,
		"HOME":                     false,
		"AUTHOR":                   false,
		"PATH":                     false,
		"KEYBOARD_LAYOUT":          false,
	} {
		if got := IsSecretEnvKey(key); got != want {
			t.Errorf("IsSecretEnvKey(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestValuesJSONRedactsSecretEnvEntries(t *testing.T) {
	tmpl := &config.TemplateConfig{Env: map[string]string{
		"ANTHROPIC_AUTH_TOKEN": "sk-real-secret",
		"EMPTY_TOKEN":          "",
		"ANTHROPIC_BASE_URL":   "http://localhost:3325",
	}}
	got := ValuesJSON(tmpl, SectionTemplate, nil)["env"].(map[string]any)
	want := map[string]any{
		"ANTHROPIC_AUTH_TOKEN": map[string]any{"set": true},
		"EMPTY_TOKEN":          map[string]any{"set": false},
		"ANTHROPIC_BASE_URL":   "http://localhost:3325",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("env = %#v, want %#v", got, want)
	}
}

func envOf(m map[string]string) *config.TemplateConfig { return &config.TemplateConfig{Env: m} }

func TestApplyEnvPatchEntries(t *testing.T) {
	start := func() *config.TemplateConfig {
		return envOf(map[string]string{"A_TOKEN": "keep-me ", "PLAIN": "x", "GONE": "y"})
	}
	t.Run("keep replace clear and omit", func(t *testing.T) {
		tmpl := start()
		err := ApplyEnvPatch(tmpl, SectionTemplate, "env", map[string]any{
			"A_TOKEN": map[string]any{"set": true}, // keep, verbatim
			"PLAIN":   "z",                         // replace
			"GONE":    nil,                         // clear
			"NEW":     "n",                         // add
		})
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]string{"A_TOKEN": "keep-me ", "PLAIN": "z", "NEW": "n"}
		if !reflect.DeepEqual(tmpl.Env, want) {
			t.Errorf("env = %v, want %v", tmpl.Env, want)
		}
	})
	t.Run("omitted entries are kept", func(t *testing.T) {
		tmpl := start()
		if err := ApplyEnvPatch(tmpl, SectionTemplate, "env", map[string]any{"PLAIN": "z"}); err != nil {
			t.Fatal(err)
		}
		if tmpl.Env["A_TOKEN"] != "keep-me " || tmpl.Env["GONE"] != "y" {
			t.Errorf("env = %v", tmpl.Env)
		}
	})
	t.Run("null clears the whole map", func(t *testing.T) {
		tmpl := start()
		if err := ApplyEnvPatch(tmpl, SectionTemplate, "env", nil); err != nil || tmpl.Env != nil {
			t.Errorf("env = %v, err = %v", tmpl.Env, err)
		}
	})
	t.Run("keeping an entry that has no value is an error", func(t *testing.T) {
		tmpl := start()
		err := ApplyEnvPatch(tmpl, SectionTemplate, "env", map[string]any{"MISSING_TOKEN": map[string]any{"set": true}})
		if err == nil {
			t.Fatal("want an error")
		}
		if fe, ok := err.(*FieldError); !ok || fe.Key != "env" {
			t.Errorf("err = %#v, want a FieldError on env", err)
		}
	})
	for name, bad := range map[string]any{
		"not an object":    "A=1",
		"bad entry type":   map[string]any{"A": 1},
		"unknown object":   map[string]any{"A": map[string]any{"replace": "x"}},
		"empty key":        map[string]any{"": "x"},
		"equals in key":    map[string]any{"A=B": "x"},
		"newline in value": map[string]any{"A": "x\ny"},
	} {
		t.Run("rejects "+name, func(t *testing.T) {
			if err := ApplyEnvPatch(start(), SectionTemplate, "env", bad); err == nil {
				t.Error("want an error")
			}
		})
	}
}

func TestApplyKeysTouchesOnlyTheNamedFields(t *testing.T) {
	tmpl := &config.TemplateConfig{Model: "sonnet", Workspace: "  /ws with spaces  ", AddDirs: []string{"/a,b", "/c"}}
	if err := ApplyKeys(tmpl, SectionTemplate, url.Values{"model": {"opus"}}, []string{"model"}); err != nil {
		t.Fatal(err)
	}
	if tmpl.Model != "opus" || tmpl.Workspace != "  /ws with spaces  " || !reflect.DeepEqual(tmpl.AddDirs, []string{"/a,b", "/c"}) {
		t.Errorf("template = %+v", tmpl)
	}
}

func TestApplyHarnessOptionKeysMergesIntoTheCurrentOptions(t *testing.T) {
	h, err := harness.Get("claude")
	if err != nil {
		t.Fatal(err)
	}
	current := map[string]any{
		"allowed_tools":        []any{"Bash(a,b)"},
		"append_system_prompt": "  keep  \n",
		"permission_mode":      "plan",
	}
	form := url.Values{HarnessOptionPrefix + "permission_mode": {""}, HarnessOptionPrefix + "bypass_permissions": {"true"}}
	got, err := ApplyHarnessOptionKeys(h, current, form, []string{"permission_mode", "bypass_permissions"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"allowed_tools":        []any{"Bash(a,b)"},
		"append_system_prompt": "  keep  \n",
		"bypass_permissions":   true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("options = %#v, want %#v", got, want)
	}
	if current["permission_mode"] != "plan" {
		t.Error("ApplyHarnessOptionKeys mutated its input")
	}
}
