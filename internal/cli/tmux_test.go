package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

// newProcessCLITestConfig is the process-side cousin of newAgentCLITestConfig:
// a single remote host + a single configured process so `leo process attach`
// has something to dispatch against.
func newProcessCLITestConfig(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	cfg := &config.Config{
		HomePath: home,
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 10},
		Client: config.ClientConfig{
			DefaultHost: "prod",
			Hosts: map[string]config.HostConfig{
				"prod": {SSH: "user@prod.example.com", SSHArgs: []string{"-p", "2222"}},
			},
		},
		Processes: map[string]config.ProcessConfig{
			"primary": {Enabled: true, Model: "sonnet"},
		},
	}
	path := home + "/leo.yaml"
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return path
}

func TestProcessAttachRemoteUsesTmuxDirectly(t *testing.T) {
	path := newProcessCLITestConfig(t)
	stub := withStubExec(t)
	withStubStdio(t)

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "process", "attach", "primary"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("expected 1 ssh call, got %d", len(stub.calls))
	}
	want := []string{"ssh", "-t", "user@prod.example.com", "-p", "2222", config.DefaultRemoteTmuxPath, "-L", "leo", "attach", "-t", "leo-primary"}
	if !equalStrings(stub.calls[0], want) {
		t.Errorf("ssh attach args = %v, want %v", stub.calls[0], want)
	}
}

func TestProcessAttachRemoteHonorsTmuxPathOverride(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{
		HomePath: home,
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 10},
		Client: config.ClientConfig{
			DefaultHost: "prod",
			Hosts: map[string]config.HostConfig{
				"prod": {
					SSH:      "user@prod.example.com",
					TmuxPath: "/opt/homebrew/bin/tmux",
				},
			},
		},
		Processes: map[string]config.ProcessConfig{
			"primary": {Enabled: true, Model: "sonnet"},
		},
	}
	path := home + "/leo.yaml"
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	stub := withStubExec(t)
	withStubStdio(t)

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "process", "attach", "primary"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	want := []string{"ssh", "-t", "user@prod.example.com", "/opt/homebrew/bin/tmux", "-L", "leo", "attach", "-t", "leo-primary"}
	if !equalStrings(stub.calls[0], want) {
		t.Errorf("ssh args = %v, want %v", stub.calls[0], want)
	}
}

func TestProcessLogsFollowRemoteUsesTmuxPath(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{
		HomePath: home,
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 10},
		Client: config.ClientConfig{
			DefaultHost: "prod",
			Hosts: map[string]config.HostConfig{
				"prod": {
					SSH:      "user@prod.example.com",
					TmuxPath: "/opt/homebrew/bin/tmux",
				},
			},
		},
		Processes: map[string]config.ProcessConfig{
			"primary": {Enabled: true, Model: "sonnet"},
		},
	}
	path := home + "/leo.yaml"
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	stub := withStubExec(t)
	withStubStdio(t)

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "process", "logs", "primary", "--follow"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	joined := strings.Join(stub.calls[0], " ")
	if !strings.Contains(joined, "/opt/homebrew/bin/tmux -L leo capture-pane") {
		t.Errorf("remote tail cmd missing tmux path: %s", joined)
	}
	if !strings.Contains(joined, "/opt/homebrew/bin/tmux -L leo pipe-pane") {
		t.Errorf("remote tail cmd missing tmux path in pipe-pane: %s", joined)
	}
	if !strings.Contains(joined, "leo-primary") {
		t.Errorf("remote tail cmd missing session name leo-primary: %s", joined)
	}
}

func TestProcessLogsRemoteCapturesPane(t *testing.T) {
	path := newProcessCLITestConfig(t)
	stub := withStubExec(t)
	withStubStdio(t)

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "process", "logs", "primary", "-n", "50"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("expected 1 ssh call, got %d", len(stub.calls))
	}
	want := []string{"ssh", "user@prod.example.com", "-p", "2222", config.DefaultRemoteTmuxPath, "-L", "leo", "capture-pane", "-t", "leo-primary", "-p", "-S", "-50"}
	if !equalStrings(stub.calls[0], want) {
		t.Errorf("ssh capture args = %v, want %v", stub.calls[0], want)
	}
}

// --- top-level `leo attach` alias ---

func newAttachAliasTestConfig(t *testing.T, processes map[string]config.ProcessConfig) string {
	t.Helper()
	home := t.TempDir()
	cfg := &config.Config{
		HomePath:  home,
		Defaults:  config.DefaultsConfig{Model: "sonnet", MaxTurns: 10},
		Processes: processes,
	}
	path := home + "/leo.yaml"
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return path
}

// TestAttachAliasRemoteDelegatesToServer asserts that when --host points to a
// remote, the alias shells `ssh -t <host> <leo_path> attach <name>` so the
// server does the process-vs-agent resolution.
func TestAttachAliasRemoteDelegatesToServer(t *testing.T) {
	path := newAgentCLITestConfig(t) // remote host, no local processes
	stub := withStubExec(t)
	withStubStdio(t)

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "attach", "whatever"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("expected 1 ssh call, got %d", len(stub.calls))
	}
	want := []string{"ssh", "-t", "user@prod.example.com", "-p", "2222", config.DefaultRemoteLeoPath, "attach", "whatever"}
	if !equalStrings(stub.calls[0], want) {
		t.Errorf("ssh args = %v, want %v", stub.calls[0], want)
	}
}

// TestAttachAliasResolvesToProcess covers the localhost-only happy path where
// the name matches a configured process but not a running agent. The daemon
// socket isn't running in tests, so AgentSession returns an error and we
// fall through to the process branch.
func TestAttachAliasResolvesToProcess(t *testing.T) {
	path := newAttachAliasTestConfig(t, map[string]config.ProcessConfig{
		"primary": {Enabled: true, Model: "sonnet"},
	})
	stub := withStubExec(t)
	withStubStdio(t)
	stubAgentSession(t, func(workDir, name string) (string, error) {
		return "", fmt.Errorf("not found")
	})

	// Force localhost so we exercise the resolution branch.
	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "attach", "primary", "--host", "localhost"})
	// attach hits exec.LookPath + syscall.Exec — stub both so the test runs
	// without real tmux on the runner and doesn't replace the test process.
	stubTmuxLookPath(t, "/usr/bin/tmux", nil)
	stubOutsideTmux(t)
	oldExec := agentSyscallExec
	var execed bool
	agentSyscallExec = func(argv0 string, argv []string, envv []string) error {
		execed = true
		return nil
	}
	t.Cleanup(func() { agentSyscallExec = oldExec })

	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !execed {
		t.Fatalf("expected syscall.Exec to be called for process attach; stub.calls = %v", stub.calls)
	}
}

// stubTmuxLookPath replaces tmuxLocate for the test so local-attach paths
// don't require a real tmux binary on the runner.
func stubTmuxLookPath(t *testing.T, path string, err error) {
	t.Helper()
	old := tmuxLocate
	tmuxLocate = func() (string, error) { return path, err }
	t.Cleanup(func() { tmuxLocate = old })
}

// stubOutsideTmux forces the $TMUX env probe to report "not inside tmux" so
// local-attach tests exercise the syscall.Exec path even when the developer
// is running tests from inside an interactive tmux session.
func stubOutsideTmux(t *testing.T) {
	t.Helper()
	old := tmuxEnv
	tmuxEnv = func() string { return "" }
	t.Cleanup(func() { tmuxEnv = old })
}

// stubAgentSession replaces lookupAgentSession for the duration of the test.
// Pass a function that returns (session, err) for a given name.
func stubAgentSession(t *testing.T, fn func(workDir, name string) (string, error)) {
	t.Helper()
	old := lookupAgentSession
	lookupAgentSession = fn
	t.Cleanup(func() { lookupAgentSession = old })
}

// TestAttachAliasResolvesToAgent exercises the "name matches an agent but not a
// process" branch. We stub the daemon lookup so it reports a live session.
func TestAttachAliasResolvesToAgent(t *testing.T) {
	path := newAttachAliasTestConfig(t, nil)
	stub := withStubExec(t)
	withStubStdio(t)
	stubAgentSession(t, func(workDir, name string) (string, error) {
		if name == "scratch" {
			return "leo-scratch", nil
		}
		return "", fmt.Errorf("not found")
	})

	// Stub exec.LookPath + syscall.Exec so the local attach works on runners
	// without real tmux and we can capture the resolved argv.
	stubTmuxLookPath(t, "/usr/bin/tmux", nil)
	stubOutsideTmux(t)
	var execed bool
	var execedArgv []string
	oldExec := agentSyscallExec
	agentSyscallExec = func(argv0 string, argv []string, envv []string) error {
		execed = true
		execedArgv = argv
		return nil
	}
	t.Cleanup(func() { agentSyscallExec = oldExec })

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "attach", "scratch", "--host", "localhost"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !execed {
		t.Fatalf("expected syscall.Exec for agent attach; ssh calls = %v", stub.calls)
	}
	// argv is ["tmux", "-L", "leo", "attach", "-t", "leo-scratch"]
	if len(execedArgv) != 6 || execedArgv[5] != "leo-scratch" {
		t.Errorf("unexpected tmux argv: %v", execedArgv)
	}
}

// TestAttachAliasCollisionErrors verifies the error path when the same name
// appears in both cfg.Processes AND the running agent set.
func TestAttachAliasCollisionErrors(t *testing.T) {
	path := newAttachAliasTestConfig(t, map[string]config.ProcessConfig{
		"twin": {Enabled: true, Model: "sonnet"},
	})
	withStubExec(t)
	withStubStdio(t)
	stubAgentSession(t, func(workDir, name string) (string, error) {
		if name == "twin" {
			return "leo-twin", nil
		}
		return "", fmt.Errorf("not found")
	})

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "attach", "twin", "--host", "localhost"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected collision error")
	}
	if !strings.Contains(err.Error(), "both a process and an agent") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestAttachAliasMissingReturnsError verifies that when a name matches neither
// a configured process nor a known agent, the user gets a friendly error
// instead of a silent misfire.
func TestAttachAliasMissingReturnsError(t *testing.T) {
	path := newAttachAliasTestConfig(t, map[string]config.ProcessConfig{
		"primary": {Enabled: true, Model: "sonnet"},
	})
	withStubExec(t)
	withStubStdio(t)
	stubAgentSession(t, func(workDir, name string) (string, error) {
		return "", fmt.Errorf("not found")
	})

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "attach", "nope", "--host", "localhost"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected error for unknown name")
	}
	if !strings.Contains(err.Error(), `no process or agent named "nope"`) {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestShellQuoteArg(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"simple", "'simple'"},
		{"/usr/bin/tmux", "'/usr/bin/tmux'"},
		{"leo-my-session", "'leo-my-session'"},
		{"", "''"},
		// Single-quote escape: the value breaks out of the outer quotes,
		// inserts a literal quote, then re-opens quoting.
		{"it's", `'it'\''s'`},
		{"a'b'c", `'a'\''b'\''c'`},
		// Leading/trailing quotes get escaped the same way.
		{"'edge", `''\''edge'`},
	}
	for _, c := range cases {
		if got := shellQuoteArg(c.in); got != c.want {
			t.Errorf("shellQuoteArg(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The --cc (tmux control mode) flag must refuse to run over SSH — the remote
// attach path doesn't route through a local tmux client and would end up
// garbling the user's terminal.
func TestAttachTmuxSessionCCRefusesRemote(t *testing.T) {
	res := config.HostResolution{
		Name: "prod",
		Host: config.HostConfig{SSH: "user@prod.example.com"},
	}
	err := attachTmuxSession(res, "leo-primary", attachOptions{cc: true})
	if err == nil || !strings.Contains(err.Error(), "local-only") {
		t.Fatalf("want local-only refusal, got %v", err)
	}
}

// --cc inside an existing tmux session is nonsensical — tmux control mode
// wants to take over the terminal, but the outer tmux already owns it.
func TestAttachTmuxSessionCCRefusesInsideTmux(t *testing.T) {
	stubTmuxLookPath(t, "/usr/bin/tmux", nil)
	old := tmuxEnv
	tmuxEnv = func() string { return "/tmp/tmux-501/default,1234,0" }
	t.Cleanup(func() { tmuxEnv = old })

	err := attachTmuxSession(config.HostResolution{Localhost: true}, "leo-primary", attachOptions{cc: true})
	if err == nil || !strings.Contains(err.Error(), "non-tmux terminal") {
		t.Fatalf("want inside-tmux refusal, got %v", err)
	}
}

// When launching from inside a user tmux session, the local attach should use
// `display-popup -E` so the overlay runs on the outer tmux server while still
// attaching to the leo-socket session. Verify the tmux invocation shape.
func TestAttachTmuxSessionUsesDisplayPopupInsideTmux(t *testing.T) {
	stubTmuxLookPath(t, "/usr/bin/tmux", nil)
	old := tmuxEnv
	tmuxEnv = func() string { return "/tmp/tmux-501/default,1234,0" }
	t.Cleanup(func() { tmuxEnv = old })
	stub := withStubExec(t)
	withStubStdio(t)

	// The attach runs via agentExecCommand (not syscall.Exec) so display-popup
	// can return control to the outer tmux when the popup is dismissed. Any
	// non-nil result from the fake process is a failure signal — stub.fn
	// returns a no-op exec.Cmd that exits 0.
	if err := attachTmuxSession(config.HostResolution{Localhost: true}, "leo-primary", attachOptions{}); err != nil {
		t.Fatalf("attachTmuxSession: %v", err)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("want 1 exec call (display-popup), got %d: %v", len(stub.calls), stub.calls)
	}
	argv := stub.calls[0]
	// argv[0] is the tmux binary; the rest are the popup args. Spot-check the
	// essentials rather than pinning the full command string.
	if argv[0] != "/usr/bin/tmux" {
		t.Errorf("argv[0] = %q, want tmux path", argv[0])
	}
	if !containsAll(argv, []string{"display-popup", "-E", "-w", "95%", "-h", "95%"}) {
		t.Errorf("display-popup args missing from %v", argv)
	}
	// The inner command should shell-quote the session name and reference the
	// leo socket explicitly.
	last := argv[len(argv)-1]
	if !strings.Contains(last, "-L leo") || !strings.Contains(last, "'leo-primary'") {
		t.Errorf("inner popup command missing -L leo / quoted session: %q", last)
	}
}

func containsAll(haystack, needles []string) bool {
	for _, n := range needles {
		found := false
		for _, h := range haystack {
			if h == n {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
