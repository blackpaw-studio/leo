package claude

import (
	"reflect"
	"testing"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// These are golden tests: they lock CURRENT behavior of processArgs,
// agentArgs, and taskArgs as read directly from args.go / args_shared.go.
// If an expectation here looks surprising, re-check the source before
// changing it — these tests are not a spec, they are a snapshot.

func TestProcessArgs(t *testing.T) {
	tests := []struct {
		name string
		spec harness.LaunchSpec
		opts Options
		want []string
	}{
		{
			name: "minimal",
			spec: harness.LaunchSpec{
				Kind:      harness.KindProcess,
				Model:     "sonnet",
				Workspace: "/ws",
			},
			opts: Options{},
			want: []string{
				"--model", "sonnet",
				"--add-dir", "/ws",
			},
		},
		{
			name: "fully populated",
			spec: harness.LaunchSpec{
				Kind:        harness.KindProcess,
				Model:       "opus",
				Workspace:   "/ws",
				AddDirs:     []string{"/extra1", "/extra2"},
				Channels:    []string{"plugin:telegram@official"},
				DevChannels: []string{"plugin:dev@local"},
			},
			opts: Options{
				RemoteControl:       true,
				RemoteControlPrefix: "leo-",
				PermissionMode:      "acceptEdits",
				BypassPermissions:   true, // should be ignored — PermissionMode wins
				AgentFile:           "/agents/foo.md",
				AllowedTools:        []string{"Read", "Write"},
				DisallowedTools:     []string{"Bash"},
				AppendSystemPrompt:  "extra prompt",
				MCPConfigPath:       "/mcp/user.json",
				LeoMCPArgs:          []string{"--mcp-config", "/state/leo-mcp.json"},
			},
			want: []string{
				"--model", "opus",
				"--channels", "plugin:telegram@official",
				"--dangerously-load-development-channels", "plugin:dev@local",
				"--add-dir", "/ws",
				"--add-dir", "/extra1",
				"--add-dir", "/extra2",
				"--remote-control",
				"--remote-control-session-name-prefix", "leo-",
				"--permission-mode", "acceptEdits",
				"--mcp-config", "/mcp/user.json",
				"--mcp-config", "/state/leo-mcp.json",
				"--agent", "/agents/foo.md",
				"--allowed-tools", "Read,Write",
				"--disallowed-tools", "Bash",
				"--append-system-prompt", "extra prompt",
			},
		},
		{
			name: "bypass fallback when permission mode empty",
			spec: harness.LaunchSpec{
				Kind:      harness.KindProcess,
				Model:     "sonnet",
				Workspace: "/ws",
			},
			opts: Options{
				BypassPermissions: true,
			},
			want: []string{
				"--model", "sonnet",
				"--add-dir", "/ws",
				"--dangerously-skip-permissions",
			},
		},
		{
			name: "remote control without prefix",
			spec: harness.LaunchSpec{
				Kind:      harness.KindProcess,
				Model:     "sonnet",
				Workspace: "/ws",
			},
			opts: Options{
				RemoteControl: true,
			},
			want: []string{
				"--model", "sonnet",
				"--add-dir", "/ws",
				"--remote-control",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.spec.Options = tt.opts
			got, err := Claude{}.Args(tt.spec)
			if err != nil {
				t.Fatalf("Args() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Args() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestAgentArgs(t *testing.T) {
	tests := []struct {
		name string
		spec harness.LaunchSpec
		opts Options
		want []string
	}{
		{
			name: "minimal",
			spec: harness.LaunchSpec{
				Kind:      harness.KindAgent,
				Model:     "sonnet",
				Name:      "my-agent",
				Workspace: "/ws",
			},
			opts: Options{},
			want: []string{
				"--model", "sonnet",
				"--add-dir", "/ws",
				"--name", "my-agent",
			},
		},
		{
			name: "fully populated",
			spec: harness.LaunchSpec{
				Kind:        harness.KindAgent,
				Model:       "opus",
				Name:        "my-agent",
				Workspace:   "/ws",
				AddDirs:     []string{"/extra1"},
				Channels:    []string{"plugin:telegram@official"},
				DevChannels: []string{"plugin:dev@local"},
				MaxTurns:    5,
				Prompt:      "do the thing",
			},
			opts: Options{
				RemoteControl:      true, // agent kind: no prefix flag emitted
				PermissionMode:     "acceptEdits",
				AgentFile:          "/agents/foo.md",
				AllowedTools:       []string{"Read"},
				DisallowedTools:    []string{"Bash"},
				AppendSystemPrompt: "extra prompt",
				MCPConfigPath:      "/mcp/user.json",
				LeoMCPArgs:         []string{"--mcp-config", "/state/leo-mcp.json"},
			},
			want: []string{
				"--model", "opus",
				"--channels", "plugin:telegram@official",
				"--dangerously-load-development-channels", "plugin:dev@local",
				"--add-dir", "/ws",
				"--add-dir", "/extra1",
				"--remote-control",
				"--name", "my-agent",
				"--permission-mode", "acceptEdits",
				"--mcp-config", "/mcp/user.json",
				"--agent", "/agents/foo.md",
				"--allowed-tools", "Read",
				"--disallowed-tools", "Bash",
				"--append-system-prompt", "extra prompt",
				"--mcp-config", "/state/leo-mcp.json",
				"--max-turns", "5",
				"do the thing",
			},
		},
		{
			name: "bypass permissions ignored (structural footgun guard)",
			spec: harness.LaunchSpec{
				Kind:      harness.KindAgent,
				Model:     "sonnet",
				Name:      "my-agent",
				Workspace: "/ws",
			},
			opts: Options{
				BypassPermissions: true,
			},
			want: []string{
				"--model", "sonnet",
				"--add-dir", "/ws",
				"--name", "my-agent",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.spec.Options = tt.opts
			got, err := Claude{}.Args(tt.spec)
			if err != nil {
				t.Fatalf("Args() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Args() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestSessionKindArgs(t *testing.T) {
	tests := []struct {
		name string
		spec harness.LaunchSpec
		opts Options
		want []string
	}{
		{
			name: "full-featured",
			spec: harness.LaunchSpec{
				Kind:      harness.KindSession,
				Model:     "opus",
				Workspace: "/ws",
				AddDirs:   []string{"/extra1", "/extra2"},
				Channels:  []string{"plugin:telegram@official", "plugin:slack@official"},
				Session:   harness.SessionState{Mode: harness.SessionResume, ID: "csid-1"},
			},
			opts: Options{
				PermissionMode:     "acceptEdits",
				AgentFile:          "/agents/foo.md",
				AllowedTools:       []string{"Read", "Write"},
				DisallowedTools:    []string{"Bash"},
				AppendSystemPrompt: "extra prompt",
			},
			want: []string{
				"--model", "opus",
				"--resume", "csid-1",
				"--permission-mode", "acceptEdits",
				"--channels", "plugin:telegram@official",
				"--channels", "plugin:slack@official",
				"--agent", "/agents/foo.md",
				"--add-dir", "/ws",
				"--add-dir", "/extra1",
				"--add-dir", "/extra2",
				"--allowed-tools", "Read,Write",
				"--disallowed-tools", "Bash",
				"--append-system-prompt", "extra prompt",
			},
		},
		{
			name: "minimal (only workdir)",
			spec: harness.LaunchSpec{
				Kind:      harness.KindSession,
				Workspace: "/ws",
			},
			opts: Options{},
			want: []string{
				"--add-dir", "/ws",
			},
		},
		{
			name: "empty model",
			spec: harness.LaunchSpec{
				Kind:      harness.KindSession,
				Model:     "",
				Workspace: "/ws",
				Channels:  []string{"plugin:telegram@official"},
			},
			opts: Options{
				PermissionMode: "acceptEdits",
			},
			want: []string{
				"--permission-mode", "acceptEdits",
				"--channels", "plugin:telegram@official",
				"--add-dir", "/ws",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.spec.Options = tt.opts
			got, err := Claude{}.Args(tt.spec)
			if err != nil {
				t.Fatalf("Args() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Args() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestTaskArgs(t *testing.T) {
	tests := []struct {
		name string
		spec harness.LaunchSpec
		opts Options
		want []string
	}{
		{
			name: "minimal",
			spec: harness.LaunchSpec{
				Kind:      harness.KindTask,
				Model:     "sonnet",
				Workspace: "/ws",
				Prompt:    "run the task",
				MaxTurns:  3,
			},
			opts: Options{},
			want: []string{
				"-p", "run the task",
				"--model", "sonnet",
				"--max-turns", "3",
				"--output-format", "stream-json",
				"--verbose",
				"--add-dir", "/ws",
			},
		},
		{
			name: "fully populated, dev channels only (no --channels even though Channels is set)",
			spec: harness.LaunchSpec{
				Kind:        harness.KindTask,
				Model:       "opus",
				Workspace:   "/ws",
				Prompt:      "run the task",
				MaxTurns:    7,
				Channels:    []string{"plugin:telegram@official"}, // must NOT emit --channels
				DevChannels: []string{"plugin:dev@local"},
				Session:     harness.SessionState{Mode: harness.SessionResume, ID: "sess-123"},
			},
			opts: Options{
				PermissionMode:     "acceptEdits",
				BypassPermissions:  true, // ignored — PermissionMode wins
				AllowedTools:       []string{"Read"},
				DisallowedTools:    []string{"Bash"},
				AppendSystemPrompt: "extra prompt",
				MCPConfigPath:      "/mcp/user.json",
				LeoMCPArgs:         []string{"--mcp-config", "/state/leo-mcp.json"},
			},
			want: []string{
				"-p", "run the task",
				"--model", "opus",
				"--max-turns", "7",
				"--output-format", "stream-json",
				"--verbose",
				"--dangerously-load-development-channels", "plugin:dev@local",
				"--resume", "sess-123",
				"--permission-mode", "acceptEdits",
				"--mcp-config", "/mcp/user.json",
				"--mcp-config", "/state/leo-mcp.json",
				"--add-dir", "/ws",
				"--allowed-tools", "Read",
				"--disallowed-tools", "Bash",
				"--append-system-prompt", "extra prompt",
			},
		},
		{
			name: "bypass fallback when permission mode empty",
			spec: harness.LaunchSpec{
				Kind:      harness.KindTask,
				Model:     "sonnet",
				Workspace: "/ws",
				Prompt:    "run the task",
				MaxTurns:  1,
			},
			opts: Options{
				BypassPermissions: true,
			},
			want: []string{
				"-p", "run the task",
				"--model", "sonnet",
				"--max-turns", "1",
				"--output-format", "stream-json",
				"--verbose",
				"--dangerously-skip-permissions",
				"--add-dir", "/ws",
			},
		},
		{
			name: "session pinned",
			spec: harness.LaunchSpec{
				Kind:      harness.KindTask,
				Model:     "sonnet",
				Workspace: "/ws",
				Prompt:    "run the task",
				MaxTurns:  1,
				Session:   harness.SessionState{Mode: harness.SessionPinned, ID: "pin-1"},
			},
			opts: Options{},
			want: []string{
				"-p", "run the task",
				"--model", "sonnet",
				"--max-turns", "1",
				"--output-format", "stream-json",
				"--verbose",
				"--session-id", "pin-1",
				"--add-dir", "/ws",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.spec.Options = tt.opts
			got, err := Claude{}.Args(tt.spec)
			if err != nil {
				t.Fatalf("Args() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Args() = %#v, want %#v", got, tt.want)
			}
		})
	}
}
