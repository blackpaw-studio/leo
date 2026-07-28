package schema

import (
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
)

func TestOptionSources(t *testing.T) {
	cfg := &config.Config{
		Templates: map[string]config.TemplateConfig{"dev": {}},
	}
	src := OptionSources{Cfg: cfg, Agents: func() []string { return []string{"rocket"} }}

	if opts := src.For("agents"); len(opts) != 2 || opts[1].Value != "rocket" {
		t.Errorf("agents: got %v", opts)
	}
	if opts := src.For("runtimes"); len(opts) != 2 {
		t.Errorf("runtimes: want oneshot+persistent, got %v", opts)
	}
	if opts := src.For("templates"); len(opts) != 2 || opts[1].Value != "dev" {
		t.Errorf("templates: got %v", opts)
	}
	if opts := src.For("permission_modes"); len(opts) == 0 || opts[0].Value != "" {
		t.Errorf("permission_modes: want leading inherit option, got %v", opts)
	}
}

func TestOptionSourcesUnknownPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected panic for unknown options source")
		}
	}()
	src := OptionSources{Cfg: &config.Config{}}
	src.For("bogus")
}

func TestOptionSourcesNilAgentsFunc(t *testing.T) {
	src := OptionSources{Cfg: &config.Config{}}
	opts := src.For("agents")
	if len(opts) != 1 || opts[0].Value != "" {
		t.Errorf("agents with nil func: want just the empty option, got %v", opts)
	}
}

func TestHarnessesOptionSource(t *testing.T) {
	src := OptionSources{Cfg: &config.Config{}}
	opts := src.For("harnesses")
	if len(opts) < 4 { // inherit + claude + codex + opencode
		t.Fatalf("harnesses source = %v, want inherit + at least 3 registered harnesses", opts)
	}
	if opts[0].Value != "" || opts[0].Label != "inherit" {
		t.Errorf("first option = %+v, want empty-value inherit", opts[0])
	}
	names := map[string]bool{}
	for _, o := range opts[1:] {
		names[o.Value] = true
	}
	for _, want := range []string{"claude", "codex", "opencode"} {
		if !names[want] {
			t.Errorf("harnesses source missing %q", want)
		}
	}
}

func TestTryForUnknownSourceReturnsNil(t *testing.T) {
	src := OptionSources{Cfg: &config.Config{}}
	if got := src.TryFor("no-such-source"); got != nil {
		t.Fatalf("TryFor(unknown) = %v, want nil", got)
	}
}

func TestHarnessFieldRegisteredOnConfigSections(t *testing.T) {
	for _, section := range []Section{SectionDefaults, SectionTask, SectionTemplate} {
		found := false
		for _, f := range FieldsFor(section) {
			if f.Key == "harness" {
				found = true
				if EffectiveKind(section, f) != KindSelect || f.Options != "harnesses" {
					t.Errorf("%s harness field = %+v, want KindSelect/harnesses", section, f)
				}
			}
		}
		if !found {
			t.Errorf("section %s has no harness field", section)
		}
	}
}

// TestModelSuggestionsMatchAdapter guards against ModelSuggestions drifting
// from the claude adapter's suggestion list. The list is a datalist hint, not
// an allowlist — config accepts any whitespace-free model name so a user can
// type one released after this build — so the assertion is one-directional:
// every suggestion must render, and each must survive Config.Validate().
func TestModelSuggestionsMatchAdapter(t *testing.T) {
	optValues := make(map[string]bool)
	for _, opt := range ModelSuggestions("claude") {
		optValues[opt.Value] = true
	}

	suggested := claudeharness.SuggestedModels()
	if len(suggested) == 0 {
		t.Fatal("claudeharness.SuggestedModels() is empty")
	}
	for _, name := range suggested {
		if !optValues[name] {
			t.Errorf("adapter suggests model %q but modelOptions does not offer it — update internal/web/schema/options.go", name)
		}
	}
	if len(optValues) != len(suggested) {
		t.Errorf("modelOptions offers %d values, adapter suggests %d — they must match", len(optValues), len(suggested))
	}

	// Every offered value must survive real config validation.
	for name := range optValues {
		cfg := &config.Config{Defaults: config.DefaultsConfig{Model: name}}
		if err := cfg.Validate(); err != nil {
			t.Errorf("modelOptions offers %q but config.Validate() rejects it: %v", name, err)
		}
	}
}

// TestModelSuggestionsDoNotConstrain pins the point of the change: a model
// name outside the suggestion list — a newer alias, a full model ID — must
// still validate, because the datalist only suggests.
func TestModelSuggestionsDoNotConstrain(t *testing.T) {
	for _, name := range []string{"fable", "claude-fable-5", "claude-opus-5"} {
		cfg := &config.Config{Defaults: config.DefaultsConfig{Model: name}}
		if err := cfg.Validate(); err != nil {
			t.Errorf("config.Validate() rejects model %q: %v", name, err)
		}
	}
}
