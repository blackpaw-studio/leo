package config

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// stubHarness is a minimal harness.Harness implementation for exercising
// config validation paths that the real claude adapter can't (e.g.
// SupportsChannels() == false). Registered once, process-global — the
// harness registry panics on duplicate registration, so every test that
// needs it calls registerStubNoChannels() (idempotent via sync.Once).
type stubHarness struct {
	name             string
	supportsChannels bool
}

func (s stubHarness) Name() string                              { return s.name }
func (s stubHarness) Binary() string                            { return s.name }
func (s stubHarness) Args(harness.LaunchSpec) ([]string, error) { return nil, nil }
func (s stubHarness) SessionArgs(harness.SessionState) []string { return nil }
func (s stubHarness) ValidateModel(model string) error {
	if model == "" || model == "stub-model" {
		return nil
	}
	return fmt.Errorf("%q is not valid (use stub-model)", model)
}

// DecodeOptions accepts only the "known" key; anything else is an error, so
// tests can exercise the unknown-key and bad-type paths without depending on
// the claude adapter's specific option set.
func (s stubHarness) DecodeOptions(raw map[string]any) (any, error) {
	for k, v := range raw {
		if k != "known" {
			return nil, fmt.Errorf("unknown option %q", k)
		}
		if _, ok := v.(string); !ok {
			return nil, fmt.Errorf("option %q must be a string, got %T", k, v)
		}
	}
	return raw, nil
}

func (s stubHarness) OptionsSchema() []harness.OptionField { return nil }

func (s stubHarness) SupportsChannels() bool { return s.supportsChannels }

func (s stubHarness) ParseEvents(io.Reader) (harness.Result, error)     { return harness.Result{}, nil }
func (s stubHarness) Env(harness.LaunchSpec) (map[string]string, error) { return nil, nil }
func (s stubHarness) SupportsKind(harness.Kind) bool                    { return true }
func (s stubHarness) Driver() harness.SessionDriver                     { return nil }

const stubNoChannelsName = "stubnochannels"

var registerStubNoChannelsOnce sync.Once

// registerStubNoChannels registers a harness named stubNoChannelsName whose
// SupportsChannels() is false. Safe to call from multiple tests.
func registerStubNoChannels() {
	registerStubNoChannelsOnce.Do(func() {
		harness.Register(stubHarness{name: stubNoChannelsName, supportsChannels: false})
	})
}

func TestDefaultsHarness(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{"unset returns claude default", Config{}, "claude"},
		{"set value wins", Config{Defaults: DefaultsConfig{Harness: "codex"}}, "codex"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.DefaultsHarness(); got != tt.want {
				t.Errorf("DefaultsHarness() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestScopeHarnessCascade(t *testing.T) {
	t.Run("task", func(t *testing.T) {
		cfg := &Config{Defaults: DefaultsConfig{Harness: "opencode"}}
		if got := cfg.TaskHarness(TaskConfig{Harness: "codex"}); got != "codex" {
			t.Errorf("TaskHarness() scope = %q, want codex", got)
		}
		if got := cfg.TaskHarness(TaskConfig{}); got != "opencode" {
			t.Errorf("TaskHarness() defaults fallback = %q, want opencode", got)
		}
		if got := (&Config{}).TaskHarness(TaskConfig{}); got != "claude" {
			t.Errorf("TaskHarness() claude fallback = %q, want claude", got)
		}
	})

	t.Run("template", func(t *testing.T) {
		cfg := &Config{Defaults: DefaultsConfig{Harness: "opencode"}}
		if got := cfg.TemplateHarness(TemplateConfig{Harness: "codex"}); got != "codex" {
			t.Errorf("TemplateHarness() scope = %q, want codex", got)
		}
		if got := cfg.TemplateHarness(TemplateConfig{}); got != "opencode" {
			t.Errorf("TemplateHarness() defaults fallback = %q, want opencode", got)
		}
		if got := (&Config{}).TemplateHarness(TemplateConfig{}); got != "claude" {
			t.Errorf("TemplateHarness() claude fallback = %q, want claude", got)
		}
	})

}

func TestUsesHarness(t *testing.T) {
	tests := []struct {
		name  string
		cfg   Config
		query string
		want  bool
	}{
		{"empty config, claude default matches", Config{}, "claude", true},
		{"empty config, other harness does not match", Config{}, "opencode", false},
		{
			"defaults harness matches",
			Config{Defaults: DefaultsConfig{Harness: "opencode"}},
			"opencode",
			true,
		},
		{
			"template explicit harness matches",
			Config{
				Templates: map[string]TemplateConfig{"t": {Harness: "codex"}},
			},
			"codex",
			true,
		},
		{
			"task explicit harness matches",
			Config{
				Tasks: map[string]TaskConfig{"t": {Harness: "opencode"}},
			},
			"opencode",
			true,
		},
		{
			"no scope uses the queried harness",
			Config{
				Defaults: DefaultsConfig{Harness: "claude"},
				Tasks:    map[string]TaskConfig{"t": {}},
			},
			"opencode",
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg
			if got := cfg.UsesHarness(tt.query); got != tt.want {
				t.Errorf("UsesHarness(%q) = %v, want %v", tt.query, got, tt.want)
			}
		})
	}
}

func TestMergeHarnessOptionsImmutability(t *testing.T) {
	base := map[string]any{"permission_mode": "plan", "agent": "a.md"}
	override := map[string]any{"agent": "b.md"}
	baseSnapshot := map[string]any{"permission_mode": "plan", "agent": "a.md"}
	overrideSnapshot := map[string]any{"agent": "b.md"}

	merged := mergeHarnessOptions(base, override)

	want := map[string]any{"permission_mode": "plan", "agent": "b.md"}
	if len(merged) != len(want) {
		t.Fatalf("merged = %v, want %v", merged, want)
	}
	for k, v := range want {
		if merged[k] != v {
			t.Errorf("merged[%q] = %v, want %v", k, merged[k], v)
		}
	}

	// Inputs must not be mutated by the merge.
	if len(base) != len(baseSnapshot) || base["agent"] != baseSnapshot["agent"] || base["permission_mode"] != baseSnapshot["permission_mode"] {
		t.Errorf("base mutated: got %v, want %v", base, baseSnapshot)
	}
	if len(override) != len(overrideSnapshot) || override["agent"] != overrideSnapshot["agent"] {
		t.Errorf("override mutated: got %v, want %v", override, overrideSnapshot)
	}
}

func TestMergeHarnessOptionsNeverNil(t *testing.T) {
	merged := mergeHarnessOptions(nil, nil)
	if merged == nil {
		t.Error("mergeHarnessOptions(nil, nil) = nil, want empty non-nil map")
	}
}

// TestScopeHarnessOptionsDifferentHarnessDoesNotLeak asserts that a scope
// running a different harness than defaults gets only its own options —
// defaults' options for the other harness must not leak in. The merging
// logic must not require the scope's harness name to actually resolve.
func TestScopeHarnessOptionsDifferentHarnessDoesNotLeak(t *testing.T) {
	cfg := &Config{
		Defaults: DefaultsConfig{
			Harness:        "claude",
			HarnessOptions: map[string]any{"permission_mode": "plan"},
		},
	}
	task := TaskConfig{Harness: "other", HarnessOptions: map[string]any{"model_flag": "x"}}

	got := cfg.TaskHarnessOptions(task)
	want := map[string]any{"model_flag": "x"}
	if len(got) != len(want) || got["model_flag"] != want["model_flag"] {
		t.Errorf("TaskHarnessOptions() = %v, want %v (defaults options must not leak)", got, want)
	}
}

func TestValidateUnknownHarnessName(t *testing.T) {
	validConfig := func() *Config {
		return &Config{
			Defaults: DefaultsConfig{Model: "sonnet", MaxTurns: 15},
			HomePath: "/tmp/leo",
		}
	}

	t.Run("bad defaults harness errors once at defaults.harness", func(t *testing.T) {
		cfg := validConfig()
		cfg.Defaults.Harness = "bogus"
		err := cfg.Validate()
		if err == nil {
			t.Fatal("expected error")
		}
		got := err.Error()
		if !strings.Contains(got, `defaults.harness "bogus" is not a registered harness`) {
			t.Errorf("error = %q, want mention of defaults.harness", got)
		}
		if !strings.Contains(got, "available:") || !strings.Contains(got, "claude") {
			t.Errorf("error = %q, want available-harness list mentioning claude", got)
		}
		// Only one error should be emitted for the bad defaults harness,
		// even though it is also the harness every other scope inherits.
		if n := strings.Count(got, "is not a registered harness"); n != 1 {
			t.Errorf("error contains %d harness-name failures, want 1: %q", n, got)
		}
	})

	scopes := []struct {
		name  string
		apply func(*Config)
		want  string
	}{
		{
			"templates",
			func(c *Config) {
				c.Templates = map[string]TemplateConfig{"foo": {Harness: "bogus"}}
			},
			`templates.foo.harness "bogus" is not a registered harness`,
		},
		{
			"tasks",
			func(c *Config) {
				c.Tasks = map[string]TaskConfig{"foo": {
					Schedule: "0 * * * *", PromptFile: "p.md", Harness: "bogus",
				}}
			},
			`tasks.foo.harness "bogus" is not a registered harness`,
		},
	}
	for _, sc := range scopes {
		t.Run(sc.name, func(t *testing.T) {
			cfg := validConfig()
			sc.apply(cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatal("expected error")
			}
			if got := err.Error(); !strings.Contains(got, sc.want) {
				t.Errorf("error = %q, want %q", got, sc.want)
			}
		})
	}
}

func TestValidateHarnessOptionsErrors(t *testing.T) {
	registerStubNoChannels()

	tests := []struct {
		name  string
		apply func(*Config)
		want  string
	}{
		{
			"defaults unknown key",
			func(c *Config) { c.Defaults.HarnessOptions = map[string]any{"bogus": "x"} },
			"defaults.harness_options: ",
		},
		{
			"defaults bad type",
			func(c *Config) { c.Defaults.HarnessOptions = map[string]any{"agent": 5} },
			"defaults.harness_options: ",
		},
		{
			"defaults bad permission_mode value",
			func(c *Config) { c.Defaults.HarnessOptions = map[string]any{"permission_mode": "nope"} },
			"defaults.harness_options: ",
		},
		{
			"templates bad type",
			func(c *Config) {
				c.Templates = map[string]TemplateConfig{"foo": {
					HarnessOptions: map[string]any{"agent": 5},
				}}
			},
			"templates.foo.harness_options: ",
		},
		{
			"tasks bad permission_mode value",
			func(c *Config) {
				c.Tasks = map[string]TaskConfig{"foo": {
					Schedule: "0 * * * *", PromptFile: "p.md",
					HarnessOptions: map[string]any{"permission_mode": "nope"},
				}}
			},
			"tasks.foo.harness_options: ",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{Defaults: DefaultsConfig{Model: "sonnet", MaxTurns: 15}, HomePath: "/tmp/leo"}
			tt.apply(cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatal("expected error")
			}
			if got := err.Error(); !strings.Contains(got, tt.want) {
				t.Errorf("error = %q, want prefix %q", got, tt.want)
			}
		})
	}
}

func TestValidateChannelsUnsupportedHarness(t *testing.T) {
	registerStubNoChannels()

	tests := []struct {
		name  string
		apply func(*Config)
		want  string
	}{
		{
			"templates channels",
			func(c *Config) {
				c.Templates = map[string]TemplateConfig{"foo": {
					Harness: stubNoChannelsName, Channels: []string{"plugin:telegram@x"},
				}}
			},
			"templates.foo.channels: the stubnochannels harness does not support channel plugins",
		},
		{
			"tasks channels",
			func(c *Config) {
				c.Tasks = map[string]TaskConfig{"foo": {
					Schedule: "0 * * * *", PromptFile: "p.md",
					Harness: stubNoChannelsName, Channels: []string{"plugin:telegram@x"},
				}}
			},
			"tasks.foo.channels: the stubnochannels harness does not support channel plugins",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{Defaults: DefaultsConfig{Model: "sonnet", MaxTurns: 15}, HomePath: "/tmp/leo"}
			tt.apply(cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatal("expected error")
			}
			if got := err.Error(); !strings.Contains(got, tt.want) {
				t.Errorf("error = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestValidateModelDelegation locks in the scope-prefixed model error
// wording: config.Validate() must delegate to the harness adapter's
// ValidateModel and wrap it via `fmt.Sprintf("%s.model %v", scope, err)`,
// with no leading field path from the adapter itself.
func TestValidateModelDelegation(t *testing.T) {
	const want = `%s.model "not a model" is not valid (must not contain whitespace)`

	t.Run("defaults", func(t *testing.T) {
		cfg := &Config{Defaults: DefaultsConfig{Model: "not a model", MaxTurns: 15}, HomePath: "/tmp/leo"}
		err := cfg.Validate()
		if err == nil {
			t.Fatal("expected error")
		}
		if got := err.Error(); !strings.Contains(got, fmt.Sprintf(want, "defaults")) {
			t.Errorf("error = %q, want to contain %q", got, fmt.Sprintf(want, "defaults"))
		}
	})

	tests := []struct {
		name  string
		apply func(*Config)
		want  string
	}{
		{
			"templates",
			func(c *Config) {
				c.Templates = map[string]TemplateConfig{"foo": {Model: "not a model"}}
			},
			fmt.Sprintf(want, "templates.foo"),
		},
		{
			"tasks",
			func(c *Config) {
				c.Tasks = map[string]TaskConfig{"foo": {
					Schedule: "0 * * * *", PromptFile: "p.md", Model: "not a model",
				}}
			},
			fmt.Sprintf(want, "tasks.foo"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{Defaults: DefaultsConfig{Model: "sonnet", MaxTurns: 15}, HomePath: "/tmp/leo"}
			tt.apply(cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatal("expected error")
			}
			if got := err.Error(); !strings.Contains(got, tt.want) {
				t.Errorf("error = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestValidateKindSupportHappyPath confirms codex/opencode tasks with valid
// harness_options and models validate cleanly, and that the existing
// SupportsChannels() check still fires with codex named in the message.
func TestValidateKindSupportHappyPath(t *testing.T) {
	t.Run("codex task with sandbox option validates clean", func(t *testing.T) {
		cfg := &Config{
			Defaults: DefaultsConfig{Model: "sonnet", MaxTurns: 15},
			HomePath: "/tmp/leo",
			Tasks: map[string]TaskConfig{"nightly": {
				Schedule: "0 * * * *", PromptFile: "p.md",
				Harness:        "codex",
				Model:          "gpt-5.3-codex",
				HarnessOptions: map[string]any{"permission_mode": "workspace-write"},
			}},
		}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("Validate() = %v, want nil", err)
		}
	})

	t.Run("codex template validates clean (Plan 4 Task 5 TurnDriver)", func(t *testing.T) {
		cfg := &Config{
			Defaults: DefaultsConfig{Model: "sonnet", MaxTurns: 15},
			HomePath: "/tmp/leo",
			Templates: map[string]TemplateConfig{"helper": {
				Harness: "codex",
				Model:   "gpt-5.3-codex",
			}},
		}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("Validate() = %v, want nil", err)
		}
	})

	t.Run("opencode template validates clean (Plan 4 Task 6 ServerDriver)", func(t *testing.T) {
		cfg := &Config{
			Defaults: DefaultsConfig{Model: "sonnet", MaxTurns: 15},
			HomePath: "/tmp/leo",
			Templates: map[string]TemplateConfig{"helper": {
				Harness: "opencode",
				Model:   "anthropic/claude-sonnet-4-5",
			}},
		}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("Validate() = %v, want nil", err)
		}
	})

	t.Run("codex persistent task validates clean (Plan 4 Task 7 session drivers)", func(t *testing.T) {
		cfg := &Config{
			Defaults: DefaultsConfig{Model: "sonnet", MaxTurns: 15},
			HomePath: "/tmp/leo",
			Tasks: map[string]TaskConfig{"nightly": {
				Schedule: "0 * * * *", PromptFile: "p.md",
				Harness: "codex", Runtime: "persistent",
				Workspace: "/tmp/ws",
			}},
		}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("Validate() = %v, want nil", err)
		}
	})

	t.Run("opencode persistent task validates clean (Plan 4 Task 7 session drivers)", func(t *testing.T) {
		cfg := &Config{
			Defaults: DefaultsConfig{Model: "sonnet", MaxTurns: 15},
			HomePath: "/tmp/leo",
			Tasks: map[string]TaskConfig{"nightly": {
				Schedule: "0 * * * *", PromptFile: "p.md",
				Harness: "opencode", Runtime: "persistent",
				Workspace: "/tmp/ws",
			}},
		}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("Validate() = %v, want nil", err)
		}
	})

	t.Run("opencode task with permission option validates clean", func(t *testing.T) {
		cfg := &Config{
			Defaults: DefaultsConfig{Model: "sonnet", MaxTurns: 15},
			HomePath: "/tmp/leo",
			Tasks: map[string]TaskConfig{"nightly": {
				Schedule: "0 * * * *", PromptFile: "p.md",
				Harness:        "opencode",
				Model:          "anthropic/claude-sonnet-4-5",
				HarnessOptions: map[string]any{"permission": map[string]any{"bash": "allow"}},
			}},
		}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("Validate() = %v, want nil", err)
		}
	})

	t.Run("opencode task with claude-shaped model errors", func(t *testing.T) {
		cfg := &Config{
			Defaults: DefaultsConfig{Model: "sonnet", MaxTurns: 15},
			HomePath: "/tmp/leo",
			Tasks: map[string]TaskConfig{"t": {
				Schedule: "0 * * * *", PromptFile: "p.md",
				Harness: "opencode", Model: "opus",
			}},
		}
		err := cfg.Validate()
		if err == nil {
			t.Fatal("expected error")
		}
		want := `tasks.t.model "opus" is not valid (must be provider/model, e.g. anthropic/claude-sonnet-4-5)`
		if got := err.Error(); !strings.Contains(got, want) {
			t.Errorf("error = %q, want %q", got, want)
		}
	})

	t.Run("codex task with whitespace model errors", func(t *testing.T) {
		cfg := &Config{
			Defaults: DefaultsConfig{Model: "sonnet", MaxTurns: 15},
			HomePath: "/tmp/leo",
			Tasks: map[string]TaskConfig{"t": {
				Schedule: "0 * * * *", PromptFile: "p.md",
				Harness: "codex", Model: "gpt 5",
			}},
		}
		err := cfg.Validate()
		if err == nil {
			t.Fatal("expected error")
		}
		want := `tasks.t.model "gpt 5" is not valid (must not contain whitespace)`
		if got := err.Error(); !strings.Contains(got, want) {
			t.Errorf("error = %q, want %q", got, want)
		}
	})

	t.Run("codex task with channels still errors via SupportsChannels", func(t *testing.T) {
		cfg := &Config{
			Defaults: DefaultsConfig{Model: "sonnet", MaxTurns: 15},
			HomePath: "/tmp/leo",
			Tasks: map[string]TaskConfig{"t": {
				Schedule: "0 * * * *", PromptFile: "p.md",
				Harness: "codex", Channels: []string{"plugin:telegram@x"},
			}},
		}
		err := cfg.Validate()
		if err == nil {
			t.Fatal("expected error")
		}
		want := "tasks.t.channels: the codex harness does not support channel plugins; use leo's MCP tools for messaging"
		if got := err.Error(); !strings.Contains(got, want) {
			t.Errorf("error = %q, want %q", got, want)
		}
	})
}
