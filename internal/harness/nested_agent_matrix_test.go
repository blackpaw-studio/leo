package harness_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/harness/claude"
	"github.com/blackpaw-studio/leo/internal/harness/codex"
	"github.com/blackpaw-studio/leo/internal/harness/opencode"
)

func TestNestedAgentRestrictionMatrix(t *testing.T) {
	cases := []struct {
		name       string
		kind       harness.Kind
		dispatched bool
	}{
		{"dispatched headless", harness.KindTask, true},
		{"dispatched interactive", harness.KindAgent, true},
		{"ephemeral agent", harness.KindAgent, false},
		{"persistent task", harness.KindAgent, false},
		{"oneshot task", harness.KindTask, false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			spec := harness.LaunchSpec{Kind: tt.kind, Model: "model", Prompt: "prompt", Dispatched: tt.dispatched}

			claudeArgs, err := (claude.Claude{}).Args(withOptions(spec, claude.Options{}))
			if err != nil {
				t.Fatal(err)
			}
			assertContainsExactly(t, claudeArgs, "--disallowed-tools", "Agent", tt.dispatched)

			codexArgs, err := (codex.Codex{}).Args(withOptions(spec, codex.Options{}))
			if err != nil {
				t.Fatal(err)
			}
			assertContainsExactly(t, codexArgs, "-c", "features.multi_agent=false", tt.dispatched)

			opencodeSpec := withOptions(spec, opencode.Options{})
			env, err := (opencode.Opencode{}).Env(opencodeSpec)
			if err != nil {
				t.Fatal(err)
			}
			if !tt.dispatched {
				if env != nil {
					t.Fatalf("opencode env = %#v, want nil", env)
				}
				return
			}
			var cfg struct {
				Permission map[string]string `json:"permission"`
			}
			if err := json.Unmarshal([]byte(env["OPENCODE_CONFIG_CONTENT"]), &cfg); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(cfg.Permission, map[string]string{"task": "deny"}) {
				t.Fatalf("opencode permission = %#v", cfg.Permission)
			}
		})
	}
}

func withOptions(spec harness.LaunchSpec, options any) harness.LaunchSpec {
	spec.Options = options
	return spec
}

func assertContainsExactly(t *testing.T, args []string, flag, value string, want bool) {
	t.Helper()
	got := false
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			got = true
		}
	}
	if got != want {
		t.Fatalf("argv %#v contains %q %q = %v, want %v", args, flag, value, got, want)
	}
}
