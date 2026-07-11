package agent

import (
	"reflect"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/harness"
	codexharness "github.com/blackpaw-studio/leo/internal/harness/codex"
	opencodeharness "github.com/blackpaw-studio/leo/internal/harness/opencode"
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

	args := BuildTemplateArgs(cfg, tmpl, "agent-x", "/tmp/ws", "", "")

	if !hasFlagValue(args, "--mcp-config", "leo-mcp.json") {
		t.Errorf("expected --mcp-config pointing at leo-mcp.json; got %v", args)
	}
	if !hasFlagValue(args, "--append-system-prompt", "leo_send_message") {
		t.Errorf("expected awareness line in --append-system-prompt; got %v", args)
	}
}

func TestBuildTemplateArgsNoLeoMCPWhenWebDisabled(t *testing.T) {
	cfg := &config.Config{HomePath: t.TempDir(), Web: config.WebConfig{Enabled: false}}
	args := BuildTemplateArgs(cfg, config.TemplateConfig{}, "agent-x", "/tmp/ws", "", "")

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
			got := BuildTemplateArgs(tt.cfg, tt.tmpl, "myagent", "/tmp/ws", tt.prompt, "")
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

	// Args() now renders the turn-prefix argv for KindAgent (TurnDriver,
	// Plan 4 Task 5): the leo MCP bridge config lands in the prefix, ready
	// for TurnArgs to be appended per-turn.
	args, err := h.Args(spec)
	if err != nil {
		t.Fatalf("Args(): %v", err)
	}
	joined := strings.Join(args, "\x00")
	if !strings.Contains(joined, "mcp_servers.leo.command=\"leo\"") {
		t.Errorf("Args() = %v, want the leo MCP bridge config", args)
	}
}

// TestResolveTemplateLaunchCodexNoLeoMCPWhenWebDisabled mirrors the claude
// gate: web disabled means no leo MCP bridge for codex either.
func TestResolveTemplateLaunchCodexNoLeoMCPWhenWebDisabled(t *testing.T) {
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
	if opts.LeoMCP != nil {
		t.Errorf("expected nil LeoMCP bridge when web is disabled, got %+v", opts.LeoMCP)
	}
}

// TestResolveTemplateLaunchCodexNoLeoMCPWithoutToken confirms the gate
// requires a live token, not just web.Enabled: even though codex's bridge
// only references env-var *names* (no literal secret embedded), a bridge is
// useless without a token for the supervisor to actually export — matching
// processLeoMCPEnv's contract and the opencode branch in this same function.
func TestResolveTemplateLaunchCodexNoLeoMCPWithoutToken(t *testing.T) {
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
	if opts.LeoMCP != nil {
		t.Errorf("expected nil LeoMCP bridge without a webToken, got %+v", opts.LeoMCP)
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

	// Args() itself still errors — opencode has no session driver for agents yet.
	if _, err := h.Args(spec); err == nil {
		t.Error("expected opencode Args() to still refuse a KindAgent launch")
	}
}

// TestResolveTemplateLaunchOpencodeNoBridgeWithoutToken confirms the
// literal-value requirement: even with web enabled, an empty webToken must
// suppress the bridge rather than embed empty credentials.
func TestResolveTemplateLaunchOpencodeNoBridgeWithoutToken(t *testing.T) {
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
	if opts.LeoMCP != nil {
		t.Errorf("expected nil LeoMCP bridge without a webToken, got %+v", opts.LeoMCP)
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
