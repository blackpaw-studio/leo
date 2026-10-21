package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
	"github.com/blackpaw-studio/leo/internal/leomcp"
)

// claudeLookupStub is a swappable claude lookup: the test flips path to model
// the CLI being installed after the daemon started.
type claudeLookupStub struct {
	mu    sync.Mutex
	path  string
	calls int
}

func (c *claudeLookupStub) lookup() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.path == "" {
		return "", claudeharness.ErrNotFound
	}
	return c.path, nil
}

func (c *claudeLookupStub) install(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.path = path
}

func (c *claudeLookupStub) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// lenientTempDir is t.TempDir without the strict cleanup: a launched agent's
// background goroutines may still be writing state when the test ends.
func lenientTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "leo-claude-missing-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// tmuxArgvSupervisor is a supervisor whose tmux is a stub logging each argv
// line to the returned path.
func tmuxArgvSupervisor(t *testing.T, claude *claudeLookupStub) (*Supervisor, string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	sv := NewSupervisor(ctx)
	dir := lenientTempDir(t)
	home := lenientTempDir(t)
	t.Cleanup(func() { stopLoops(cancel, sv) }) // runs before the dirs go
	logPath := filepath.Join(dir, "tmux-args.log")
	sv.tmuxPath = filepath.Join(dir, "tmux")
	script := "#!/bin/sh\necho \"$@\" >> " + logPath + "\ncase \"$*\" in *has-session*) exit 1;; *new-session*) echo %1;; esac\nexit 0\n"
	if err := os.WriteFile(sv.tmuxPath, []byte(script), 0o755); err != nil { //nolint:gosec // test fixture, needs +x
		t.Fatal(err)
	}
	sv.homePath = home
	sv.claudeLookup = claude.lookup
	return sv, logPath
}

func TestSpawnAgentFailsClearlyWhenClaudeIsMissing(t *testing.T) {
	claude := &claudeLookupStub{}
	sv, logPath := tmuxArgvSupervisor(t, claude)

	err := sv.SpawnAgent(daemon.AgentSpawnSpec{Name: "a1", WorkDir: lenientTempDir(t)})

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
	if err := sv.SpawnAgent(daemon.AgentSpawnSpec{Name: "a1", WorkDir: lenientTempDir(t)}); err == nil {
		t.Fatal("first spawn should fail with claude missing")
	}

	claude.install("/late/bin/claude")
	if err := sv.SpawnAgent(daemon.AgentSpawnSpec{Name: "a1", WorkDir: lenientTempDir(t)}); err != nil {
		t.Fatalf("spawn after claude appeared: %v", err)
	}

	logged := waitForLog(t, logPath, "new-session")
	if !strings.Contains(logged, "/late/bin/claude") {
		t.Errorf("new-session argv does not run the late-installed claude:\n%s", logged)
	}
}

func TestSpawnAgentOtherHarnessIgnoresMissingClaude(t *testing.T) {
	sv, logPath := tmuxArgvSupervisor(t, &claudeLookupStub{})

	err := sv.SpawnAgent(daemon.AgentSpawnSpec{Name: "oc", WorkDir: lenientTempDir(t), Harness: "opencode", ClaudeArgs: []string{"serve"}})

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

// A worktree agent whose boot-time restore fails because claude is missing is
// kept as a system-stopped record, so `leo agent start` recovers it once
// claude is installed (a non-stopped record with nothing supervising it would
// be refused with ErrAgentNotStopped).
func TestWorktreeAgentRestoreWithoutClaudeIsStartableOnceInstalled(t *testing.T) {
	claude := &claudeLookupStub{}
	sv, logPath := tmuxArgvSupervisor(t, claude)
	home := sv.homePath
	rec := agentstore.Record{
		Name:          "leo-coding-owner-repo-feat-x",
		Template:      "coding",
		Repo:          "owner/repo",
		Workspace:     lenientTempDir(t),
		Branch:        "feat/x",
		CanonicalPath: lenientTempDir(t),
		ClaudeArgs:    []string{"--model", "sonnet", "--session-id", "sid-1"},
		SessionID:     "sid-1",
		WebPort:       "8370",
		SpawnedAt:     time.Now(),
	}
	if err := agentstore.Save(home, rec); err != nil {
		t.Fatal(err)
	}

	if got := RestoreAgents(home, "", "", sv, leomcp.Server{}); got != 0 {
		t.Fatalf("restored %d agents with claude missing, want 0", got)
	}
	stored, err := agentstore.Load(agentstore.FilePath(home))
	if err != nil {
		t.Fatal(err)
	}
	if got := stored[rec.Name]; !got.Stopped || !got.IsFailedRestore() {
		t.Fatalf("record after failed restore = %+v, want a system-stopped (failed-restore) record", got)
	}

	claude.install("/late/bin/claude")
	cfg := &config.Config{HomePath: home}
	mgr := agent.New(func() (*config.Config, error) { return cfg, nil }, sv, sv.tmuxPath, "tok")
	if err := mgr.Start(rec.Name); err != nil {
		t.Fatalf("leo agent start after claude was installed: %v", err)
	}
	if logged := waitForLog(t, logPath, "new-session"); !strings.Contains(logged, "/late/bin/claude") {
		t.Errorf("start did not launch the late-installed claude:\n%s", logged)
	}
}

// Adopting a session that survived a daemon bounce never needs the claude
// binary: with claude moved or uninstalled, the live session must still be
// adopted, not killed or respawned.
func TestAdoptSurvivingSessionWithoutClaude(t *testing.T) {
	sv, logPath := tmuxArgvSupervisor(t, &claudeLookupStub{})
	script := "#!/bin/sh\necho \"$@\" >> " + logPath + "\ncase \"$*\" in *show-options*) echo %1;; *show-environment*) exit 1;; esac\nexit 0\n"
	if err := os.WriteFile(sv.tmuxPath, []byte(script), 0o755); err != nil { //nolint:gosec // test fixture, needs +x
		t.Fatal(err)
	}

	if err := sv.SpawnAgent(daemon.AgentSpawnSpec{Name: "survivor", WorkDir: lenientTempDir(t), Adopt: true}); err != nil {
		t.Fatalf("adopting a live session with claude missing: %v", err)
	}

	logged := waitForLog(t, logPath, "show-options") // the adopt path's primary-pane lookup
	for _, forbidden := range []string{"kill-session", "new-session"} {
		if strings.Contains(logged, forbidden) {
			t.Errorf("adoption ran %s:\n%s", forbidden, logged)
		}
	}
}

// Once an adopted session ends, the next launch is a fresh one and needs
// claude: it looks it up at that moment, waits and retries while it is missing
// (never launching a stale path), and launches it as soon as it appears.
func TestFreshLaunchAfterAdoptedSessionEndsResolvesClaudeThen(t *testing.T) {
	origPoll, origBackoff := sessionPollInterval, initialBackoff
	sessionPollInterval, initialBackoff = time.Millisecond, time.Millisecond
	t.Cleanup(func() { sessionPollInterval, initialBackoff = origPoll, origBackoff })
	claude := &claudeLookupStub{}
	sv, logPath := tmuxArgvSupervisor(t, claude)
	ended := filepath.Join(lenientTempDir(t), "ended")
	script := "#!/bin/sh\necho \"$@\" >> " + logPath + "\ncase \"$*\" in *has-session*) [ -e " + ended + " ] && exit 1;; *show-options*) echo %1;; *show-environment*) exit 1;; *display-message*) echo 0;; *new-session*) echo %1;; esac\nexit 0\n"
	if err := os.WriteFile(sv.tmuxPath, []byte(script), 0o755); err != nil { //nolint:gosec // test fixture, needs +x
		t.Fatal(err)
	}
	if err := sv.SpawnAgent(daemon.AgentSpawnSpec{Name: "survivor", WorkDir: lenientTempDir(t), Adopt: true}); err != nil {
		t.Fatal(err)
	}
	waitForLog(t, logPath, "show-options")

	if err := os.WriteFile(ended, nil, 0o600); err != nil { // the adopted session is gone
		t.Fatal(err)
	}
	waitFor(t, "two failed lookups for the fresh launch", func() bool { return claude.callCount() >= 2 })
	if strings.Contains(loggedLines(logPath), "new-session") {
		t.Fatal("a fresh launch started with claude missing")
	}

	claude.install("/late/bin/claude")
	if logged := waitForLog(t, logPath, "new-session"); !strings.Contains(logged, "/late/bin/claude") {
		t.Errorf("fresh launch did not run the late-installed claude:\n%s", logged)
	}
}
