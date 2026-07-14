package run

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

// leoSkillNudgeText mirrors leomcp's unconditional leo_skill guidance
// (unexported there), so characterization tests below can assert the exact
// --append-system-prompt value the leo MCP server injection produces when
// web is disabled (as in every case here — none of these configs set
// Web.Enabled).
const leoSkillNudgeText = "When you need to operate Leo — schedule or trigger tasks, read logs, or manage the daemon and agents — call the `leo_skill` tool for step-by-step instructions."

// leoMCPConfigPath is the leo MCP config path AppendArg always adds now,
// derived from the fixed HomePath used by every case in this table.
const leoMCPConfigPath = "/tmp/leo-home/state/leo-mcp.json"

func TestBuildArgsCharacterization(t *testing.T) {
	tests := []struct {
		name      string
		cfg       *config.Config
		task      config.TaskConfig
		prompt    string
		sessionID string
		leoEnv    map[string]string
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
				"--mcp-config", leoMCPConfigPath,
				"--add-dir", "/tmp/ws",
				"--append-system-prompt", leoSkillNudgeText,
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
				"--mcp-config", leoMCPConfigPath,
				"--add-dir", "/tmp/ws",
				"--allowed-tools", "Read,Bash",
				"--append-system-prompt", leoSkillNudgeText + "\n\nbe terse",
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
				"--mcp-config", leoMCPConfigPath,
				"--add-dir", "/tmp/ws",
				"--append-system-prompt", leoSkillNudgeText,
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
				"--mcp-config", leoMCPConfigPath,
				"--add-dir", "/tmp/ws",
				"--append-system-prompt", leoSkillNudgeText,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := buildArgs(tt.cfg, tt.task, "mytask", tt.prompt, tt.sessionID, tt.leoEnv)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("buildArgs argv mismatch\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}

// TestBuildArgsCodex covers the codex adapter path through buildArgs: sandbox
// option fill, no session, and no adapter-injected env of its own — the leo
// MCP bridge is always wired in now (env-var names only, no literal values),
// so it appears in argv even with a nil leoEnv.
func TestBuildArgsCodex(t *testing.T) {
	cfg := &config.Config{
		HomePath: "/tmp/leo-home",
		Defaults: config.DefaultsConfig{Harness: "codex"},
		Tasks: map[string]config.TaskConfig{
			"mytask": {
				Workspace:      "/tmp/ws",
				HarnessOptions: map[string]any{"sandbox": "workspace-write"},
			},
		},
	}
	task := cfg.Tasks["mytask"]

	args, env := buildArgs(cfg, task, "mytask", "do it", "", nil)
	joined := strings.Join(args, " ")
	// No task/defaults model override, so TaskModel falls through to the
	// built-in default ("sonnet") — the harness matches defaults.harness, so
	// the fall-through applies.
	for _, want := range []string{
		"exec --json --skip-git-repo-check --model sonnet --sandbox workspace-write",
		`-c mcp_servers.leo.command="leo"`,
		`developer_instructions="` + leoSkillNudgeText + `"`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv missing %q; got %v", want, args)
		}
	}
	if !strings.HasSuffix(joined, "do it") {
		t.Errorf("expected argv to end with the prompt; got %v", args)
	}
	if env != nil {
		t.Errorf("expected nil extraEnv (codex needs no adapter-injected env of its own), got %v", env)
	}
}

// TestBuildArgsCodexWithLeoMCP verifies the four `-c mcp_servers.leo.*`
// override pairs land in argv when leoEnv is non-nil.
func TestBuildArgsCodexWithLeoMCP(t *testing.T) {
	cfg := &config.Config{
		HomePath: "/tmp/leo-home",
		Defaults: config.DefaultsConfig{Harness: "codex"},
	}
	task := config.TaskConfig{Workspace: "/tmp/ws"}
	leoEnv := map[string]string{
		"LEO_PROCESS_NAME": "task:mytask",
		"LEO_WEB_PORT":     "8888",
		"LEO_API_TOKEN":    "tok",
	}

	args, _ := buildArgs(cfg, task, "mytask", "do it", "", leoEnv)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		`-c mcp_servers.leo.command="leo"`,
		`-c mcp_servers.leo.args=["mcp-server"]`,
		`-c mcp_servers.leo.env_vars=["LEO_PROCESS_NAME","LEO_WEB_PORT","LEO_API_TOKEN"]`,
		`-c mcp_servers.leo.default_tools_approval_mode="approve"`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv missing %q; got %v", want, args)
		}
	}
}

// TestBuildArgsOpencode covers the opencode adapter path through buildArgs:
// permission option fill and the leo MCP bridge landing in
// OPENCODE_CONFIG_CONTENT.
func TestBuildArgsOpencode(t *testing.T) {
	cfg := &config.Config{
		HomePath: "/tmp/leo-home",
		Defaults: config.DefaultsConfig{Harness: "opencode"},
		Tasks: map[string]config.TaskConfig{
			"mytask": {
				Workspace: "/tmp/ws",
				HarnessOptions: map[string]any{
					"permission": map[string]any{"bash": "ask"},
				},
			},
		},
	}
	task := cfg.Tasks["mytask"]
	leoEnv := map[string]string{
		"LEO_PROCESS_NAME": "task:mytask",
		"LEO_WEB_PORT":     "8888",
		"LEO_API_TOKEN":    "tok-abc",
	}

	args, env := buildArgs(cfg, task, "mytask", "do it", "", leoEnv)
	// No task/defaults model override, so TaskModel falls through to the
	// built-in default ("sonnet") — the harness matches defaults.harness, so
	// the fall-through applies.
	want := []string{"run", "--format", "json", "--model", "sonnet", "do it"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("buildArgs argv mismatch\n got: %q\nwant: %q", args, want)
	}

	content, ok := env["OPENCODE_CONFIG_CONTENT"]
	if !ok {
		t.Fatalf("expected OPENCODE_CONFIG_CONTENT in extraEnv, got %v", env)
	}
	var parsed struct {
		MCP struct {
			Leo struct {
				Environment map[string]string `json:"environment"`
			} `json:"leo"`
		} `json:"mcp"`
	}
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		t.Fatalf("unmarshaling OPENCODE_CONFIG_CONTENT: %v", err)
	}
	if parsed.MCP.Leo.Environment["LEO_API_TOKEN"] != "tok-abc" {
		t.Errorf("mcp.leo.environment.LEO_API_TOKEN = %q, want %q", parsed.MCP.Leo.Environment["LEO_API_TOKEN"], "tok-abc")
	}
}

// TestBuildArgsNonClaudeModelCascade pins Task 4's model cascade for
// codex/opencode: a claude-shaped defaults.model ("opus") must not leak into
// a task running a different harness, so argv carries no --model flag at
// all.
func TestBuildArgsNonClaudeModelCascade(t *testing.T) {
	for _, h := range []string{"codex", "opencode"} {
		t.Run(h, func(t *testing.T) {
			cfg := &config.Config{
				HomePath: "/tmp/leo-home",
				Defaults: config.DefaultsConfig{Model: "opus"},
			}
			task := config.TaskConfig{Workspace: "/tmp/ws", Harness: h}

			args, _ := buildArgs(cfg, task, "mytask", "do it", "", nil)
			for i, a := range args {
				if a == "--model" {
					t.Errorf("did not expect --model in argv (defaults.model is claude-shaped); got %v (at index %d)", args, i)
				}
			}
		})
	}
}
