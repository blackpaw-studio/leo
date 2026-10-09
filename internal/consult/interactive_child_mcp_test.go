package consult

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

const interactiveToken = "interactive-s3cr3t"

type interactiveLaunch struct {
	argv    []string // the full new-window tmux argv
	command string   // the pane command string (last argv element)
	envFlag map[string]string
}

// launchInteractiveChild launches one interactive child and returns the tmux
// argv exactly as it was exec'd.
func launchInteractiveChild(t *testing.T, cfg *config.Config, template, tokenFile string) interactiveLaunch {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	cfg.HomePath = dir
	r := NewInteractiveRuntime("/tmp/leo.yaml", func() (*config.Config, error) { return cfg, nil }, nil, "tmux", "/opt/leo")
	r.AgentTokenFile = func(*config.Config) string { return tokenFile }
	var launch []string
	r.ExecCommandContext = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		if slices.Contains(args, "new-window") {
			launch = args
			return exec.Command("echo", "%42")
		}
		return exec.Command("true")
	}
	if _, _, err := r.Launch(context.Background(), LaunchRequest{ID: "d-child01", Template: template, Cwd: dir, Name: "work", Dispatched: true}); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	flags := map[string]string{}
	for i, a := range launch {
		if a == "-e" && i+1 < len(launch) {
			k, v, _ := strings.Cut(launch[i+1], "=")
			flags[k] = v
		}
	}
	return interactiveLaunch{argv: launch, command: launch[len(launch)-1], envFlag: flags}
}

func writeTokenFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent.token")
	if err := os.WriteFile(path, []byte(interactiveToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertInteractiveMCPEnv(t *testing.T, got interactiveLaunch, tokenFile string) {
	t.Helper()
	for k, want := range map[string]string{"LEO_PROCESS_NAME": "dispatch:d-child01", "LEO_WEB_PORT": "9100", "LEO_DISPATCH_ID": "d-child01"} {
		if got.envFlag[k] != want {
			t.Errorf("tmux -e %s = %q, want %q (argv %q)", k, got.envFlag[k], want, got.argv)
		}
	}
	if _, leaked := got.envFlag["LEO_API_TOKEN"]; leaked {
		t.Errorf("LEO_API_TOKEN passed as a tmux -e flag: %q", got.argv)
	}
	for _, arg := range got.argv {
		if strings.Contains(arg, interactiveToken) {
			t.Fatalf("API token leaked into tmux argv: %q", got.argv)
		}
	}
	if want := `LEO_API_TOKEN="$(cat '` + tokenFile + `' 2>/dev/null)"`; !strings.Contains(got.command, want) {
		t.Errorf("pane command does not read the token file (%s): %q", want, got.command)
	}
}

func TestInteractiveClaudeChildGetsDaemonBackedLeoMCPEnvWithoutTokenInArgv(t *testing.T) {
	tokenFile := writeTokenFile(t)
	cfg := childMCPConfig()
	cfg.Templates = map[string]config.TemplateConfig{"claude": {Harness: "claude", Model: "sonnet"}}
	got := launchInteractiveChild(t, cfg, "claude", tokenFile)
	assertInteractiveMCPEnv(t, got, tokenFile)
	if !strings.Contains(got.command, "mcp-server") {
		t.Errorf("claude child has no leo MCP server: %q", got.command)
	}
}

func TestInteractiveCodexChildGetsLeoMCPBridgeAndTokenFreeArgv(t *testing.T) {
	tokenFile := writeTokenFile(t)
	cfg := childMCPConfig()
	cfg.Templates = map[string]config.TemplateConfig{"codex": {Harness: "codex", Model: "gpt-5.3-codex"}}
	got := launchInteractiveChild(t, cfg, "codex", tokenFile)
	assertInteractiveMCPEnv(t, got, tokenFile)
	for _, want := range []string{
		"mcp_servers.leo.command=",
		`mcp_servers.leo.env_vars=["LEO_PROCESS_NAME","LEO_WEB_PORT","LEO_API_TOKEN","LEO_DISPATCH_ID"]`,
		"features.multi_agent=false",
	} {
		if !strings.Contains(got.command, want) {
			t.Errorf("codex pane command missing %q: %q", want, got.command)
		}
	}
}

func TestInteractiveChildStaysLocalOnlyWithoutADaemonListener(t *testing.T) {
	cfg := testConfig()
	cfg.Templates = map[string]config.TemplateConfig{"claude": {Harness: "claude", Model: "sonnet"}}
	got := launchInteractiveChild(t, cfg, "claude", writeTokenFile(t)) // web disabled
	if strings.Contains(got.command, "LEO_API_TOKEN") {
		t.Errorf("token expression present with web disabled: %q", got.command)
	}
	if _, ok := got.envFlag["LEO_WEB_PORT"]; ok {
		t.Errorf("web port set with web disabled: %q", got.argv)
	}
}

func TestLaunchCommandWordsExpandTheTokenFromItsFileWithoutPuttingItInTheCommand(t *testing.T) {
	tokenFile := writeTokenFile(t)
	words := launchCommandWords("d-child01", "/usr/bin/printenv", []string{"LEO_API_TOKEN"}, tokenFile)
	command := strings.Join(words, " ")
	if strings.Contains(command, interactiveToken) {
		t.Fatalf("token inside the command string: %q", command)
	}
	out, err := exec.Command("sh", "-c", command).Output()
	if err != nil {
		t.Fatalf("run %q: %v", command, err)
	}
	if got := strings.TrimSpace(string(out)); got != interactiveToken {
		t.Fatalf("child saw LEO_API_TOKEN=%q, want the file contents", got)
	}
	// A missing file degrades to an empty token (local-only), not a failed launch.
	words = launchCommandWords("d-child01", "/usr/bin/printenv", []string{"LEO_API_TOKEN"}, filepath.Join(t.TempDir(), "missing"))
	out, err = exec.Command("sh", "-c", strings.Join(words, " ")).Output()
	if err != nil || strings.TrimSpace(string(out)) != "" {
		t.Fatalf("missing token file: out=%q err=%v", out, err)
	}
}
