package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/daemon"
	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
)

// claudeLookupStub is a swappable claude lookup: the test flips path to model
// the CLI being installed after the daemon started.
type claudeLookupStub struct{ path string }

func (c *claudeLookupStub) lookup() (string, error) {
	if c.path == "" {
		return "", claudeharness.ErrNotFound
	}
	return c.path, nil
}

// tmuxArgvSupervisor is a supervisor whose tmux is a stub logging each argv
// line to the returned path.
func tmuxArgvSupervisor(t *testing.T, claude *claudeLookupStub) (*Supervisor, string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	sv := NewSupervisor(ctx)
	t.Cleanup(func() { stopLoops(cancel, sv) })
	dir := t.TempDir()
	logPath := filepath.Join(dir, "tmux-args.log")
	sv.tmuxPath = filepath.Join(dir, "tmux")
	script := "#!/bin/sh\necho \"$@\" >> " + logPath + "\ncase \"$1\" in has-session) exit 1;; new-session) exec sleep 30;; esac\nexit 0\n"
	if err := os.WriteFile(sv.tmuxPath, []byte(script), 0o755); err != nil { //nolint:gosec // test fixture, needs +x
		t.Fatal(err)
	}
	sv.homePath = t.TempDir()
	sv.claudeLookup = claude.lookup
	return sv, logPath
}

func TestSpawnAgentFailsClearlyWhenClaudeIsMissing(t *testing.T) {
	claude := &claudeLookupStub{}
	sv, logPath := tmuxArgvSupervisor(t, claude)

	err := sv.SpawnAgent(daemon.AgentSpawnSpec{Name: "a1", WorkDir: t.TempDir()})

	if !errors.Is(err, claudeharness.ErrNotFound) {
		t.Fatalf("SpawnAgent error = %v, want claudeharness.ErrNotFound", err)
	}
	if want := "claude CLI not found; install it from https://claude.com/claude-code"; err.Error() != want {
		t.Errorf("message = %q, want %q", err.Error(), want)
	}
	if _, ok := sv.states["a1"]; ok {
		t.Error("failed spawn left supervisor state behind")
	}
	if got := loggedLines(logPath); got != "" {
		t.Errorf("tmux ran despite the missing claude: %q", got)
	}
}

func TestSpawnAgentUsesClaudeInstalledAfterDaemonStart(t *testing.T) {
	claude := &claudeLookupStub{}
	sv, logPath := tmuxArgvSupervisor(t, claude)
	if err := sv.SpawnAgent(daemon.AgentSpawnSpec{Name: "a1", WorkDir: t.TempDir()}); err == nil {
		t.Fatal("first spawn should fail with claude missing")
	}

	claude.path = "/late/bin/claude"
	if err := sv.SpawnAgent(daemon.AgentSpawnSpec{Name: "a1", WorkDir: t.TempDir()}); err != nil {
		t.Fatalf("spawn after claude appeared: %v", err)
	}

	logged := waitForLog(t, logPath, "new-session")
	if !strings.Contains(logged, "/late/bin/claude") {
		t.Errorf("new-session argv does not run the late-installed claude:\n%s", logged)
	}
}

func TestSpawnAgentOtherHarnessIgnoresMissingClaude(t *testing.T) {
	sv, logPath := tmuxArgvSupervisor(t, &claudeLookupStub{})

	err := sv.SpawnAgent(daemon.AgentSpawnSpec{Name: "oc", WorkDir: t.TempDir(), Harness: "opencode", ClaudeArgs: []string{"serve"}})

	if err != nil {
		t.Fatalf("opencode spawn blocked by missing claude: %v", err)
	}
	waitForLog(t, logPath, "new-session")
}

func TestBridgeCapableIsFalseWhenClaudeIsMissing(t *testing.T) {
	sv, _ := tmuxArgvSupervisor(t, &claudeLookupStub{})
	if sv.BridgeCapable() {
		t.Error("BridgeCapable with no claude")
	}
}
