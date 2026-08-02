package agent

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/harness"
	codexharness "github.com/blackpaw-studio/leo/internal/harness/codex"
	opencodeharness "github.com/blackpaw-studio/leo/internal/harness/opencode"
	"github.com/blackpaw-studio/leo/internal/leomcp"
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

	args, env := BuildTemplateArgs(cfg, tmpl, "agent-x", "/tmp/ws", "", "")

	if !hasFlagValue(args, "--mcp-config", "leo-mcp.json") {
		t.Errorf("expected --mcp-config pointing at leo-mcp.json; got %v", args)
	}
	if !hasFlagValue(args, "--append-system-prompt", "leo_send_message") {
		t.Errorf("expected awareness line in --append-system-prompt; got %v", args)
	}
	// Claude Code's per-tool MCP deadline is process-global; a wired leo
	// bridge raises it past leo's own consult deadline.
	wantTimeout := strconv.FormatInt(leomcp.ToolTimeout.Milliseconds(), 10)
	if env["MCP_TOOL_TIMEOUT"] != wantTimeout {
		t.Errorf("MCP_TOOL_TIMEOUT = %q, want %q (env %v)", env["MCP_TOOL_TIMEOUT"], wantTimeout, env)
	}
}

func TestBuildTemplateArgsLeoMCPAlwaysWiredWhenWebDisabled(t *testing.T) {
	// The leo MCP server is now always wired in — it self-selects
	// local-only mode from its env when web is disabled — so --mcp-config
	// and the leo_skill nudge still appear, but the messaging nudge (which
	// requires the daemon's web listener) does not.
	cfg := &config.Config{HomePath: t.TempDir(), Web: config.WebConfig{Enabled: false}}
	args, _ := BuildTemplateArgs(cfg, config.TemplateConfig{}, "agent-x", "/tmp/ws", "", "")

	if !hasFlagValue(args, "--mcp-config", "leo-mcp.json") {
		t.Errorf("expected --mcp-config pointing at leo-mcp.json even when web disabled; got %v", args)
	}
	if !hasFlagValue(args, "--append-system-prompt", "leo_skill") {
		t.Errorf("expected leo_skill nudge even when web disabled; got %v", args)
	}
	if hasFlagValue(args, "--append-system-prompt", "leo_send_message") {
		t.Errorf("messaging nudge must not appear when web disabled; got %v", args)
	}
}

// leoSkillNudgeText mirrors leomcp's unconditional leo_skill guidance
// (unexported there), so characterization tests below can assert the exact
// --append-system-prompt value the leo MCP server injection produces when
// web is disabled (as in every case here — none of these configs set
// Web.Enabled).
const leoSkillNudgeText = "When you need to operate Leo — schedule or trigger tasks, read logs, or manage the daemon and agents — call the `leo_skill` tool for step-by-step instructions."

// leoMCPConfigPath is the leo MCP config path AppendArg always adds now,
// derived from the fixed HomePath used by every case in this table.
const leoMCPConfigPath = "/tmp/leo-home/state/leo-mcp.json"

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
				"--append-system-prompt", leoSkillNudgeText,
				"--mcp-config", leoMCPConfigPath,
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
				"--append-system-prompt", leoSkillNudgeText + "\n\nbe terse",
				"--mcp-config", leoMCPConfigPath,
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
				"--append-system-prompt", leoSkillNudgeText,
				"--mcp-config", leoMCPConfigPath,
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
				"--append-system-prompt", leoSkillNudgeText,
				"--mcp-config", leoMCPConfigPath,
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
				"--append-system-prompt", leoSkillNudgeText,
				"--mcp-config", leoMCPConfigPath,
				"--max-turns", "15",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := BuildTemplateArgs(tt.cfg, tt.tmpl, "myagent", "/tmp/ws", tt.prompt, "")
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("BuildTemplateArgs argv mismatch\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}

// TestResolveTemplateLaunchCodexFillsLeoMCPBridge locks the type-switch's
// codex branch: when the leo MCP gate passes (web enabled), the codex
// options carry a LeoMCP bridge referencing the env-var *names* the
// supervisor already exports — no literal token needed.
func TestResolveTemplateLaunchCodexFillsLeoMCPBridge(t *testing.T) {
	cfg := &config.Config{
		HomePath: t.TempDir(),
		Web:      config.WebConfig{Enabled: true},
		Defaults: config.DefaultsConfig{Harness: "codex"},
	}
	tmpl := config.TemplateConfig{}

	h, spec, err := resolveTemplateLaunch(cfg, tmpl, "agent-x", "/tmp/ws", "", "tok")
	if err != nil {
		t.Fatalf("resolveTemplateLaunch: %v", err)
	}
	if h.Name() != "codex" {
		t.Fatalf("resolved harness = %q, want codex", h.Name())
	}
	opts, ok := spec.Options.(codexharness.Options)
	if !ok {
		t.Fatalf("spec.Options = %T, want codexharness.Options", spec.Options)
	}
	if opts.LeoMCP == nil {
		t.Fatal("expected LeoMCP bridge to be filled when web is enabled")
	}
	if opts.LeoMCP.Command != "leo" {
		t.Errorf("LeoMCP.Command = %q, want leo", opts.LeoMCP.Command)
	}
	wantEnvVars := []string{"LEO_PROCESS_NAME", "LEO_WEB_PORT", "LEO_API_TOKEN"}
	if !reflect.DeepEqual(opts.LeoMCP.EnvVars, wantEnvVars) {
		t.Errorf("LeoMCP.EnvVars = %v, want %v", opts.LeoMCP.EnvVars, wantEnvVars)
	}
	if opts.LeoMCP.ApprovalMode != "approve" {
		t.Errorf("LeoMCP.ApprovalMode = %q, want approve", opts.LeoMCP.ApprovalMode)
	}
	if opts.LeoMCP.ToolTimeout != leomcp.ToolTimeout {
		t.Errorf("LeoMCP.ToolTimeout = %s, want %s", opts.LeoMCP.ToolTimeout, leomcp.ToolTimeout)
	}

	// Args() renders the codex TUI launch argv for KindAgent: the leo MCP
	// bridge config lands in the launch argv via -c mcp_servers.leo.*
	// overrides, same as the headless exec path.
	args, err := h.Args(spec)
	if err != nil {
		t.Fatalf("Args(): %v", err)
	}
	joined := strings.Join(args, "\x00")
	if !strings.Contains(joined, "mcp_servers.leo.command=\"leo\"") {
		t.Errorf("Args() = %v, want the leo MCP bridge config", args)
	}
}

// TestResolveTemplateLaunchCodexLeoMCPWhenWebDisabled confirms the codex
// bridge is always wired in now, even with web disabled — the env-var names
// are always referenced; the actual values (LEO_WEB_PORT/LEO_API_TOKEN) are
// simply absent/empty at spawn time, and the leo MCP server self-selects
// local-only mode from that.
func TestResolveTemplateLaunchCodexLeoMCPWhenWebDisabled(t *testing.T) {
	cfg := &config.Config{
		HomePath: t.TempDir(),
		Web:      config.WebConfig{Enabled: false},
		Defaults: config.DefaultsConfig{Harness: "codex"},
	}
	_, spec, err := resolveTemplateLaunch(cfg, config.TemplateConfig{}, "agent-x", "/tmp/ws", "", "tok")
	if err != nil {
		t.Fatalf("resolveTemplateLaunch: %v", err)
	}
	opts, ok := spec.Options.(codexharness.Options)
	if !ok {
		t.Fatalf("spec.Options = %T, want codexharness.Options", spec.Options)
	}
	if opts.LeoMCP == nil {
		t.Fatal("expected LeoMCP bridge to be filled even when web is disabled")
	}
}

// TestResolveTemplateLaunchCodexLeoMCPWithoutToken confirms the codex bridge
// is wired in even without a live token: codex's bridge only references
// env-var *names* (no literal secret embedded), so an empty token at spawn
// time is harmless — the MCP server just runs local-only.
func TestResolveTemplateLaunchCodexLeoMCPWithoutToken(t *testing.T) {
	cfg := &config.Config{
		HomePath: t.TempDir(),
		Web:      config.WebConfig{Enabled: true},
		Defaults: config.DefaultsConfig{Harness: "codex"},
	}
	_, spec, err := resolveTemplateLaunch(cfg, config.TemplateConfig{}, "agent-x", "/tmp/ws", "", "")
	if err != nil {
		t.Fatalf("resolveTemplateLaunch: %v", err)
	}
	opts, ok := spec.Options.(codexharness.Options)
	if !ok {
		t.Fatalf("spec.Options = %T, want codexharness.Options", spec.Options)
	}
	if opts.LeoMCP == nil {
		t.Fatal("expected LeoMCP bridge to be filled even without a webToken")
	}
}

// TestResolveTemplateLaunchOpencodeFillsLeoMCPBridge locks the opencode
// branch: unlike codex's env-var-name whitelist, opencode's bridge needs the
// literal LEO_* values inline (OPENCODE_CONFIG_CONTENT has no notion of
// "read this from the parent env"), so it only fires when a non-empty
// webToken is available.
func TestResolveTemplateLaunchOpencodeFillsLeoMCPBridge(t *testing.T) {
	cfg := &config.Config{
		HomePath: t.TempDir(),
		Web:      config.WebConfig{Enabled: true, Port: 4141},
		Defaults: config.DefaultsConfig{Harness: "opencode"},
	}
	h, spec, err := resolveTemplateLaunch(cfg, config.TemplateConfig{}, "agent-x", "/tmp/ws", "", "sekrit-token")
	if err != nil {
		t.Fatalf("resolveTemplateLaunch: %v", err)
	}
	if h.Name() != "opencode" {
		t.Fatalf("resolved harness = %q, want opencode", h.Name())
	}
	opts, ok := spec.Options.(opencodeharness.Options)
	if !ok {
		t.Fatalf("spec.Options = %T, want opencodeharness.Options", spec.Options)
	}
	if opts.LeoMCP == nil {
		t.Fatal("expected LeoMCP bridge to be filled when web is enabled and a token is available")
	}
	wantEnv := map[string]string{
		"LEO_PROCESS_NAME": "agent-x",
		"LEO_WEB_PORT":     "4141",
		"LEO_API_TOKEN":    "sekrit-token",
	}
	if !reflect.DeepEqual(opts.LeoMCP.Env, wantEnv) {
		t.Errorf("LeoMCP.Env = %v, want %v", opts.LeoMCP.Env, wantEnv)
	}
	if opts.LeoMCP.ToolTimeout != leomcp.ToolTimeout {
		t.Errorf("LeoMCP.ToolTimeout = %s, want %s", opts.LeoMCP.ToolTimeout, leomcp.ToolTimeout)
	}

	// Args() renders the interactive TUI argv for KindAgent (tmuxtui driver).
	// spec.Model falls back to cfg.TemplateModel's built-in default ("sonnet")
	// since the template config here sets none.
	args, err := h.Args(spec)
	if err != nil {
		t.Fatalf("Args(): %v", err)
	}
	want := []string{"--model", spec.Model}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("Args() = %#v, want %#v", args, want)
	}
}

// TestResolveTemplateLaunchOpencodeBridgeWithEmptyTokenWhenTokenEmpty
// confirms the opencode bridge is now always wired in, even with an empty
// webToken — the leo MCP server self-selects local-only mode from the
// (empty) LEO_API_TOKEN value rather than the bridge being suppressed.
func TestResolveTemplateLaunchOpencodeBridgeWithEmptyTokenWhenTokenEmpty(t *testing.T) {
	cfg := &config.Config{
		HomePath: t.TempDir(),
		Web:      config.WebConfig{Enabled: true},
		Defaults: config.DefaultsConfig{Harness: "opencode"},
	}
	_, spec, err := resolveTemplateLaunch(cfg, config.TemplateConfig{}, "agent-x", "/tmp/ws", "", "")
	if err != nil {
		t.Fatalf("resolveTemplateLaunch: %v", err)
	}
	opts, ok := spec.Options.(opencodeharness.Options)
	if !ok {
		t.Fatalf("spec.Options = %T, want opencodeharness.Options", spec.Options)
	}
	if opts.LeoMCP == nil {
		t.Fatal("expected LeoMCP bridge to be filled even without a webToken")
	}
	if opts.LeoMCP.Env["LEO_API_TOKEN"] != "" {
		t.Errorf("expected empty LEO_API_TOKEN, got %q", opts.LeoMCP.Env["LEO_API_TOKEN"])
	}
}

// TestResolveTemplateLaunchKindIsAgent locks that templates always resolve
// KindAgent, regardless of harness — codex/opencode's Args() rejection is
// keyed off this.
func TestResolveTemplateLaunchKindIsAgent(t *testing.T) {
	cfg := &config.Config{HomePath: t.TempDir()}
	_, spec, err := resolveTemplateLaunch(cfg, config.TemplateConfig{}, "agent-x", "/tmp/ws", "", "")
	if err != nil {
		t.Fatalf("resolveTemplateLaunch: %v", err)
	}
	if spec.Kind != harness.KindAgent {
		t.Errorf("spec.Kind = %q, want %q", spec.Kind, harness.KindAgent)
	}
}
