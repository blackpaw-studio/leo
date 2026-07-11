package schema

import (
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
)

func TestOptionSources(t *testing.T) {
	cfg := &config.Config{
		Sessions:  map[string]config.SessionConfig{"daily": {}},
		Templates: map[string]config.TemplateConfig{"dev": {}},
	}
	src := OptionSources{Cfg: cfg, Agents: func() []string { return []string{"rocket"} }}

	if opts := src.For("models"); len(opts) < 3 {
		t.Errorf("models: want sonnet/opus/haiku at least, got %v", opts)
	}
	if opts := src.For("models"); opts[0].Value != "" {
		t.Errorf("models: want leading inherit option, got %v", opts)
	}
	if opts := src.For("sessions"); len(opts) != 2 || opts[1].Value != "daily" {
		t.Errorf("sessions: got %v", opts)
	}
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

// TestModelOptionsMatchConfigValidModels guards against modelOptions (hand-
// maintained in options.go) drifting from the model names the claude harness
// adapter actually accepts. Model policy lives with the adapter (config
// delegates to Harness.ValidateModel), so this test uses the adapter's
// exported claudeharness.ValidModels() accessor plus the exported
// Config.Validate() path, rather than reaching into internals.
func TestModelOptionsMatchConfigValidModels(t *testing.T) {
	optValues := make(map[string]bool)
	src := OptionSources{Cfg: &config.Config{}}
	for _, opt := range src.For("models") {
		if opt.Value == "" {
			continue // "inherit" placeholder, not a real model
		}
		optValues[opt.Value] = true
	}

	validModels := make(map[string]bool)
	for _, name := range claudeharness.ValidModels() {
		validModels[name] = true
	}

	// Forward: every model config accepts must be offered in the dropdown.
	for name := range validModels {
		if !optValues[name] {
			t.Errorf("config accepts model %q but modelOptions does not offer it — update internal/web/schema/options.go", name)
		}
	}

	// Reverse: every option value must actually be accepted by config
	// validation (double-checked via both the ValidModels() list and a real
	// Validate() call, since Validate() is the behavior that matters).
	for name := range optValues {
		if !validModels[name] {
			t.Errorf("modelOptions offers %q but claudeharness.ValidModels() does not include it", name)
		}
		cfg := &config.Config{Defaults: config.DefaultsConfig{Model: name}}
		if err := cfg.Validate(); err != nil {
			t.Errorf("modelOptions offers %q but config.Validate() rejects it: %v", name, err)
		}
	}
}
