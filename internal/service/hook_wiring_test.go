package service

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// fakeHookDriver is a harness.SessionDriver stub implementing the optional
// PreLauncher + SessionArgsRefresher capabilities, for exercising
// superviseProcess's generic hook-wiring (Task 7) without depending on a real
// adapter's side effects (e.g. codex's ensureWorkspaceTrusted writes to
// ~/.codex/config.toml, which a service-package test must never touch).
type fakeHookDriver struct {
	mu             sync.Mutex
	preLaunchCalls int
	preLaunchErr   error
	refreshedWith  []string // storedID seen on each RefreshSessionArgs call
}

func (d *fakeHookDriver) Style() harness.DriveStyle                          { return harness.DriveTmux }
func (d *fakeHookDriver) Start(context.Context, harness.SessionHandle) error { return nil }
func (d *fakeHookDriver) Inject(context.Context, harness.SessionHandle, string) (*harness.Result, error) {
	return nil, nil
}
func (d *fakeHookDriver) Attach(harness.SessionHandle) (harness.AttachSpec, error) {
	return harness.AttachSpec{}, nil
}

func (d *fakeHookDriver) PreLaunch(harness.SessionHandle) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.preLaunchCalls++
	return d.preLaunchErr
}

func (d *fakeHookDriver) RefreshSessionArgs(args []string, storedID string) []string {
	d.mu.Lock()
	d.refreshedWith = append(d.refreshedWith, storedID)
	d.mu.Unlock()
	if storedID == "" {
		return args
	}
	return append([]string{"--resume-token", storedID}, args...)
}

func (d *fakeHookDriver) calls() (int, []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	cp := append([]string(nil), d.refreshedWith...)
	return d.preLaunchCalls, cp
}

// testFakeDriver is swapped by each test before invoking superviseProcess;
// fakeHookHarness.Driver() returns whatever is currently set here. Tests in
// this package never run in parallel, so a package-level seam is safe.
var testFakeDriver harness.SessionDriver

// fakeHookHarness is a minimal harness.Harness registered once (in init)
// under a name distinct from every real adapter, so tests can exercise
// process.go's driver-capability wiring against a controlled fake without
// colliding with claude/codex/opencode's real registrations.
type fakeHookHarness struct{}

func (fakeHookHarness) Name() string   { return "fakehook" }
func (fakeHookHarness) Binary() string { return "fakehook-bin" }
func (fakeHookHarness) Args(harness.LaunchSpec) ([]string, error) {
	return []string{"--fake"}, nil
}
func (fakeHookHarness) SessionArgs(harness.SessionState) []string         { return nil }
func (fakeHookHarness) ValidateModel(string) error                        { return nil }
func (fakeHookHarness) DecodeOptions(map[string]any) (any, error)         { return nil, nil }
func (fakeHookHarness) OptionsSchema() []harness.OptionField              { return nil }
func (fakeHookHarness) SupportsChannels() bool                            { return false }
func (fakeHookHarness) ParseEvents(io.Reader) (harness.Result, error)     { return harness.Result{}, nil }
func (fakeHookHarness) Env(harness.LaunchSpec) (map[string]string, error) { return nil, nil }
func (fakeHookHarness) SupportsKind(harness.Kind) bool                    { return true }
func (fakeHookHarness) Driver() harness.SessionDriver                     { return testFakeDriver }

func init() { harness.Register(fakeHookHarness{}) }

// stubTmux writes a tmux stub script logging every invocation to logPath;
// has-session always reports "no live session" so the loop never adopts.
func stubTmux(t *testing.T, dir, logPath string) string {
	t.Helper()
	tmuxStub := filepath.Join(dir, "tmux")
	script := "#!/bin/sh\necho \"$@\" >> " + logPath + "\ncase \"$1\" in has-session) exit 1;; esac\nexit 0\n"
	if err := os.WriteFile(tmuxStub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return tmuxStub
}

func waitForLog(t *testing.T, logPath, want string) string {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		b, _ := os.ReadFile(logPath)
		logged := string(b)
		if strings.Contains(logged, want) {
			return logged
		}
		select {
		case <-deadline:
			t.Fatalf("%q not seen within deadline; tmux log:\n%s", want, logged)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// TestSuperviseProcessRunsPreLaunchBeforeNewSession verifies the driver's
// optional PreLauncher hook fires before every tmux new-session spawn.
func TestSuperviseProcessRunsPreLaunchBeforeNewSession(t *testing.T) {
	drv := &fakeHookDriver{}
	testFakeDriver = drv
	defer func() { testFakeDriver = nil }()

	dir := t.TempDir()
	logPath := filepath.Join(dir, "tmux-args.log")
	tmuxStub := stubTmux(t, dir, logPath)

	ctx, cancel := context.WithCancel(context.Background())
	sv := NewSupervisor(ctx)
	sv.homePath = t.TempDir()

	id := newProcIdentity("hookproc", []string{"--x"})
	spec := ProcessSpec{Name: "hookproc", WorkDir: t.TempDir(), Harness: "fakehook", Kind: harness.KindProcess}

	done := make(chan struct{})
	go func() {
		defer close(done)
		superviseProcess(ctx, tmuxStub, "/fake/claude-path", spec, sv.homePath, sv, id)
	}()
	defer func() { cancel(); <-done }()

	waitForLog(t, logPath, "new-session")

	calls, _ := drv.calls()
	if calls == 0 {
		t.Fatalf("expected PreLaunch to be called at least once before new-session")
	}
}

// TestSuperviseProcessRefreshesArgsFromStoredID verifies a stored process id
// surfaces as refreshed tokens in the spawned shell command.
func TestSuperviseProcessRefreshesArgsFromStoredID(t *testing.T) {
	drv := &fakeHookDriver{}
	testFakeDriver = drv
	defer func() { testFakeDriver = nil }()

	dir := t.TempDir()
	logPath := filepath.Join(dir, "tmux-args.log")
	tmuxStub := stubTmux(t, dir, logPath)

	home := t.TempDir()
	agentOrProcessIDs(home, "hookproc2").Set("stored-id-123")

	ctx, cancel := context.WithCancel(context.Background())
	sv := NewSupervisor(ctx)
	sv.homePath = home

	id := newProcIdentity("hookproc2", []string{"--x"})
	spec := ProcessSpec{Name: "hookproc2", WorkDir: t.TempDir(), Harness: "fakehook", Kind: harness.KindProcess}

	done := make(chan struct{})
	go func() {
		defer close(done)
		superviseProcess(ctx, tmuxStub, "/fake/claude-path", spec, sv.homePath, sv, id)
	}()
	defer func() { cancel(); <-done }()

	logged := waitForLog(t, logPath, "new-session")
	if !strings.Contains(logged, "'--resume-token' 'stored-id-123'") {
		t.Fatalf("expected refreshed args with stored id in spawned command:\n%s", logged)
	}
}

// TestSuperviseProcessNoStoredIDLeavesArgsUnchanged verifies that with no
// stored id, argv is unchanged (RefreshSessionArgs is still called with "",
// but the fake driver's own no-op-on-empty semantics keep argv untouched).
func TestSuperviseProcessNoStoredIDLeavesArgsUnchanged(t *testing.T) {
	drv := &fakeHookDriver{}
	testFakeDriver = drv
	defer func() { testFakeDriver = nil }()

	dir := t.TempDir()
	logPath := filepath.Join(dir, "tmux-args.log")
	tmuxStub := stubTmux(t, dir, logPath)

	ctx, cancel := context.WithCancel(context.Background())
	sv := NewSupervisor(ctx)
	sv.homePath = t.TempDir()

	id := newProcIdentity("hookproc3", []string{"--x"})
	spec := ProcessSpec{Name: "hookproc3", WorkDir: t.TempDir(), Harness: "fakehook", Kind: harness.KindProcess}

	done := make(chan struct{})
	go func() {
		defer close(done)
		superviseProcess(ctx, tmuxStub, "/fake/claude-path", spec, sv.homePath, sv, id)
	}()
	defer func() { cancel(); <-done }()

	logged := waitForLog(t, logPath, "new-session")
	if strings.Contains(logged, "--resume-token") {
		t.Fatalf("expected no resume token with an empty stored id:\n%s", logged)
	}
	if !strings.Contains(logged, "--x") {
		t.Fatalf("expected original argv to survive an empty-id refresh:\n%s", logged)
	}

	_, refreshed := drv.calls()
	if len(refreshed) == 0 || refreshed[0] != "" {
		t.Fatalf("expected RefreshSessionArgs to be called with an empty stored id, got %v", refreshed)
	}
}
