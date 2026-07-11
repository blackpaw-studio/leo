package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
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

// TestAttachViaDriverLocalExecsArgv verifies a non-nil Argv is exec'd locally
// via agentSyscallExec, mirroring attachTmuxSession's outside-tmux branch.
func TestAttachViaDriverLocalExecsArgv(t *testing.T) {
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
	if execedArgv0 != "faketurns" {
		t.Fatalf("argv0 = %q, want %q", execedArgv0, "faketurns")
	}
	if len(execedArgv) != 3 {
		t.Fatalf("argv = %v", execedArgv)
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
