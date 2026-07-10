package agent

import (
	"reflect"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

// hasFlagValue reports whether args contains `flag` immediately followed by a
// value that contains `substr`.
func hasFlagValue(args []string, flag, substr string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && strings.Contains(args[i+1], substr) {
			return true
		}
	}
	return false
}

func TestBuildTemplateArgsWiresLeoMCPWhenWebEnabled(t *testing.T) {
	// HomePath must be set: AppendArg writes leo-mcp.json under StatePath
	// (HomePath/state). An empty HomePath would write relative to cwd.
	cfg := &config.Config{HomePath: t.TempDir(), Web: config.WebConfig{Enabled: true}}
	tmpl := config.TemplateConfig{}

	args := BuildTemplateArgs(cfg, tmpl, "agent-x", "/tmp/ws", "")

	if !hasFlagValue(args, "--mcp-config", "leo-mcp.json") {
		t.Errorf("expected --mcp-config pointing at leo-mcp.json; got %v", args)
	}
	if !hasFlagValue(args, "--append-system-prompt", "leo_send_message") {
		t.Errorf("expected awareness line in --append-system-prompt; got %v", args)
	}
}

func TestBuildTemplateArgsNoLeoMCPWhenWebDisabled(t *testing.T) {
	cfg := &config.Config{HomePath: t.TempDir(), Web: config.WebConfig{Enabled: false}}
	args := BuildTemplateArgs(cfg, config.TemplateConfig{}, "agent-x", "/tmp/ws", "")

	if hasFlagValue(args, "--append-system-prompt", "leo_send_message") {
		t.Errorf("awareness line must not appear when web disabled; got %v", args)
	}
}

func TestBuildTemplateArgsCharacterization(t *testing.T) {
	tests := []struct {
		name   string
		cfg    *config.Config
		tmpl   config.TemplateConfig
		prompt string
		want   []string
	}{
		{
			name: "minimal — remote control defaults on, max turns default",
			cfg: &config.Config{
				HomePath: "/tmp/leo-home",
				Defaults: config.DefaultsConfig{Model: "opus"},
			},
			tmpl: config.TemplateConfig{},
			want: []string{
				"--model", "opus",
				"--add-dir", "/tmp/ws",
				"--remote-control",
				"--name", "myagent",
				"--max-turns", "15",
			},
		},
		{
			name: "full template with opening prompt",
			cfg: &config.Config{
				HomePath: "/tmp/leo-home",
				Defaults: config.DefaultsConfig{
					Model:          "opus",
					HarnessOptions: map[string]any{"permission_mode": "acceptEdits"},
				},
			},
			tmpl: config.TemplateConfig{
				Model:    "sonnet",
				Channels: []string{"plugin:telegram@claude-plugins-official"},
				AddDirs:  []string{"/tmp/extra"},
				MaxTurns: 50,
				HarnessOptions: map[string]any{
					"remote_control":       false,
					"agent":                "rocket",
					"allowed_tools":        []any{"Read"},
					"disallowed_tools":     []any{"WebFetch"},
					"append_system_prompt": "be terse",
				},
			},
			prompt: "hello world",
			want: []string{
				"--model", "sonnet",
				"--channels", "plugin:telegram@claude-plugins-official",
				"--add-dir", "/tmp/ws",
				"--add-dir", "/tmp/extra",
				"--name", "myagent",
				"--permission-mode", "acceptEdits",
				"--agent", "rocket",
				"--allowed-tools", "Read",
				"--disallowed-tools", "WebFetch",
				"--append-system-prompt", "be terse",
				"--max-turns", "50",
				"hello world",
			},
		},
		{
			name: "unsafe add_dir dropped",
			cfg: &config.Config{
				HomePath: "/tmp/leo-home",
				Defaults: config.DefaultsConfig{Model: "opus"},
			},
			tmpl: config.TemplateConfig{
				AddDirs: []string{"/ok/dir", "/bad;dir"},
			},
			want: []string{
				"--model", "opus",
				"--add-dir", "/tmp/ws",
				"--add-dir", "/ok/dir",
				"--remote-control",
				"--name", "myagent",
				"--max-turns", "15",
			},
		},
		{
			name: "defaults-level option inherited by template",
			cfg: &config.Config{
				HomePath: "/tmp/leo-home",
				Defaults: config.DefaultsConfig{
					Model:          "opus",
					HarnessOptions: map[string]any{"agent": "shared-agent"},
				},
			},
			tmpl: config.TemplateConfig{},
			want: []string{
				"--model", "opus",
				"--add-dir", "/tmp/ws",
				"--remote-control",
				"--name", "myagent",
				"--agent", "shared-agent",
				"--max-turns", "15",
			},
		},
		{
			name: "template's own remote_control:false suppresses flag even when defaults.harness_options.remote_control:true",
			cfg: &config.Config{
				HomePath: "/tmp/leo-home",
				Defaults: config.DefaultsConfig{
					Model:          "opus",
					HarnessOptions: map[string]any{"remote_control": true},
				},
			},
			tmpl: config.TemplateConfig{
				HarnessOptions: map[string]any{"remote_control": false},
			},
			want: []string{
				"--model", "opus",
				"--add-dir", "/tmp/ws",
				"--name", "myagent",
				"--max-turns", "15",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildTemplateArgs(tt.cfg, tt.tmpl, "myagent", "/tmp/ws", tt.prompt)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("BuildTemplateArgs argv mismatch\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}
