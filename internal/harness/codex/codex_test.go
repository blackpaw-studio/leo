package codex

import (
	"reflect"
	"testing"
	"time"

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
		{harness.KindAgent, true},
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
		// want returns the expected argv given the per-test temp workspace,
		// so expectations track sandboxWritableRootsArgs' own (symlink-
		// resolved) output rather than duplicating its logic with literals.
		want func(ws string) []string
	}{
		{
			name: "fresh minimal",
			spec: harness.LaunchSpec{Kind: harness.KindTask, Prompt: "do it", Options: Options{}},
			want: func(ws string) []string {
				return withRoots([]string{"exec", "--json", "--skip-git-repo-check"}, ws, []string{"do it"})
			},
		},
		{
			name: "model, permission_mode, resume",
			spec: harness.LaunchSpec{
				Kind: harness.KindTask, Prompt: "again", Model: "gpt-5.3-codex",
				Session: harness.SessionState{Mode: harness.SessionResume, ID: "tid-9"},
				Options: Options{PermissionMode: "workspace-write"},
			},
			want: func(ws string) []string {
				return withRoots([]string{"exec", "--json", "--skip-git-repo-check",
					"--model", "gpt-5.3-codex", "--sandbox", "workspace-write"},
					ws, []string{"resume", "tid-9", "again"})
			},
		},
		{
			// --approve-for-me is a self-contained preset: codex's own CLI
			// rejects it alongside --sandbox, so Args must emit it alone.
			name: "permission_mode approve-for-me omits --sandbox",
			spec: harness.LaunchSpec{
				Kind: harness.KindTask, Prompt: "go", Model: "gpt-5.6-sol",
				Options: Options{PermissionMode: "approve-for-me"},
			},
			want: func(ws string) []string {
				return withRoots([]string{"exec", "--json", "--skip-git-repo-check",
					"--model", "gpt-5.6-sol", "--approve-for-me"}, ws, []string{"go"})
			},
		},
		{
			name: "leo MCP bridge",
			spec: harness.LaunchSpec{Kind: harness.KindTask, Prompt: "p", Options: Options{
				LeoMCP: &LeoMCPBridge{
					Command: "leo", Args: []string{"mcp-server"},
					EnvVars:      []string{"LEO_PROCESS_NAME", "LEO_WEB_PORT", "LEO_API_TOKEN"},
					ApprovalMode: "approve",
					ToolTimeout:  32 * time.Minute,
				},
			}},
			want: func(ws string) []string {
				return withRoots([]string{"exec", "--json", "--skip-git-repo-check"}, ws, []string{
					"-c", `mcp_servers.leo.command="leo"`,
					"-c", `mcp_servers.leo.args=["mcp-server"]`,
					"-c", `mcp_servers.leo.env_vars=["LEO_PROCESS_NAME","LEO_WEB_PORT","LEO_API_TOKEN"]`,
					"-c", `mcp_servers.leo.default_tools_approval_mode="approve"`,
					"-c", `mcp_servers.leo.tool_timeout_sec=1920`,
					"p"})
			},
		},
		{
			// A zero ToolTimeout leaves codex's own default in place rather
			// than rendering a nonsense `tool_timeout_sec=0`.
			name: "leo MCP bridge without tool timeout",
			spec: harness.LaunchSpec{Kind: harness.KindTask, Prompt: "p", Options: Options{
				LeoMCP: &LeoMCPBridge{
					Command: "leo", Args: []string{"mcp-server"},
					ApprovalMode: "approve",
				},
			}},
			want: func(ws string) []string {
				return withRoots([]string{"exec", "--json", "--skip-git-repo-check"}, ws, []string{
					"-c", `mcp_servers.leo.command="leo"`,
					"-c", `mcp_servers.leo.args=["mcp-server"]`,
					"-c", `mcp_servers.leo.env_vars=[]`,
					"-c", `mcp_servers.leo.default_tools_approval_mode="approve"`,
					"p"})
			},
		},
		{
			name: "leo system context nudge",
			spec: harness.LaunchSpec{Kind: harness.KindTask, Prompt: "p", SystemContext: "you're running under leo",
				Options: Options{}},
			want: func(ws string) []string {
				return withRoots([]string{"exec", "--json", "--skip-git-repo-check",
					"-c", `developer_instructions="you're running under leo"`},
					ws, []string{"p"})
			},
		},
		{
			name: "empty system context omits developer_instructions",
			spec: harness.LaunchSpec{Kind: harness.KindTask, Prompt: "p", SystemContext: "", Options: Options{}},
			want: func(ws string) []string {
				return withRoots([]string{"exec", "--json", "--skip-git-repo-check"}, ws, []string{"p"})
			},
		},
		{
			name: "multi-line system context is toml-escaped",
			spec: harness.LaunchSpec{Kind: harness.KindTask, Prompt: "p", SystemContext: "line one\nline two\ttabbed",
				Options: Options{}},
			want: func(ws string) []string {
				return withRoots([]string{"exec", "--json", "--skip-git-repo-check",
					"-c", `developer_instructions="line one\nline two\ttabbed"`},
					ws, []string{"p"})
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := t.TempDir()
			spec := tt.spec
			spec.Workspace = ws
			got, err := Codex{}.Args(spec)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			want := tt.want(ws)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("got %v\nwant %v", got, want)
			}
		})
	}
}

// withRoots splices sandboxWritableRootsArgs(ws) between before and after,
// mirroring the insertion point Args() uses (after developer_instructions,
// before the LeoMCP bridge / session args / prompt).
func withRoots(before []string, ws string, after []string) []string {
	want := append([]string{}, before...)
	want = append(want, sandboxWritableRootsArgs(ws)...)
	return append(want, after...)
}

func TestArgsSessionKindsBuildTUIArgv(t *testing.T) {
	tests := []struct {
		name string
		spec harness.LaunchSpec
		want func(ws string) []string
	}{
		{
			name: "KindAgent TUI argv",
			spec: harness.LaunchSpec{Kind: harness.KindAgent, Model: "gpt-5.6-sol",
				Options: Options{PermissionMode: "workspace-write"}},
			want: func(ws string) []string {
				return withRoots([]string{"-a", "never", "--model", "gpt-5.6-sol", "--sandbox", "workspace-write"}, ws, nil)
			},
		},
		{
			name: "KindAgent TUI argv with MCP bridge",
			spec: harness.LaunchSpec{
				Kind: harness.KindAgent, Model: "gpt-5.3-codex",
				Options: Options{
					LeoMCP: &LeoMCPBridge{
						Command: "leo", Args: []string{"mcp-server"},
						EnvVars:      []string{"LEO_PROCESS_NAME"},
						ApprovalMode: "approve",
						ToolTimeout:  32 * time.Minute,
					},
				},
			},
			want: func(ws string) []string {
				return withRoots([]string{"-a", "never", "--model", "gpt-5.3-codex"}, ws, []string{
					"-c", `mcp_servers.leo.command="leo"`,
					"-c", `mcp_servers.leo.args=["mcp-server"]`,
					"-c", `mcp_servers.leo.env_vars=["LEO_PROCESS_NAME"]`,
					"-c", `mcp_servers.leo.default_tools_approval_mode="approve"`,
					"-c", `mcp_servers.leo.tool_timeout_sec=1920`})
			},
		},
		{
			// codex rejects --approve-for-me alongside either -a or -s, so
			// the TUI argv must drop the usual `-a never` pinning too.
			name: "KindAgent approve-for-me omits -a and --sandbox",
			spec: harness.LaunchSpec{Kind: harness.KindAgent, Model: "gpt-5.6-sol",
				Options: Options{PermissionMode: "approve-for-me"}},
			want: func(ws string) []string {
				return withRoots([]string{"--approve-for-me", "--model", "gpt-5.6-sol"}, ws, nil)
			},
		},
		{
			name: "KindAgent no model no permission_mode",
			spec: harness.LaunchSpec{Kind: harness.KindAgent, Options: Options{}},
			want: func(ws string) []string {
				return withRoots([]string{"-a", "never"}, ws, nil)
			},
		},
		{
			name: "KindAgent TUI argv with system context",
			spec: harness.LaunchSpec{Kind: harness.KindAgent, Model: "gpt-5.6-sol",
				SystemContext: "you're running under leo", Options: Options{}},
			want: func(ws string) []string {
				return withRoots([]string{"-a", "never", "--model", "gpt-5.6-sol",
					"-c", `developer_instructions="you're running under leo"`}, ws, nil)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := t.TempDir()
			spec := tt.spec
			spec.Workspace = ws
			got, err := Codex{}.Args(spec)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			want := tt.want(ws)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("got %#v\nwant %#v", got, want)
			}
			for _, tok := range got {
				if tok == "exec" || tok == "--json" || tok == "resume" {
					t.Errorf("TUI argv must not include %q: %#v", tok, got)
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
