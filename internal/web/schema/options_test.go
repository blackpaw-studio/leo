package schema

import (
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

func TestOptionSources(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{"zai": {BaseURL: "https://api.z.ai/v1"}},
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
	provs := src.For("providers")
	if len(provs) != 2 || provs[0].Value != "" || provs[1].Value != "zai" {
		t.Errorf("providers: want [inherit, zai], got %v", provs)
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
