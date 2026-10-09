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
	if want := `export LEO_API_TOKEN="$(cat '` + tokenFile + `' 2>/dev/null)";`; !strings.Contains(got.command, want) {
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
	if strings.Contains(got.command, "export LEO_API_TOKEN") || !strings.Contains(got.command, "unset LEO_API_TOKEN;") {
		t.Errorf("a local-only child must have its token unset, not read: %q", got.command)
	}
	if v, ok := got.envFlag["LEO_WEB_PORT"]; !ok || v != "" {
		t.Errorf("web port = %q (set=%v) with web disabled, want an explicit empty value: %q", v, ok, got.argv)
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

func TestInteractiveChildBlanksAmbientLeoEnvItIsNotGiven(t *testing.T) {
	cfg := testConfig() // web disabled: local-only
	cfg.Templates = map[string]config.TemplateConfig{"claude": {Harness: "claude", Model: "sonnet", Env: map[string]string{"LEO_API_TOKEN": "template-token", "LEO_WEB_PORT": "8888", "LEO_PERMISSIONS": "{}"}}}
	got := launchInteractiveChild(t, cfg, "claude", writeTokenFile(t))
	// tmux's global environment would otherwise show through: each reserved
	// variable the child is not given is set empty explicitly.
	for _, k := range []string{"LEO_WEB_PORT", "LEO_PERMISSIONS"} {
		v, ok := got.envFlag[k]
		if !ok || v != "" {
			t.Errorf("tmux -e %s = %q (set=%v), want an explicit empty value", k, v, ok)
		}
	}
	if !strings.Contains(got.command, "unset LEO_API_TOKEN;") {
		t.Errorf("pane command does not clear an ambient token: %q", got.command)
	}
	for _, arg := range got.argv {
		if strings.Contains(arg, "template-token") {
			t.Fatalf("template-supplied token reached argv: %q", got.argv)
		}
	}
}

// stubBin writes an executable script into dir that records its argv (one
// argument per line) to <name>.argv, then runs exec.
func stubBin(t *testing.T, dir, name, exec string) {
	t.Helper()
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > '" + filepath.Join(dir, name+".argv") + "'\n" + exec + "\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestLaunchCommandNeverPutsTheTokenInAnyProcessArgv(t *testing.T) {
	tokenFile := writeTokenFile(t)
	bin := t.TempDir()
	// Shadow env so its own argv is observable, then hand over to the real one.
	stubBin(t, bin, "env", `exec /usr/bin/env "$@"`)
	stubBin(t, bin, "harness", `printf '%s' "$LEO_API_TOKEN" > '`+filepath.Join(bin, "harness.token")+`'`)
	command := strings.Join(launchCommandWords("d-child01", filepath.Join(bin, "harness"), []string{"--flag"}, tokenFile), " ")
	if strings.Contains(command, interactiveToken) {
		t.Fatalf("token inside the command string: %q", command)
	}
	cmd := exec.Command("sh", "-c", command)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "LEO_API_TOKEN=ambient-token")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run %q: %v: %s", command, err, out)
	}
	for _, name := range []string{"env.argv", "harness.argv"} {
		argv, err := os.ReadFile(filepath.Join(bin, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if strings.Contains(string(argv), interactiveToken) {
			t.Errorf("token reached %s: %q", name, argv)
		}
	}
	got, _ := os.ReadFile(filepath.Join(bin, "harness.token"))
	if string(got) != interactiveToken {
		t.Fatalf("harness saw LEO_API_TOKEN=%q, want the token file's contents", got)
	}
}

func TestLaunchCommandClearsAnAmbientTokenWithoutATokenFile(t *testing.T) {
	bin := t.TempDir()
	stubBin(t, bin, "harness", `printf '%s' "$LEO_API_TOKEN" > '`+filepath.Join(bin, "harness.token")+`'`)
	command := strings.Join(launchCommandWords("d-child01", filepath.Join(bin, "harness"), nil, ""), " ")
	cmd := exec.Command("sh", "-c", command)
	cmd.Env = append(os.Environ(), "LEO_API_TOKEN=ambient-token")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run: %v: %s", err, out)
	}
	if got, _ := os.ReadFile(filepath.Join(bin, "harness.token")); len(got) != 0 {
		t.Fatalf("ambient token survived into a local-only child: %q", got)
	}
}

func TestPaneLookupStillFindsTheDispatchIDInTheTokenCommand(t *testing.T) {
	command := strings.Join(launchCommandWords("d-child01", "/opt/claude", []string{"--model", "x"}, "/state/agent.token"), " ")
	if !startCommandHasDispatchID(command, "d-child01") || startCommandHasDispatchID(command, "d-other") {
		t.Fatalf("startCommandHasDispatchID mismatches %q", command)
	}
}
