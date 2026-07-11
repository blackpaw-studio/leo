package run

import (
	"reflect"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

func TestBuildArgsCharacterization(t *testing.T) {
	tests := []struct {
		name      string
		cfg       *config.Config
		task      config.TaskConfig
		prompt    string
		sessionID string
		leoMCPOK  bool
		want      []string
	}{
		{
			name: "minimal",
			cfg: &config.Config{
				HomePath: "/tmp/leo-home",
				Defaults: config.DefaultsConfig{Model: "opus"},
			},
			task:   config.TaskConfig{Workspace: "/tmp/ws"},
			prompt: "do the thing",
			want: []string{
				"-p", "do the thing",
				"--model", "opus",
				"--max-turns", "15",
				"--output-format", "stream-json",
				"--verbose",
				"--add-dir", "/tmp/ws",
			},
		},
		{
			name: "resume with dev channels, tools, bypass fallback",
			cfg: &config.Config{
				HomePath: "/tmp/leo-home",
				Defaults: config.DefaultsConfig{
					Model: "opus",
					HarnessOptions: map[string]any{
						"bypass_permissions": true,
						"allowed_tools":      []any{"Read", "Bash"},
					},
				},
			},
			task: config.TaskConfig{
				Workspace:   "/tmp/ws",
				Model:       "sonnet",
				MaxTurns:    30,
				DevChannels: []string{"plugin:dev@local"},
				HarnessOptions: map[string]any{
					"append_system_prompt": "be terse",
				},
			},
			prompt:    "nightly run",
			sessionID: "sess-789",
			want: []string{
				"-p", "nightly run",
				"--model", "sonnet",
				"--max-turns", "30",
				"--output-format", "stream-json",
				"--verbose",
				"--dangerously-load-development-channels", "plugin:dev@local",
				"--resume", "sess-789",
				"--dangerously-skip-permissions",
				"--add-dir", "/tmp/ws",
				"--allowed-tools", "Read,Bash",
				"--append-system-prompt", "be terse",
			},
		},
		{
			name: "task permission mode wins over bypass",
			cfg: &config.Config{
				HomePath: "/tmp/leo-home",
				Defaults: config.DefaultsConfig{
					Model:          "opus",
					HarnessOptions: map[string]any{"bypass_permissions": true},
				},
			},
			task: config.TaskConfig{
				Workspace:      "/tmp/ws",
				HarnessOptions: map[string]any{"permission_mode": "plan"},
			},
			prompt: "plan it",
			want: []string{
				"-p", "plan it",
				"--model", "opus",
				"--max-turns", "15",
				"--output-format", "stream-json",
				"--verbose",
				"--permission-mode", "plan",
				"--add-dir", "/tmp/ws",
			},
		},
		{
			name: "defaults-level option inherited by task scope",
			cfg: &config.Config{
				HomePath: "/tmp/leo-home",
				Defaults: config.DefaultsConfig{
					Model:          "opus",
					HarnessOptions: map[string]any{"permission_mode": "plan"},
				},
			},
			task:   config.TaskConfig{Workspace: "/tmp/ws"},
			prompt: "plan it",
			want: []string{
				"-p", "plan it",
				"--model", "opus",
				"--max-turns", "15",
				"--output-format", "stream-json",
				"--verbose",
				"--permission-mode", "plan",
				"--add-dir", "/tmp/ws",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildArgs(tt.cfg, tt.task, "mytask", tt.prompt, tt.sessionID, tt.leoMCPOK)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("buildArgs argv mismatch\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}
