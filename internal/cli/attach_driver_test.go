package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/picker"
	"github.com/spf13/cobra"
)

// fakeCLITurnsDriver is a minimal harness.SessionDriver whose Attach returns
// a fixed AttachSpec, used to exercise the non-claude attach dispatch paths
// without a real driven process.
type fakeCLITurnsDriver struct {
	spec harness.AttachSpec
	err  error
}

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

// TestAttachViaDriverDelegatesToTmuxSession verifies a non-empty TmuxSession
// is dispatched via attachTmuxSession — every harness's AttachSpec is a plain
// tmux attach post-#106 cleanup, so there is no separate exec/argv branch
// left to exercise.
func TestAttachViaDriverDelegatesToTmuxSession(t *testing.T) {
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

	spec := harness.AttachSpec{TmuxSession: "leo-worker"}
	if err := attachViaDriver(config.HostResolution{Localhost: true}, spec, attachOptions{}); err != nil {
		t.Fatalf("attachViaDriver: %v", err)
	}
	if execedArgv0 != "/usr/bin/tmux" {
		t.Fatalf("argv0 = %q, want tmux path %q", execedArgv0, "/usr/bin/tmux")
	}
	joined := strings.Join(execedArgv, " ")
	if !strings.Contains(joined, "leo-worker") {
		t.Fatalf("argv = %v, want it to reference the tmux session %q", execedArgv, "leo-worker")
	}
}

// TestAttachViaDriverEmptyTmuxSessionErrors verifies an empty TmuxSession
// (a driver resolve/attach failure upstream) surfaces a clear error instead
// of silently falling through to nothing.
func TestAttachViaDriverEmptyTmuxSessionErrors(t *testing.T) {
	spec := harness.AttachSpec{}
	err := attachViaDriver(config.HostResolution{Localhost: true}, spec, attachOptions{})
	if err == nil {
		t.Fatal("expected an error for an empty TmuxSession")
	}
}

// TestAttachLocalWarnsOnAttachSpecLookupFailure verifies attachLocal no
// longer silently swallows an agentAttachSpecFn error — it prints a one-line
// stderr warning before falling back to the tmux attach path, so a user
// landing in the raw serve pane has a clue why.
//
// The attach-spec lookup runs AFTER the session lookup/ensureAgentRunning
// (see TestAttachLocalDormantNonClaudeAgentRoutesToDriverAfterStart for why),
// so the session lookup is stubbed live here to reach the attach-spec call.
func TestAttachLocalWarnsOnAttachSpecLookupFailure(t *testing.T) {
	stubAgentSessionFull(t, func(workDir, name string) (daemon.AgentSessionResponse, error) {
		return daemon.AgentSessionResponse{Session: "leo-scratch", Name: name}, nil
	})
	stubAgentAttachSpecFn(t, func(workDir, name string) (daemon.AgentAttachSpecResponse, error) {
		return daemon.AgentAttachSpecResponse{}, fmt.Errorf("daemon unreachable")
	})
	_, errBuf := withStubStdio(t)

	_ = attachLocal(context.Background(), &cobra.Command{}, t.TempDir(), "scratch", attachOptions{}) //nolint:errcheck

	warning := errBuf.String()
	if !strings.Contains(warning, "warning:") || !strings.Contains(warning, "daemon unreachable") {
		t.Fatalf("expected a warning mentioning the lookup error, got %q", warning)
	}
}

// TestAttachLocalDormantNonClaudeAgentRoutesToDriverAfterStart verifies
// attachLocal re-checks driver routing AFTER starting a dormant agent,
// matching attach.go's order. Before the fix, the attach-spec lookup ran
// first — a dormant agent's ResolveHandle bails (internal/agent/manager.go),
// so the harness would come back empty and a just-started non-claude agent
// would incorrectly fall through to a raw tmux attach instead of its driver.
func TestAttachLocalDormantNonClaudeAgentRoutesToDriverAfterStart(t *testing.T) {
	drv := registerFakeCLITurnsHarness()
	drv.err = nil

	stubAgentSessionFull(t, func(workDir, name string) (daemon.AgentSessionResponse, error) {
		return daemon.AgentSessionResponse{Session: "leo-fallback", Name: name, Stopped: true}, nil
	})
	withStubStdio(t)

	oldTTY := agentIsTTY
	agentIsTTY = func() bool { return true }
	t.Cleanup(func() { agentIsTTY = oldTTY })
	oldIn := agentStdin
	agentStdin = strings.NewReader("y\n")
	t.Cleanup(func() { agentStdin = oldIn })

	called := stubAgentStart(t, nil)
	stubAgentSessionReady(t, true)

	// The attach-spec stub only reports the driver harness once the agent has
	// actually been started — mirroring ResolveHandle's real behavior, which
	// bails on a still-dormant record (internal/agent/manager.go). If
	// attachLocal looks this up BEFORE starting, it sees the claude/empty
	// fallback and never reaches the driver's session below.
	stubAgentAttachSpecFn(t, func(workDir, name string) (daemon.AgentAttachSpecResponse, error) {
		if !*called {
			return daemon.AgentAttachSpecResponse{Name: name}, nil // claude/unresolved
		}
		drv.spec = harness.AttachSpec{TmuxSession: "leo-driver-session"}
		return daemon.AgentAttachSpecResponse{
			Name:        name,
			Harness:     fakeCLITurnsHarnessName,
			TmuxSession: "leo-driver-session",
		}, nil
	})

	stubTmuxLookPath(t, "/usr/bin/tmux", nil)
	stubOutsideTmux(t)
	var execedArgv []string
	oldExec := agentSyscallExec
	agentSyscallExec = func(argv0 string, argv []string, envv []string) error {
		execedArgv = argv
		return nil
	}
	t.Cleanup(func() { agentSyscallExec = oldExec })

	if err := attachLocal(context.Background(), &cobra.Command{}, t.TempDir(), "scratch", attachOptions{}); err != nil {
		t.Fatalf("attachLocal: %v", err)
	}
	if !*called {
		t.Fatal("expected AgentStart to fire after confirming the prompt")
	}
	joined := strings.Join(execedArgv, " ")
	if !strings.Contains(joined, "leo-driver-session") {
		t.Fatalf("expected the driver's tmux session (looked up post-start) in argv, got %v", execedArgv)
	}
	if strings.Contains(joined, "leo-fallback") {
		t.Fatalf("attach used the pre-start fallback session instead of the driver's, argv = %v", execedArgv)
	}
}

// TestAttachLocalDormantAgentPromptsAndStarts verifies `leo agent attach`
// (attachLocal) prompts to start a dormant agent, then proceeds with the
// tmux attach once confirmed — attachLocal's share of ensureAgentRunning,
// exercised independently of the top-level `leo attach` door.
func TestAttachLocalDormantAgentPromptsAndStarts(t *testing.T) {
	stubAgentAttachSpecFn(t, func(workDir, name string) (daemon.AgentAttachSpecResponse, error) {
		return daemon.AgentAttachSpecResponse{Name: name}, nil // claude
	})
	stubAgentSessionFull(t, func(workDir, name string) (daemon.AgentSessionResponse, error) {
		return daemon.AgentSessionResponse{Session: "leo-scratch", Name: "scratch", Stopped: true}, nil
	})
	withStubStdio(t)

	oldTTY := agentIsTTY
	agentIsTTY = func() bool { return true }
	t.Cleanup(func() { agentIsTTY = oldTTY })
	oldIn := agentStdin
	agentStdin = strings.NewReader("y\n")
	t.Cleanup(func() { agentStdin = oldIn })

	called := stubAgentStart(t, nil)
	stubAgentSessionReady(t, true)
	stubTmuxLookPath(t, "/usr/bin/tmux", nil)
	stubOutsideTmux(t)
	var execedArgv []string
	oldExec := agentSyscallExec
	agentSyscallExec = func(argv0 string, argv []string, envv []string) error {
		execedArgv = argv
		return nil
	}
	t.Cleanup(func() { agentSyscallExec = oldExec })

	if err := attachLocal(context.Background(), &cobra.Command{}, t.TempDir(), "scratch", attachOptions{}); err != nil {
		t.Fatalf("attachLocal: %v", err)
	}
	if !*called {
		t.Fatal("expected AgentStart to fire after confirming the prompt")
	}
	if len(execedArgv) == 0 || !strings.Contains(strings.Join(execedArgv, " "), "leo-scratch") {
		t.Fatalf("expected attach to proceed to the tmux session, argv = %v", execedArgv)
	}
}

// TestAttachLocalMissingAgentReturnsError verifies attachLocal surfaces a
// clear error when the query matches no agent at all — the explore pass
// found no existing test for this path (only the top-level `leo attach`
// door had one).
func TestAttachLocalMissingAgentReturnsError(t *testing.T) {
	stubAgentAttachSpecFn(t, func(workDir, name string) (daemon.AgentAttachSpecResponse, error) {
		return daemon.AgentAttachSpecResponse{}, fmt.Errorf("not found")
	})
	stubAgentSessionFull(t, func(workDir, name string) (daemon.AgentSessionResponse, error) {
		return daemon.AgentSessionResponse{}, &agent.ErrNotFound{Query: name}
	})
	withStubStdio(t)

	err := attachLocal(context.Background(), &cobra.Command{}, t.TempDir(), "nope", attachOptions{})
	if err == nil {
		t.Fatal("expected an error for a missing agent")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("expected error to reference the query %q, got %v", "nope", err)
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

// TestAttachTopLevelRoutesNonClaudeAgent verifies `leo attach <name>`
// resolves a non-claude agent through the driver, which now reports the same
// tmux-attach shape as claude.
func TestAttachTopLevelRoutesNonClaudeAgent(t *testing.T) {
	drv := registerFakeCLITurnsHarness()
	drv.spec = harness.AttachSpec{TmuxSession: "leo-scratch"}
	drv.err = nil

	path := newAttachAliasTestConfig(t)
	stubTmuxLookPath(t, "/usr/bin/tmux", nil)
	stubOutsideTmux(t)
	withStubStdio(t)
	stubAgentSession(t, func(workDir, name string) (string, error) {
		if name == "scratch" {
			return "leo-scratch", nil
		}
		return "", fmt.Errorf("not found")
	})
	stubAgentAttachSpecFn(t, func(workDir, name string) (daemon.AgentAttachSpecResponse, error) {
		return daemon.AgentAttachSpecResponse{
			Name:        name,
			Harness:     fakeCLITurnsHarnessName,
			TmuxSession: "leo-scratch",
		}, nil
	})
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
		t.Fatalf("argv0 = %q, want tmux path %q (driver dispatch didn't fire); argv=%v", execedArgv0, "/usr/bin/tmux", execedArgv)
	}
	if !strings.Contains(strings.Join(execedArgv, " "), "leo-scratch") {
		t.Fatalf("argv = %v, want it to reference the tmux session %q", execedArgv, "leo-scratch")
	}
}

// TestAttachTopLevelClaudeAgentKeepsTmuxPath verifies a claude agent (empty
// Harness from the attach-spec endpoint) still goes through the existing
// tmux attach flow — the driver dispatch must not fire.
func TestAttachTopLevelClaudeAgentKeepsTmuxPath(t *testing.T) {
	path := newAttachAliasTestConfig(t)
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
//
// The picker itself (internal/picker) is a full-screen Bubble Tea program and
// is unit-tested in its own package. Here we only verify attachPickedAgent —
// the post-picker dispatch step — routes a chosen agent through the correct
// path, without driving the real TUI (which needs a TTY the test env lacks).

// TestAttachPickedAgentRoutesNonClaudeAgent verifies a local, non-claude
// agent chosen in the picker routes through the driver, which now reports
// the same tmux-attach shape as claude.
func TestAttachPickedAgentRoutesNonClaudeAgent(t *testing.T) {
	drv := registerFakeCLITurnsHarness()
	drv.spec = harness.AttachSpec{TmuxSession: "leo-solo"}
	drv.err = nil

	home := t.TempDir()
	cfg := &config.Config{HomePath: home, Defaults: config.DefaultsConfig{Model: "sonnet"}}

	stubAgentAttachSpecFn(t, func(workDir, name string) (daemon.AgentAttachSpecResponse, error) {
		return daemon.AgentAttachSpecResponse{
			Name:        name,
			Harness:     fakeCLITurnsHarnessName,
			TmuxSession: "leo-solo",
		}, nil
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

	if err := attachPickedAgent(context.Background(), cfg, picker.Agent{Host: picker.LocalHost, Name: "solo"}, attachOptions{}); err != nil {
		t.Fatalf("attachPickedAgent: %v", err)
	}
	if execedArgv0 != "/usr/bin/tmux" {
		t.Fatalf("argv0 = %q, want tmux path %q (driver dispatch didn't fire); argv=%v", execedArgv0, "/usr/bin/tmux", execedArgv)
	}
	if !strings.Contains(strings.Join(execedArgv, " "), "leo-solo") {
		t.Fatalf("argv = %v, want it to reference the tmux session %q", execedArgv, "leo-solo")
	}
}

// TestAttachPickedAgentAttachOnlyRemoteRowAttachesSessionDirectly verifies a
// tmux-fallback remote row (AttachOnly, Name is the full tmux session name)
// attaches that tmux session directly over ssh instead of routing through
// `agent attach <name>` on the remote — the fallback Name is not a bare agent
// name, so the remote CLI resolution would fail.
func TestAttachPickedAgentAttachOnlyRemoteRowAttachesSessionDirectly(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{
		HomePath: home,
		Client: config.ClientConfig{
			Hosts: map[string]config.HostConfig{
				"prod": {SSH: "user@prod.example.com", SSHArgs: []string{"-p", "2222"}},
			},
		},
	}
	stub := withStubExec(t)
	withStubStdio(t)

	err := attachPickedAgent(context.Background(), cfg, picker.Agent{
		Host:       "prod",
		Name:       "leo-orphan",
		AttachOnly: true,
	}, attachOptions{})
	if err != nil {
		t.Fatalf("attachPickedAgent: %v", err)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("expected 1 ssh call, got %d: %v", len(stub.calls), stub.calls)
	}
	joined := strings.Join(stub.calls[0], " ")
	if strings.Contains(joined, "agent attach") {
		t.Fatalf("argv = %v, must NOT invoke remote `agent attach` for an AttachOnly row", stub.calls[0])
	}
	if !strings.Contains(joined, "attach") || !strings.Contains(joined, "leo-orphan") {
		t.Fatalf("argv = %v, want a tmux attach targeting session %q", stub.calls[0], "leo-orphan")
	}
}

// TestAttachPickedAgentClaudeAgentKeepsTmuxPath verifies a local claude agent
// chosen in the picker keeps the plain tmux attach path.
func TestAttachPickedAgentClaudeAgentKeepsTmuxPath(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home, Defaults: config.DefaultsConfig{Model: "sonnet"}}

	stubAgentAttachSpecFn(t, func(workDir, name string) (daemon.AgentAttachSpecResponse, error) {
		return daemon.AgentAttachSpecResponse{Name: name}, nil // claude
	})
	stubTmuxLookPath(t, "/usr/bin/tmux", nil)
	stubOutsideTmux(t)
	var execedArgv0 string
	old := agentSyscallExec
	agentSyscallExec = func(argv0 string, argv []string, envv []string) error {
		execedArgv0 = argv0
		return nil
	}
	t.Cleanup(func() { agentSyscallExec = old })

	if err := attachPickedAgent(context.Background(), cfg, picker.Agent{Host: picker.LocalHost, Name: "solo"}, attachOptions{}); err != nil {
		t.Fatalf("attachPickedAgent: %v", err)
	}
	if execedArgv0 != "/usr/bin/tmux" {
		t.Fatalf("expected the tmux attach path for a claude agent, got argv0=%q", execedArgv0)
	}
}

// TestAttachPickedAgentDormantAgentPromptsAndStarts verifies attachPickedAgent
// (the picker's post-selection dispatch) prompts to start a dormant local
// agent before attaching, same as the two cobra-command doors — the picker
// has no cobra.Command to gate through, so this exercises the
// ensureAgentRunningForPicker/gateToolFor path (internal/cli/permissions.go).
func TestAttachPickedAgentDormantAgentPromptsAndStarts(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home, Defaults: config.DefaultsConfig{Model: "sonnet"}}

	stubAgentAttachSpecFn(t, func(workDir, name string) (daemon.AgentAttachSpecResponse, error) {
		return daemon.AgentAttachSpecResponse{Name: name}, nil // claude
	})
	withStubStdio(t)

	oldTTY := agentIsTTY
	agentIsTTY = func() bool { return true }
	t.Cleanup(func() { agentIsTTY = oldTTY })
	oldIn := agentStdin
	agentStdin = strings.NewReader("y\n")
	t.Cleanup(func() { agentStdin = oldIn })

	called := stubAgentStart(t, nil)
	stubAgentSessionReady(t, true)
	stubTmuxLookPath(t, "/usr/bin/tmux", nil)
	stubOutsideTmux(t)
	var execedArgv []string
	oldExec := agentSyscallExec
	agentSyscallExec = func(argv0 string, argv []string, envv []string) error {
		execedArgv = argv
		return nil
	}
	t.Cleanup(func() { agentSyscallExec = oldExec })

	err := attachPickedAgent(context.Background(), cfg, picker.Agent{Host: picker.LocalHost, Name: "scratch", Status: "stopped"}, attachOptions{})
	if err != nil {
		t.Fatalf("attachPickedAgent: %v", err)
	}
	if !*called {
		t.Fatal("expected AgentStart to fire after confirming the prompt")
	}
	if len(execedArgv) == 0 || !strings.Contains(strings.Join(execedArgv, " "), "leo-scratch") {
		t.Fatalf("expected attach to proceed to the tmux session, argv = %v", execedArgv)
	}
}

// TestAttachPickedAgentDormantAgentNonTTYErrors verifies a non-interactive
// picker dispatch (e.g. driven by a test harness or a non-terminal caller)
// on a dormant agent fails fast instead of hanging on an unanswerable prompt.
func TestAttachPickedAgentDormantAgentNonTTYErrors(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home, Defaults: config.DefaultsConfig{Model: "sonnet"}}
	withStubStdio(t)

	oldTTY := agentIsTTY
	agentIsTTY = func() bool { return false }
	t.Cleanup(func() { agentIsTTY = oldTTY })
	called := stubAgentStart(t, nil)

	err := attachPickedAgent(context.Background(), cfg, picker.Agent{Host: picker.LocalHost, Name: "scratch", Status: "stopped"}, attachOptions{})
	if err == nil {
		t.Fatal("expected an error for a dormant agent off a TTY")
	}
	if !strings.Contains(err.Error(), "leo agent start") {
		t.Errorf("unexpected error: %v", err)
	}
	if *called {
		t.Fatal("AgentStart should not fire without confirmation")
	}
}
