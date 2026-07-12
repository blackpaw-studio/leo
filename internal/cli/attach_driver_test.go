package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/harness"
)

// fakeCLITurnsDriver is a minimal harness.SessionDriver whose Attach returns
// a fixed AttachSpec, used to exercise the non-claude attach dispatch paths
// without a real driven process.
type fakeCLITurnsDriver struct {
	spec harness.AttachSpec
	err  error
}

func (d fakeCLITurnsDriver) Style() harness.DriveStyle                          { return harness.DriveTurns }
func (d fakeCLITurnsDriver) Start(context.Context, harness.SessionHandle) error { return nil }
func (d fakeCLITurnsDriver) Inject(context.Context, harness.SessionHandle, string) (*harness.Result, error) {
	return nil, nil
}
func (d fakeCLITurnsDriver) Attach(harness.SessionHandle) (harness.AttachSpec, error) {
	return d.spec, d.err
}

// fakeCLITurnsHarness is a minimal harness.Harness wrapping a fakeCLITurnsDriver.
type fakeCLITurnsHarness struct {
	name   string
	driver harness.SessionDriver
}

func (h fakeCLITurnsHarness) Name() string                              { return h.name }
func (h fakeCLITurnsHarness) Binary() string                            { return h.name }
func (h fakeCLITurnsHarness) Args(harness.LaunchSpec) ([]string, error) { return nil, nil }
func (h fakeCLITurnsHarness) SessionArgs(harness.SessionState) []string { return nil }
func (h fakeCLITurnsHarness) ValidateModel(string) error                { return nil }
func (h fakeCLITurnsHarness) DecodeOptions(map[string]any) (any, error) { return nil, nil }
func (h fakeCLITurnsHarness) OptionsSchema() []harness.OptionField      { return nil }
func (h fakeCLITurnsHarness) SupportsChannels() bool                    { return false }
func (h fakeCLITurnsHarness) ParseEvents(io.Reader) (harness.Result, error) {
	return harness.Result{}, nil
}
func (h fakeCLITurnsHarness) Env(harness.LaunchSpec) (map[string]string, error) { return nil, nil }
func (h fakeCLITurnsHarness) SupportsKind(harness.Kind) bool                    { return true }
func (h fakeCLITurnsHarness) Driver() harness.SessionDriver                     { return h.driver }

const fakeCLITurnsHarnessName = "faketurns-clitest"

var registerFakeCLITurnsHarnessOnce sync.Once
var fakeCLITurnsDriverInstance fakeCLITurnsDriver

// registerFakeCLITurnsHarness registers fakeCLITurnsHarnessName once (the
// harness registry panics on duplicate registration) and returns the shared
// driver so each test can point it at a fresh AttachSpec.
func registerFakeCLITurnsHarness() *fakeCLITurnsDriver {
	registerFakeCLITurnsHarnessOnce.Do(func() {
		harness.Register(fakeCLITurnsHarness{name: fakeCLITurnsHarnessName, driver: &fakeCLITurnsDriverInstance})
	})
	return &fakeCLITurnsDriverInstance
}

// TestResolveProcessAttachSpecNonClaude verifies a process configured with a
// non-claude harness resolves via the driver's Attach, not the tmux path.
func TestResolveProcessAttachSpecNonClaude(t *testing.T) {
	drv := registerFakeCLITurnsHarness()
	drv.spec = harness.AttachSpec{Argv: []string{"faketurns", "attach", "worker"}}
	drv.err = nil

	cfg := &config.Config{
		HomePath: t.TempDir(),
		Defaults: config.DefaultsConfig{Model: "sonnet", Harness: fakeCLITurnsHarnessName},
		Processes: map[string]config.ProcessConfig{
			"worker": {Enabled: true},
		},
	}

	harnessName, spec, ok, err := resolveProcessAttachSpec(cfg, "worker")
	if err != nil {
		t.Fatalf("resolveProcessAttachSpec: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true for a non-claude process")
	}
	if harnessName != fakeCLITurnsHarnessName {
		t.Fatalf("harnessName = %q, want %q", harnessName, fakeCLITurnsHarnessName)
	}
	if len(spec.Argv) != 3 || spec.Argv[0] != "faketurns" {
		t.Fatalf("spec.Argv = %v", spec.Argv)
	}
}

// TestResolveProcessAttachSpecClaudeFallsBack verifies a claude (default
// harness) process reports ok=false so the caller falls back to the existing
// tmux attach flow.
func TestResolveProcessAttachSpecClaudeFallsBack(t *testing.T) {
	cfg := &config.Config{
		HomePath: t.TempDir(),
		Defaults: config.DefaultsConfig{Model: "sonnet"},
		Processes: map[string]config.ProcessConfig{
			"worker": {Enabled: true},
		},
	}
	_, _, ok, err := resolveProcessAttachSpec(cfg, "worker")
	if err != nil {
		t.Fatalf("resolveProcessAttachSpec: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false for a claude process")
	}
}

// TestResolveProcessAttachSpecUnknownProcess verifies ok=false, no error, for
// a name that isn't in the config at all.
func TestResolveProcessAttachSpecUnknownProcess(t *testing.T) {
	cfg := &config.Config{HomePath: t.TempDir()}
	_, _, ok, err := resolveProcessAttachSpec(cfg, "ghost")
	if err != nil {
		t.Fatalf("resolveProcessAttachSpec: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false for an unknown process")
	}
}

// stubFakeExecutableOnPath creates an executable file named binName inside a
// fresh directory and prepends that directory to $PATH for the duration of
// the test, so exec.LookPath(binName) resolves without requiring a real
// harness binary on the runner's PATH. Returns the resolved absolute path.
func stubFakeExecutableOnPath(t *testing.T, binName string) string {
	t.Helper()
	dir := t.TempDir()
	binPath := filepath.Join(dir, binName)
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatalf("writing fake binary: %v", err)
	}
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+oldPath)
	return binPath
}

// TestAttachViaDriverLocalExecsArgv verifies a non-nil Argv is exec'd locally
// via agentSyscallExec, mirroring attachTmuxSession's outside-tmux branch.
// argv[0] is resolved through exec.LookPath before exec (syscall.Exec needs a
// real path, not a bare binary name) — see TestAttachViaDriverLocalResolvesLookPath
// for the case where argv[0] isn't already an absolute/relative path.
func TestAttachViaDriverLocalExecsArgv(t *testing.T) {
	binPath := stubFakeExecutableOnPath(t, "faketurns")

	var execedArgv0 string
	var execedArgv []string
	old := agentSyscallExec
	agentSyscallExec = func(argv0 string, argv []string, envv []string) error {
		execedArgv0 = argv0
		execedArgv = argv
		return nil
	}
	t.Cleanup(func() { agentSyscallExec = old })

	spec := harness.AttachSpec{Argv: []string{"faketurns", "attach", "worker"}}
	if err := attachViaDriver(config.HostResolution{Localhost: true}, spec); err != nil {
		t.Fatalf("attachViaDriver: %v", err)
	}
	if execedArgv0 != binPath {
		t.Fatalf("argv0 = %q, want LookPath-resolved %q", execedArgv0, binPath)
	}
	// argv itself (which becomes the exec'd program's os.Args) keeps the
	// original bare name at argv[0] — only the exec path is resolved.
	if len(execedArgv) != 3 || execedArgv[0] != "faketurns" {
		t.Fatalf("argv = %v, want argv[0] to stay %q", execedArgv, "faketurns")
	}
}

// TestAttachViaDriverLocalResolvesLookPath is the direct Bug B regression
// test: a bare argv[0] (as the daemon's AttachSpec always sends — e.g.
// "opencode") must be resolved to a real path via exec.LookPath before
// syscall.Exec, or the exec fails with ENOENT even when the binary is on
// $PATH.
func TestAttachViaDriverLocalResolvesLookPath(t *testing.T) {
	binPath := stubFakeExecutableOnPath(t, "opencode")

	var execedArgv0 string
	old := agentSyscallExec
	agentSyscallExec = func(argv0 string, argv []string, envv []string) error {
		execedArgv0 = argv0
		return nil
	}
	t.Cleanup(func() { agentSyscallExec = old })

	spec := harness.AttachSpec{Argv: []string{"opencode", "attach", "http://127.0.0.1:60629"}}
	if err := attachViaDriver(config.HostResolution{Localhost: true}, spec); err != nil {
		t.Fatalf("attachViaDriver: %v", err)
	}
	if execedArgv0 != binPath {
		t.Fatalf("execedArgv0 = %q, want resolved path %q", execedArgv0, binPath)
	}
}

// TestAttachViaDriverLocalMissingBinaryErrors verifies a missing argv[0]
// binary produces a named error instead of a bare/confusing exec failure.
func TestAttachViaDriverLocalMissingBinaryErrors(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // empty dir on PATH — nothing resolves

	spec := harness.AttachSpec{Argv: []string{"opencode", "attach", "worker"}}
	err := attachViaDriver(config.HostResolution{Localhost: true}, spec)
	if err == nil {
		t.Fatal("expected an error when the binary isn't on PATH")
	}
	if !strings.Contains(err.Error(), "opencode") || !strings.Contains(err.Error(), "not found on PATH") {
		t.Fatalf("error = %q, want it to name the binary and say 'not found on PATH'", err.Error())
	}
}

// TestAttachViaDriverRemoteShellQuotesEveryToken verifies a non-nil Argv on a
// remote host is dispatched over `ssh -tt -e none` with every argv token
// shell-quoted (ssh flattens post-host argv into one shell string, so an
// unquoted token could be mangled by the remote login shell).
func TestAttachViaDriverRemoteShellQuotesEveryToken(t *testing.T) {
	stub := withStubExec(t)
	withStubStdio(t)

	spec := harness.AttachSpec{Argv: []string{"faketurns", "attach", "a worker"}}
	res := config.HostResolution{
		Localhost: false,
		Host:      config.HostConfig{SSH: "user@remote.example.com"},
	}
	if err := attachViaDriver(res, spec); err != nil {
		t.Fatalf("attachViaDriver: %v", err)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("expected 1 ssh call, got %d: %v", len(stub.calls), stub.calls)
	}
	call := stub.calls[0]
	if call[0] != "ssh" || call[1] != "-tt" || call[2] != "-e" || call[3] != "none" {
		t.Fatalf("unexpected ssh prefix: %v", call)
	}
	joined := call[len(call)-1]
	want := "'faketurns' 'attach' 'a worker'"
	if joined != want {
		t.Fatalf("remote command = %q, want %q", joined, want)
	}
}

// TestAttachViaDriverNilArgvPrintsHistoryTail verifies a nil Argv (no live
// attach for this harness) falls back to printing the tail of HistoryPath —
// a note plus only the last attachHistoryTailLines lines, not the whole file.
func TestAttachViaDriverNilArgvPrintsHistoryTail(t *testing.T) {
	out, _ := withStubStdio(t)

	historyPath := filepath.Join(t.TempDir(), "history.log")
	var lines string
	for i := 1; i <= 60; i++ {
		lines += "line" + string(rune('0'+i%10)) + "\n"
	}
	if err := os.WriteFile(historyPath, []byte(lines), 0600); err != nil {
		t.Fatalf("writing history file: %v", err)
	}

	spec := harness.AttachSpec{HistoryPath: historyPath}
	if err := attachViaDriver(config.HostResolution{Localhost: true}, spec); err != nil {
		t.Fatalf("attachViaDriver: %v", err)
	}

	printed := out.String()
	if !strings.Contains(printed, "no live attach") {
		t.Fatalf("expected a no-live-attach note, got: %q", printed)
	}
	gotLines := strings.Count(printed, "\n")
	// note line + attachHistoryTailLines (60 written, capped at 50).
	if gotLines != attachHistoryTailLines+1 {
		t.Fatalf("printed %d lines, want %d (note + tail)", gotLines, attachHistoryTailLines+1)
	}
}

// TestAttachLocalWarnsOnAttachSpecLookupFailure verifies attachLocal no
// longer silently swallows an agentAttachSpecFn error — it prints a one-line
// stderr warning before falling back to the tmux attach path, so a user
// landing in the raw serve pane has a clue why.
//
// The subsequent daemon.AgentSession lookup (attachLocal's tmux fallback) has
// no test seam and will fail fast against the non-existent socket under
// t.TempDir() — that's fine, we only assert the warning fired before that,
// not that the whole fallback chain succeeds end-to-end.
func TestAttachLocalWarnsOnAttachSpecLookupFailure(t *testing.T) {
	stubAgentAttachSpecFn(t, func(workDir, name string) (daemon.AgentAttachSpecResponse, error) {
		return daemon.AgentAttachSpecResponse{}, fmt.Errorf("daemon unreachable")
	})
	_, errBuf := withStubStdio(t)

	_ = attachLocal(context.Background(), t.TempDir(), "scratch", attachOptions{}) //nolint:errcheck

	warning := errBuf.String()
	if !strings.Contains(warning, "warning:") || !strings.Contains(warning, "daemon unreachable") {
		t.Fatalf("expected a warning mentioning the lookup error, got %q", warning)
	}
}

// --- top-level `leo attach` shortcut (attach.go) ---

// stubAgentAttachSpecFn replaces agentAttachSpecFn for the duration of the
// test, mirroring stubAgentSession's pattern for daemon.AgentSession.
func stubAgentAttachSpecFn(t *testing.T, fn func(workDir, name string) (daemon.AgentAttachSpecResponse, error)) {
	t.Helper()
	old := agentAttachSpecFn
	agentAttachSpecFn = func(_ context.Context, workDir, name string) (daemon.AgentAttachSpecResponse, error) {
		return fn(workDir, name)
	}
	t.Cleanup(func() { agentAttachSpecFn = old })
}

// TestAttachTopLevelRoutesNonClaudeProcess verifies `leo attach <name>`
// resolves a non-claude process through the driver, not tmux.
func TestAttachTopLevelRoutesNonClaudeProcess(t *testing.T) {
	binPath := stubFakeExecutableOnPath(t, "faketurns")
	drv := registerFakeCLITurnsHarness()
	drv.spec = harness.AttachSpec{Argv: []string{"faketurns", "attach", "worker"}}
	drv.err = nil

	home := t.TempDir()
	cfg := &config.Config{
		HomePath:  home,
		Defaults:  config.DefaultsConfig{Model: "sonnet", Harness: fakeCLITurnsHarnessName},
		Processes: map[string]config.ProcessConfig{"worker": {Enabled: true}},
	}
	path := home + "/leo.yaml"
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	stubAgentSession(t, func(workDir, name string) (string, error) {
		return "", fmt.Errorf("not an agent")
	})
	stub := withStubExec(t)
	withStubStdio(t)
	var execedArgv0 string
	old := agentSyscallExec
	agentSyscallExec = func(argv0 string, argv []string, envv []string) error {
		execedArgv0 = argv0
		return nil
	}
	t.Cleanup(func() { agentSyscallExec = old })

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "attach", "worker", "--host", "localhost"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if execedArgv0 != binPath {
		t.Fatalf("argv0 = %q, want LookPath-resolved %q (driver dispatch didn't fire); ssh calls = %v", execedArgv0, binPath, stub.calls)
	}
}

// TestAttachTopLevelRoutesNonClaudeAgent verifies `leo attach <name>`
// resolves a non-claude agent through the driver, not tmux.
func TestAttachTopLevelRoutesNonClaudeAgent(t *testing.T) {
	binPath := stubFakeExecutableOnPath(t, "faketurns")
	drv := registerFakeCLITurnsHarness()
	drv.spec = harness.AttachSpec{Argv: []string{"faketurns", "attach", "scratch"}}
	drv.err = nil

	path := newAttachAliasTestConfig(t, nil) // no configured processes
	stub := withStubExec(t)
	withStubStdio(t)
	stubAgentSession(t, func(workDir, name string) (string, error) {
		if name == "scratch" {
			return "leo-scratch", nil
		}
		return "", fmt.Errorf("not found")
	})
	stubAgentAttachSpecFn(t, func(workDir, name string) (daemon.AgentAttachSpecResponse, error) {
		return daemon.AgentAttachSpecResponse{
			Name:    name,
			Harness: fakeCLITurnsHarnessName,
			Argv:    []string{"faketurns", "attach", "scratch"},
		}, nil
	})
	var execedArgv0 string
	old := agentSyscallExec
	agentSyscallExec = func(argv0 string, argv []string, envv []string) error {
		execedArgv0 = argv0
		return nil
	}
	t.Cleanup(func() { agentSyscallExec = old })

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "attach", "scratch", "--host", "localhost"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if execedArgv0 != binPath {
		t.Fatalf("argv0 = %q, want LookPath-resolved %q (driver dispatch didn't fire); ssh calls = %v", execedArgv0, binPath, stub.calls)
	}
}

// TestAttachTopLevelClaudeAgentKeepsTmuxPath verifies a claude agent (empty
// Harness from the attach-spec endpoint) still goes through the existing
// tmux attach flow — the driver dispatch must not fire.
func TestAttachTopLevelClaudeAgentKeepsTmuxPath(t *testing.T) {
	path := newAttachAliasTestConfig(t, nil)
	withStubExec(t)
	withStubStdio(t)
	stubAgentSession(t, func(workDir, name string) (string, error) {
		if name == "scratch" {
			return "leo-scratch", nil
		}
		return "", fmt.Errorf("not found")
	})
	stubAgentAttachSpecFn(t, func(workDir, name string) (daemon.AgentAttachSpecResponse, error) {
		return daemon.AgentAttachSpecResponse{Name: name}, nil // Harness == "" => claude
	})
	stubTmuxLookPath(t, "/usr/bin/tmux", nil)
	stubOutsideTmux(t)
	var execedArgv0 string
	var execedArgv []string
	old := agentSyscallExec
	agentSyscallExec = func(argv0 string, argv []string, envv []string) error {
		execedArgv0 = argv0
		execedArgv = argv
		return nil
	}
	t.Cleanup(func() { agentSyscallExec = old })

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "attach", "scratch", "--host", "localhost"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if execedArgv0 != "/usr/bin/tmux" {
		t.Fatalf("expected the tmux attach path for a claude agent, got argv0=%q argv=%v", execedArgv0, execedArgv)
	}
}

// --- attach picker (attach_picker.go) ---

// TestAttachPickerRoutesNonClaudeAgent verifies the picker's single-choice
// shortcut (no promptui interaction needed) routes a non-claude agent
// through the driver.
func TestAttachPickerRoutesNonClaudeAgent(t *testing.T) {
	binPath := stubFakeExecutableOnPath(t, "faketurns")
	drv := registerFakeCLITurnsHarness()
	drv.spec = harness.AttachSpec{Argv: []string{"faketurns", "attach", "solo"}}
	drv.err = nil

	home := t.TempDir()
	cfg := &config.Config{HomePath: home, Defaults: config.DefaultsConfig{Model: "sonnet"}}
	path := home + "/leo.yaml"
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	oldStdinTerm := stdinIsTerminal
	stdinIsTerminal = func() bool { return true }
	t.Cleanup(func() { stdinIsTerminal = oldStdinTerm })

	oldAgentList := agentListFn
	agentListFn = func(ctx context.Context, homePath string) ([]agent.Record, error) {
		return []agent.Record{{Name: "solo"}}, nil
	}
	t.Cleanup(func() { agentListFn = oldAgentList })

	stubAgentAttachSpecFn(t, func(workDir, name string) (daemon.AgentAttachSpecResponse, error) {
		return daemon.AgentAttachSpecResponse{
			Name:    name,
			Harness: fakeCLITurnsHarnessName,
			Argv:    []string{"faketurns", "attach", "solo"},
		}, nil
	})
	withStubExec(t)
	withStubStdio(t)
	var execedArgv0 string
	old := agentSyscallExec
	agentSyscallExec = func(argv0 string, argv []string, envv []string) error {
		execedArgv0 = argv0
		return nil
	}
	t.Cleanup(func() { agentSyscallExec = old })

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "attach", "--host", "localhost"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if execedArgv0 != binPath {
		t.Fatalf("argv0 = %q, want LookPath-resolved %q (driver dispatch didn't fire)", execedArgv0, binPath)
	}
}

// TestAttachPickerClaudeAgentKeepsTmuxPath verifies the picker's
// single-choice shortcut keeps the tmux path for a claude agent.
func TestAttachPickerClaudeAgentKeepsTmuxPath(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home, Defaults: config.DefaultsConfig{Model: "sonnet"}}
	path := home + "/leo.yaml"
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	oldStdinTerm := stdinIsTerminal
	stdinIsTerminal = func() bool { return true }
	t.Cleanup(func() { stdinIsTerminal = oldStdinTerm })

	oldAgentList := agentListFn
	agentListFn = func(ctx context.Context, homePath string) ([]agent.Record, error) {
		return []agent.Record{{Name: "solo"}}, nil
	}
	t.Cleanup(func() { agentListFn = oldAgentList })

	stubAgentAttachSpecFn(t, func(workDir, name string) (daemon.AgentAttachSpecResponse, error) {
		return daemon.AgentAttachSpecResponse{Name: name}, nil // claude
	})
	withStubExec(t)
	withStubStdio(t)
	stubTmuxLookPath(t, "/usr/bin/tmux", nil)
	stubOutsideTmux(t)
	var execedArgv0 string
	old := agentSyscallExec
	agentSyscallExec = func(argv0 string, argv []string, envv []string) error {
		execedArgv0 = argv0
		return nil
	}
	t.Cleanup(func() { agentSyscallExec = old })

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "attach", "--host", "localhost"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if execedArgv0 != "/usr/bin/tmux" {
		t.Fatalf("expected the tmux attach path for a claude agent, got argv0=%q", execedArgv0)
	}
}
