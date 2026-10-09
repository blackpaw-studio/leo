package consult

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
)

const childToken = "s3cr3t-agent-token"

func childMCPConfig() *config.Config {
	cfg := testConfig()
	cfg.HomePath = os.TempDir()
	cfg.Web = config.WebConfig{Enabled: true, Port: 9100}
	return cfg
}

// runHeadlessChild starts a headless dispatch and returns the argv and
// process environment the harness child was launched with.
func runHeadlessChild(t *testing.T, cfg *config.Config, template string, token string) (id string, args []string, env map[string]string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	for _, k := range []string{"LEO_API_TOKEN", "LEO_WEB_PORT", "LEO_PROCESS_NAME", "LEO_DISPATCH_ID"} {
		t.Setenv(k, "")
	}
	var captured *exec.Cmd
	d := NewDispatcher(nil)
	d.AgentToken = token
	d.ExecCommandContext = func(ctx context.Context, _ string, a ...string) *exec.Cmd {
		args = a
		captured = exec.CommandContext(ctx, "sh", "-c", `echo '{"type":"result","result":"ok","is_error":false}'`)
		return captured
	}
	started, err := d.Start(context.Background(), cfg, Request{Template: template, Prompt: "q", Cwd: t.TempDir(), Kind: "dispatch"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	d.Wait(context.Background(), []string{started.ID}, 3*time.Second)
	env = map[string]string{}
	for _, entry := range captured.Env {
		if k, v, ok := strings.Cut(entry, "="); ok {
			env[k] = v
		}
	}
	return started.ID, args, env
}

func assertChildMCPEnv(t *testing.T, id string, env map[string]string) {
	t.Helper()
	want := map[string]string{
		"LEO_PROCESS_NAME": "dispatch:" + id,
		"LEO_DISPATCH_ID":  id,
		"LEO_WEB_PORT":     "9100",
		"LEO_API_TOKEN":    childToken,
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
}

func assertTokenNotInArgv(t *testing.T, args []string) {
	t.Helper()
	if strings.Contains(strings.Join(args, "\x00"), childToken) {
		t.Fatalf("API token leaked into argv: %q", args)
	}
}

func TestHeadlessClaudeChildGetsDaemonBackedLeoMCPEnv(t *testing.T) {
	id, args, env := runHeadlessChild(t, childMCPConfig(), "claude", childToken)
	assertChildMCPEnv(t, id, env)
	assertTokenNotInArgv(t, args)
	if !strings.Contains(strings.Join(args, " "), "mcp-server") {
		t.Fatalf("claude child has no leo MCP server in argv: %q", args)
	}
}

func TestHeadlessCodexChildGetsLeoMCPBridgeAndEnv(t *testing.T) {
	id, args, env := runHeadlessChild(t, childMCPConfig(), "codex", childToken)
	assertChildMCPEnv(t, id, env)
	assertTokenNotInArgv(t, args)
	joined := strings.Join(args, "\x00")
	for _, want := range []string{
		"mcp_servers.leo.command=",
		`mcp_servers.leo.env_vars=["LEO_PROCESS_NAME","LEO_WEB_PORT","LEO_API_TOKEN","LEO_DISPATCH_ID"]`,
		"features.multi_agent=false",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("codex argv missing %q: %q", want, args)
		}
	}
}

func TestHeadlessOpencodeChildGetsLeoMCPBridgeAndKeepsTaskDenied(t *testing.T) {
	id, args, env := runHeadlessChild(t, childMCPConfig(), "opencode", childToken)
	assertTokenNotInArgv(t, args)
	var cfg struct {
		MCP struct {
			Leo struct {
				Environment map[string]string `json:"environment"`
			} `json:"leo"`
		} `json:"mcp"`
		Permission map[string]any `json:"permission"`
	}
	if err := json.Unmarshal([]byte(env["OPENCODE_CONFIG_CONTENT"]), &cfg); err != nil {
		t.Fatalf("OPENCODE_CONFIG_CONTENT: %v", err)
	}
	assertChildMCPEnv(t, id, cfg.MCP.Leo.Environment)
	if cfg.Permission["task"] != "deny" {
		t.Fatalf("task permission = %v, want deny", cfg.Permission["task"])
	}
}

func TestHeadlessChildStaysLocalOnlyWhenDaemonListenerUnavailable(t *testing.T) {
	disabled := testConfig() // web not enabled
	for _, tc := range []struct {
		name  string
		cfg   *config.Config
		token string
	}{
		{"web disabled", disabled, childToken},
		{"no token", childMCPConfig(), ""},
	} {
		for _, template := range []string{"claude", "codex", "opencode"} {
			id, args, env := runHeadlessChild(t, tc.cfg, template, tc.token)
			if env["LEO_API_TOKEN"] != "" || env["LEO_WEB_PORT"] != "" {
				t.Errorf("%s/%s: daemon creds present: token=%q port=%q", tc.name, template, env["LEO_API_TOKEN"], env["LEO_WEB_PORT"])
			}
			if env["LEO_DISPATCH_ID"] != id {
				t.Errorf("%s/%s: dispatch id = %q", tc.name, template, env["LEO_DISPATCH_ID"])
			}
			assertTokenNotInArgv(t, args)
		}
	}
}
