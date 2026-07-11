package codex

import (
	"reflect"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/harness"
)

func TestValidateModel(t *testing.T) {
	tests := []struct {
		name    string
		model   string
		wantErr string
	}{
		{name: "empty ok", model: ""},
		{name: "gpt-5.3-codex ok", model: "gpt-5.3-codex"},
		{name: "o4-mini ok", model: "o4-mini"},
		{name: "whitespace errors", model: "gpt 5", wantErr: `"gpt 5" is not valid (must not contain whitespace)`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Codex{}.ValidateModel(tt.model)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error %q, got nil", tt.wantErr)
			}
			if err.Error() != tt.wantErr {
				t.Fatalf("got error %q, want %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestSupportsChannels(t *testing.T) {
	if (Codex{}).SupportsChannels() {
		t.Error("expected SupportsChannels to be false")
	}
}

func TestSupportsKind(t *testing.T) {
	tests := []struct {
		kind harness.Kind
		want bool
	}{
		{harness.KindTask, true},
		{harness.KindProcess, true},
		{harness.KindAgent, true},
		{harness.KindSession, false},
	}
	for _, tt := range tests {
		t.Run(string(tt.kind), func(t *testing.T) {
			if got := (Codex{}).SupportsKind(tt.kind); got != tt.want {
				t.Errorf("SupportsKind(%s) = %v, want %v", tt.kind, got, tt.want)
			}
		})
	}
}

func TestSessionArgs(t *testing.T) {
	tests := []struct {
		name  string
		state harness.SessionState
		want  []string
	}{
		{
			name:  "resume",
			state: harness.SessionState{Mode: harness.SessionResume, ID: "tid-1"},
			want:  []string{"resume", "tid-1"},
		},
		{
			name:  "none",
			state: harness.SessionState{Mode: harness.SessionNone},
			want:  nil,
		},
		{
			name:  "pinned",
			state: harness.SessionState{Mode: harness.SessionPinned, ID: "tid-2"},
			want:  nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Codex{}.SessionArgs(tt.state)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestArgs(t *testing.T) {
	tests := []struct {
		name string
		spec harness.LaunchSpec
		want []string
	}{
		{
			name: "fresh minimal",
			spec: harness.LaunchSpec{Kind: harness.KindTask, Prompt: "do it", Options: Options{}},
			want: []string{"exec", "--json", "--skip-git-repo-check", "do it"},
		},
		{
			name: "model, sandbox, resume",
			spec: harness.LaunchSpec{
				Kind: harness.KindTask, Prompt: "again", Model: "gpt-5.3-codex",
				Session: harness.SessionState{Mode: harness.SessionResume, ID: "tid-9"},
				Options: Options{Sandbox: "workspace-write"},
			},
			want: []string{"exec", "--json", "--skip-git-repo-check",
				"--model", "gpt-5.3-codex", "--sandbox", "workspace-write",
				"resume", "tid-9", "again"},
		},
		{
			name: "leo MCP bridge",
			spec: harness.LaunchSpec{Kind: harness.KindTask, Prompt: "p", Options: Options{
				LeoMCP: &LeoMCPBridge{
					Command: "leo", Args: []string{"mcp-server"},
					EnvVars:      []string{"LEO_PROCESS_NAME", "LEO_WEB_PORT", "LEO_API_TOKEN"},
					ApprovalMode: "approve",
				},
			}},
			want: []string{"exec", "--json", "--skip-git-repo-check",
				"-c", `mcp_servers.leo.command="leo"`,
				"-c", `mcp_servers.leo.args=["mcp-server"]`,
				"-c", `mcp_servers.leo.env_vars=["LEO_PROCESS_NAME","LEO_WEB_PORT","LEO_API_TOKEN"]`,
				"-c", `mcp_servers.leo.default_tools_approval_mode="approve"`,
				"p"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Codex{}.Args(tt.spec)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v\nwant %v", got, tt.want)
			}
		})
	}
}

func TestCodexArgsInteractiveKindsRenderTurnPrefix(t *testing.T) {
	tests := []struct {
		name string
		spec harness.LaunchSpec
		want []string
	}{
		{
			name: "KindProcess turn prefix",
			spec: harness.LaunchSpec{
				Kind: harness.KindProcess, Model: "gpt-5.3-codex",
				Options: Options{Sandbox: "workspace-write"},
			},
			want: []string{"exec", "--json", "--skip-git-repo-check",
				"--model", "gpt-5.3-codex", "--sandbox", "workspace-write"},
		},
		{
			name: "KindAgent turn prefix",
			spec: harness.LaunchSpec{
				Kind: harness.KindAgent, Model: "gpt-5.3-codex",
				Options: Options{
					LeoMCP: &LeoMCPBridge{
						Command: "leo", Args: []string{"mcp-server"},
						EnvVars:      []string{"LEO_PROCESS_NAME"},
						ApprovalMode: "approve",
					},
				},
			},
			want: []string{"exec", "--json", "--skip-git-repo-check",
				"--model", "gpt-5.3-codex",
				"-c", `mcp_servers.leo.command="leo"`,
				"-c", `mcp_servers.leo.args=["mcp-server"]`,
				"-c", `mcp_servers.leo.env_vars=["LEO_PROCESS_NAME"]`,
				"-c", `mcp_servers.leo.default_tools_approval_mode="approve"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Codex{}.Args(tt.spec)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if strings.Join(got, "\x00") != strings.Join(tt.want, "\x00") {
				t.Errorf("got %#v\nwant %#v", got, tt.want)
			}
			for _, tok := range got {
				if tok == "resume" {
					t.Errorf("turn prefix must not include a resume subcommand: %#v", got)
				}
			}
		})
	}
}

func TestArgsErrors(t *testing.T) {
	tests := []struct {
		name    string
		spec    harness.LaunchSpec
		wantErr string
	}{
		{
			name:    "KindSession unsupported",
			spec:    harness.LaunchSpec{Kind: harness.KindSession, Options: Options{}},
			wantErr: `codex: session launches are not supported yet (only scheduled tasks) — session drivers land in a later plan`,
		},
		{
			name:    "SessionPinned",
			spec:    harness.LaunchSpec{Kind: harness.KindTask, Options: Options{}, Session: harness.SessionState{Mode: harness.SessionPinned, ID: "x"}},
			wantErr: `codex: cannot start a session with a pre-issued ID`,
		},
		{
			name:    "wrong Options type",
			spec:    harness.LaunchSpec{Kind: harness.KindTask, Options: "not-codex-options"},
			wantErr: `codex: spec.Options is string, want codex.Options`,
		},
		{
			name:    "channels unsupported",
			spec:    harness.LaunchSpec{Kind: harness.KindTask, Options: Options{}, Channels: []string{"plugin:telegram"}},
			wantErr: `codex: channel plugins are not supported; use leo's MCP tools for messaging`,
		},
		{
			name:    "dev channels unsupported",
			spec:    harness.LaunchSpec{Kind: harness.KindTask, Options: Options{}, DevChannels: []string{"plugin:telegram"}},
			wantErr: `codex: channel plugins are not supported; use leo's MCP tools for messaging`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Codex{}.Args(tt.spec)
			if err == nil {
				t.Fatalf("expected error %q, got nil", tt.wantErr)
			}
			if err.Error() != tt.wantErr {
				t.Fatalf("got error %q, want %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestCodexEnv(t *testing.T) {
	env, err := Codex{}.Env(harness.LaunchSpec{Kind: harness.KindTask, Prompt: "p"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if env != nil {
		t.Errorf("got %v, want nil", env)
	}
}
