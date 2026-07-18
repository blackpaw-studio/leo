package cli

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/config"
)

// --- top-level `leo attach` alias ---

func newAttachAliasTestConfig(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	cfg := &config.Config{
		HomePath: home,
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 10},
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
	lookupAgentSession = func(_ context.Context, workDir, name string) (string, error) {
		return fn(workDir, name)
	}
	t.Cleanup(func() { lookupAgentSession = old })
}

// TestAttachAliasResolvesToAgent exercises the "name matches an agent but not a
// process" branch. We stub the daemon lookup so it reports a live session.
func TestAttachAliasResolvesToAgent(t *testing.T) {
	path := newAttachAliasTestConfig(t)
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
	// argv is ["tmux", "-L", "leo", "attach", "-t", "=leo-scratch"]
	if len(execedArgv) != 6 || execedArgv[5] != "=leo-scratch" {
		t.Errorf("unexpected tmux argv: %v", execedArgv)
	}
}

// TestAttachAliasMissingReturnsError verifies that when a name matches no
// known agent, the user gets a friendly error instead of a silent misfire.
func TestAttachAliasMissingReturnsError(t *testing.T) {
	path := newAttachAliasTestConfig(t)
	withStubExec(t)
	withStubStdio(t)
	// The daemon returns a typed *agent.ErrNotFound for a resolve miss (see
	// daemon.AgentSession) — stub that shape, not a bare error, so the test
	// exercises the real not-found branch rather than the catch-all.
	stubAgentSession(t, func(workDir, name string) (string, error) {
		return "", &agent.ErrNotFound{Query: name}
	})

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "attach", "nope", "--host", "localhost"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected error for unknown name")
	}
	if !strings.Contains(err.Error(), `no agent named "nope"`) {
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

// Over SSH, --cc (tmux control mode) forces a remote PTY (-tt, since tmux -CC
// calls tcgetattr and aborts without a terminal) and disables the ssh escape
// char (-e none) so the control-mode framing survives, plus the shared
// ControlMaster so it rides the forward connection. It runs remote
// `tmux -CC attach` directly.
func TestAttachTmuxSessionCCRemoteStreamsControlMode(t *testing.T) {
	stub := withStubExec(t)
	withStubStdio(t)
	ctl := filepath.Join(t.TempDir(), "prod.ctl")
	res := config.HostResolution{
		Name:        "prod",
		Host:        config.HostConfig{SSH: "user@prod.example.com", SSHArgs: []string{"-p", "2222"}},
		ControlPath: ctl,
	}
	if err := attachTmuxSession(res, "leo-primary", attachOptions{cc: true}); err != nil {
		t.Fatalf("attach --cc over ssh: %v", err)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("expected 1 ssh call, got %d: %v", len(stub.calls), stub.calls)
	}
	want := []string{
		"ssh", "-tt", "-e", "none", "user@prod.example.com", "-p", "2222",
		"-o", "ControlMaster=auto", "-o", "ControlPath=" + ctl,
		config.DefaultRemoteTmuxPath, "-L", "leo", "-CC", "attach", "-t", "'=leo-primary'",
	}
	if !equalStrings(stub.calls[0], want) {
		t.Errorf("cc ssh args = %v, want %v", stub.calls[0], want)
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
	if !strings.Contains(last, "-L leo") || !strings.Contains(last, "'=leo-primary'") {
		t.Errorf("inner popup command missing -L leo / quoted session: %q", last)
	}
}

// A remote tmux that isn't on the non-interactive SSH PATH exits 127; the hint
// helper must turn that bare status into actionable tmux_path guidance, while
// leaving non-127 errors, nil, and localhost untouched.
func TestHintRemoteTmuxMissing(t *testing.T) {
	remote := config.HostResolution{Name: "prod", Host: config.HostConfig{SSH: "u@prod"}}
	local := config.HostResolution{Localhost: true}

	// Synthesize a real *exec.ExitError with the wanted code by running a
	// command that exits with it — cheaper and truer than faking the type.
	exit := func(code int) error {
		return exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run()
	}

	got := hintRemoteTmuxMissing(remote, exit(127))
	if got == nil || !strings.Contains(got.Error(), "tmux_path") {
		t.Errorf("127 on remote should hint tmux_path, got %v", got)
	}
	if !strings.Contains(got.Error(), "client.hosts.prod.tmux_path") {
		t.Errorf("hint should name the host's tmux_path key, got %v", got)
	}

	if err := hintRemoteTmuxMissing(remote, exit(1)); err == nil || strings.Contains(err.Error(), "tmux_path") {
		t.Errorf("non-127 error must pass through unhinted, got %v", err)
	}
	if err := hintRemoteTmuxMissing(local, exit(127)); err == nil || strings.Contains(err.Error(), "tmux_path") {
		t.Errorf("localhost must not be hinted, got %v", err)
	}
	if err := hintRemoteTmuxMissing(remote, nil); err != nil {
		t.Errorf("nil must pass through, got %v", err)
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

// TestAttachAliasSurfacesAmbiguousError verifies that `leo attach <name>`
// propagates the daemon's typed resolve failure instead of flattening every
// error into "no agent named". An ambiguous query carries the candidate names
// the user needs to disambiguate; reporting it as "not found" sent the user
// hunting for an agent that plainly existed.
func TestAttachAliasSurfacesAmbiguousError(t *testing.T) {
	path := newAttachAliasTestConfig(t)
	withStubExec(t)
	withStubStdio(t)
	stubAgentSession(t, func(workDir, name string) (string, error) {
		return "", &agent.ErrAmbiguous{
			Query:   "vitals",
			Matches: []string{"leo-vitals", "leo-vitals-enhancements"},
		}
	})

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "attach", "vitals", "--host", "localhost"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected error for ambiguous name")
	}
	for _, want := range []string{"ambiguous", "leo-vitals", "leo-vitals-enhancements"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}
