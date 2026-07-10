package cli

import (
	"reflect"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

// Characterization tests: lock buildProcessArgs's argv byte-for-byte across
// the harness refactor. Web is disabled in every case so leomcp.AppendArg
// is a no-op and MergeSystemPrompt passes through (no state-dir writes).
func TestBuildProcessArgsCharacterization(t *testing.T) {
	boolPtr := func(b bool) *bool { return &b }

	tests := []struct {
		name string
		cfg  *config.Config
		proc config.ProcessConfig
		want []string
	}{
		{
			name: "minimal defaults",
			cfg: &config.Config{
				HomePath: "/tmp/leo-home",
				Defaults: config.DefaultsConfig{Model: "opus"},
			},
			proc: config.ProcessConfig{Workspace: "/tmp/ws"},
			want: []string{"--model", "opus", "--add-dir", "/tmp/ws"},
		},
		{
			name: "kitchen sink",
			cfg: &config.Config{
				HomePath: "/tmp/leo-home",
				Defaults: config.DefaultsConfig{
					Model:           "opus",
					AllowedTools:    []string{"Read", "Bash"},
					DisallowedTools: []string{"WebFetch"},
				},
			},
			proc: config.ProcessConfig{
				Workspace:          "/tmp/ws",
				Channels:           []string{"plugin:telegram@claude-plugins-official"},
				DevChannels:        []string{"plugin:dev@local"},
				AddDirs:            []string{"/tmp/extra"},
				RemoteControl:      boolPtr(true),
				PermissionMode:     "acceptEdits",
				Agent:              "rocket",
				AppendSystemPrompt: "be terse",
			},
			want: []string{
				"--model", "opus",
				"--channels", "plugin:telegram@claude-plugins-official",
				"--dangerously-load-development-channels", "plugin:dev@local",
				"--add-dir", "/tmp/ws",
				"--add-dir", "/tmp/extra",
				"--remote-control", "--remote-control-session-name-prefix", "myproc",
				"--permission-mode", "acceptEdits",
				"--agent", "rocket",
				"--allowed-tools", "Read,Bash",
				"--disallowed-tools", "WebFetch",
				"--append-system-prompt", "be terse",
			},
		},
		{
			name: "bypass permissions legacy fallback",
			cfg: &config.Config{
				HomePath: "/tmp/leo-home",
				Defaults: config.DefaultsConfig{Model: "sonnet", BypassPermissions: true},
			},
			proc: config.ProcessConfig{Workspace: "/tmp/ws"},
			want: []string{
				"--model", "sonnet",
				"--add-dir", "/tmp/ws",
				"--dangerously-skip-permissions",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildProcessArgs(tt.cfg, "myproc", tt.proc)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("buildProcessArgs argv mismatch\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}
