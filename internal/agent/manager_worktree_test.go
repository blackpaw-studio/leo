package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/git"
)

// capturingSupervisor records calls so tests can assert ordering + rollback.
type capturingSupervisor struct {
	agents       map[string]ProcessState
	reservations map[string]struct{}
	spawnCall    *SpawnRequest
	spawnErr     error
	stopCalls    []string
	releaseCalls []string
}

func (s *capturingSupervisor) ReserveAgent(name string) error {
	if s.reservations == nil {
		s.reservations = map[string]struct{}{}
	}
	if _, exists := s.agents[name]; exists {
		return fmt.Errorf("agent %q already exists", name)
	}
	if _, reserved := s.reservations[name]; reserved {
		return fmt.Errorf("agent %q already reserved", name)
	}
	s.reservations[name] = struct{}{}
	return nil
}

func (s *capturingSupervisor) ReleaseAgent(name string) {
	s.releaseCalls = append(s.releaseCalls, name)
	delete(s.reservations, name)
}

func (s *capturingSupervisor) SpawnAgent(req SpawnRequest) error {
	spec := req
	s.spawnCall = &spec
	if s.spawnErr != nil {
		return s.spawnErr
	}
	if s.agents == nil {
		s.agents = map[string]ProcessState{}
	}
	delete(s.reservations, req.Name)
	s.agents[req.Name] = ProcessState{Name: req.Name, Status: "running"}
	return nil
}

func (s *capturingSupervisor) StopAgent(name string) error {
	s.stopCalls = append(s.stopCalls, name)
	delete(s.agents, name)
	return nil
}

func (s *capturingSupervisor) EphemeralAgents() map[string]ProcessState {
	if s.agents == nil {
		return map[string]ProcessState{}
	}
	return s.agents
}

// gitCall is one invocation of ExecGit captured for assertions.
type gitCall struct {
	repoPath string
	args     []string
}

// fakeGit replaces git.ExecGit during a test. It routes each subcommand to a
// canned result so Manager.Spawn's worktree flow can run without touching real
// git. Restore on t.Cleanup so tests don't leak the override.
type fakeGit struct {
	t        *testing.T
	calls    []gitCall
	branches map[string]git.BranchStatus
	// failWorktreeAdd, when non-empty, is returned verbatim as the stdout of
	// every `git worktree add`, simulating a git failure.
	failWorktreeAdd string
	// removeCalled records the worktree path each `git worktree remove` was
	// invoked with (used by rollback assertions).
	removeCalled []string
}

func installFakeGit(t *testing.T, branches map[string]git.BranchStatus) *fakeGit {
	t.Helper()
	f := &fakeGit{t: t, branches: branches}
	prev := git.ExecGit
	git.ExecGit = func(_ context.Context, repoPath string, args ...string) ([]byte, error) {
		f.calls = append(f.calls, gitCall{repoPath: repoPath, args: args})
		if len(args) == 0 {
			return nil, fmt.Errorf("fake git: empty args")
		}
		switch args[0] {
		case "fetch":
			return nil, nil
		case "symbolic-ref":
			// origin/HEAD -> origin/main
			return []byte("origin/main\n"), nil
		case "rev-parse":
			// rev-parse --verify --quiet refs/heads/<b> or refs/remotes/origin/<b>
			if len(args) < 4 {
				return nil, fmt.Errorf("rev-parse unexpected args: %v", args)
			}
			ref := args[len(args)-1]
			branch, remote := parseRef(ref)
			status := f.branches[branch]
			if (remote && status.Remote) || (!remote && status.Local) {
				return nil, nil
			}
			return nil, fmt.Errorf("exit 1: ref not found")
		case "worktree":
			if len(args) >= 2 && args[1] == "add" {
				if f.failWorktreeAdd != "" {
					return []byte(f.failWorktreeAdd), fmt.Errorf("exit 128")
				}
				return nil, nil
			}
			if len(args) >= 2 && args[1] == "remove" {
				// capture-worktree-path for rollback assertions; path is always last arg
				f.removeCalled = append(f.removeCalled, args[len(args)-1])
				return nil, nil
			}
			if len(args) >= 2 && args[1] == "prune" {
				return nil, nil
			}
		case "branch":
			return nil, nil
		case "status":
			return nil, nil
		}
		return nil, fmt.Errorf("fakeGit: unhandled args %v", args)
	}
	t.Cleanup(func() { git.ExecGit = prev })
	return f
}

// parseRef splits refs/heads/<b> or refs/remotes/origin/<b> into (branch, isRemote).
func parseRef(ref string) (string, bool) {
	const localP = "refs/heads/"
	const remoteP = "refs/remotes/origin/"
	if strings.HasPrefix(ref, remoteP) {
		return strings.TrimPrefix(ref, remoteP), true
	}
	if strings.HasPrefix(ref, localP) {
		return strings.TrimPrefix(ref, localP), false
	}
	return ref, false
}

// newWorktreeTestManager wires a Manager over a capturingSupervisor and a temp
// workspace. Pre-creates the canonical .git marker so EnsureCanonical skips
// the `gh repo clone` path.
func newWorktreeTestManager(t *testing.T, repoShort string) (*Manager, *capturingSupervisor, string) {
	t.Helper()
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	canonicalGit := filepath.Join(workspace, repoShort, ".git")
	if err := os.MkdirAll(canonicalGit, 0o750); err != nil {
		t.Fatalf("mkdir canonical: %v", err)
	}

	cfg := &config.Config{
		HomePath: home,
		Templates: map[string]config.TemplateConfig{
			"coding": {Workspace: workspace},
		},
	}
	sup := &capturingSupervisor{}
	loader := func() (*config.Config, error) { return cfg, nil }
	return New(loader, sup, "", ""), sup, home
}

func TestSpawnWorktreeNewBranch(t *testing.T) {
	mgr, sup, home := newWorktreeTestManager(t, "leo")
	installFakeGit(t, map[string]git.BranchStatus{})

	rec, err := mgr.Spawn(context.Background(), SpawnSpec{
		Template: "coding",
		Repo:     "blackpaw-studio/leo",
		Branch:   "feat/new",
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	if rec.Branch != "feat/new" {
		t.Errorf("rec.Branch = %q, want feat/new", rec.Branch)
	}
	if rec.CanonicalPath == "" {
		t.Error("CanonicalPath should be populated")
	}
	if rec.Workspace == rec.CanonicalPath {
		t.Error("Workspace should differ from CanonicalPath for worktree agents")
	}
	if !strings.HasSuffix(rec.Name, "-feat-new") {
		t.Errorf("agent name %q should end with -feat-new", rec.Name)
	}

	if sup.spawnCall == nil {
		t.Fatal("SpawnAgent not called")
	}
	if sup.spawnCall.WorkDir != rec.Workspace {
		t.Errorf("WorkDir = %q, want %q", sup.spawnCall.WorkDir, rec.Workspace)
	}

	// Verify persistence captured the worktree metadata.
	stored, err := agentstore.Load(agentstore.FilePath(home))
	if err != nil {
		t.Fatalf("agentstore.Load: %v", err)
	}
	got, ok := stored[rec.Name]
	if !ok {
		t.Fatalf("agentstore missing record for %q", rec.Name)
	}
	if got.Branch != "feat/new" || got.CanonicalPath == "" {
		t.Errorf("stored record = %+v, want Branch=feat/new with CanonicalPath", got)
	}
}

func TestSpawnWorktreeRejectsBareRepo(t *testing.T) {
	mgr, _, _ := newWorktreeTestManager(t, "anything")
	installFakeGit(t, nil)

	_, err := mgr.Spawn(context.Background(), SpawnSpec{
		Template: "coding",
		Repo:     "just-a-name",
		Branch:   "feat/x",
	})
	if !errors.Is(err, ErrWorktreeRequiresSlash) {
		t.Fatalf("want ErrWorktreeRequiresSlash, got %v", err)
	}
}

func TestSpawnWorktreeRollbackOnSupervisorFailure(t *testing.T) {
	mgr, sup, home := newWorktreeTestManager(t, "leo")
	fake := installFakeGit(t, map[string]git.BranchStatus{})
	sup.spawnErr = errors.New("supervisor boom")

	_, err := mgr.Spawn(context.Background(), SpawnSpec{
		Template: "coding",
		Repo:     "blackpaw-studio/leo",
		Branch:   "feat/rollback",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if len(fake.removeCalled) != 1 {
		t.Fatalf("expected 1 worktree remove call, got %v", fake.removeCalled)
	}
	// Persistence must not have been written on failure.
	if stored, _ := agentstore.Load(agentstore.FilePath(home)); len(stored) != 0 {
		t.Errorf("no agentstore record should be written on spawn failure, got %d", len(stored))
	}
}

func TestStopPreservesWorktreeRecord(t *testing.T) {
	mgr, _, home := newWorktreeTestManager(t, "leo")
	installFakeGit(t, map[string]git.BranchStatus{})

	rec, err := mgr.Spawn(context.Background(), SpawnSpec{
		Template: "coding",
		Repo:     "blackpaw-studio/leo",
		Branch:   "feat/preserve",
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	if err := mgr.Stop(rec.Name); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	stored, err := agentstore.Load(agentstore.FilePath(home))
	if err != nil {
		t.Fatalf("agentstore.Load: %v", err)
	}
	got, ok := stored[rec.Name]
	if !ok {
		t.Fatalf("worktree record should survive Stop; got %+v", stored)
	}
	if !got.Stopped {
		t.Errorf("stopped worktree record should have Stopped=true; got %+v", got)
	}
}

func TestStopRemovesSharedRecord(t *testing.T) {
	mgr, _, home := newWorktreeTestManager(t, "leo")
	installFakeGit(t, nil)

	rec, err := mgr.Spawn(context.Background(), SpawnSpec{
		Template: "coding",
		Repo:     "blackpaw-studio/leo",
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	if err := mgr.Stop(rec.Name); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	stored, _ := agentstore.Load(agentstore.FilePath(home))
	if _, ok := stored[rec.Name]; ok {
		t.Errorf("shared-workspace record should be removed on Stop")
	}
}

func TestPruneWorktreeHappyPath(t *testing.T) {
	mgr, _, home := newWorktreeTestManager(t, "leo")
	fake := installFakeGit(t, map[string]git.BranchStatus{})

	rec, err := mgr.Spawn(context.Background(), SpawnSpec{
		Template: "coding",
		Repo:     "blackpaw-studio/leo",
		Branch:   "feat/prune",
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if err := mgr.Stop(rec.Name); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if err := mgr.Prune(context.Background(), rec.Name, PruneOptions{}); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(fake.removeCalled) != 1 {
		t.Fatalf("expected 1 worktree remove, got %v", fake.removeCalled)
	}
	stored, _ := agentstore.Load(agentstore.FilePath(home))
	if _, ok := stored[rec.Name]; ok {
		t.Errorf("agentstore record should be gone after prune")
	}
}

func TestPruneRejectsRunningAgent(t *testing.T) {
	mgr, _, _ := newWorktreeTestManager(t, "leo")
	installFakeGit(t, map[string]git.BranchStatus{})

	rec, err := mgr.Spawn(context.Background(), SpawnSpec{
		Template: "coding",
		Repo:     "blackpaw-studio/leo",
		Branch:   "feat/running",
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	err = mgr.Prune(context.Background(), rec.Name, PruneOptions{})
	if !errors.Is(err, ErrAgentStillRunning) {
		t.Fatalf("want ErrAgentStillRunning, got %v", err)
	}
}

func TestPruneRejectsSharedAgent(t *testing.T) {
	mgr, _, _ := newWorktreeTestManager(t, "leo")
	installFakeGit(t, nil)

	rec, err := mgr.Spawn(context.Background(), SpawnSpec{
		Template: "coding",
		Repo:     "blackpaw-studio/leo",
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if err := mgr.Stop(rec.Name); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	err = mgr.Prune(context.Background(), rec.Name, PruneOptions{})
	// Shared-workspace agents don't survive Stop, so the agentstore lookup
	// fails with the generic "no record" error — not ErrNotWorktreeAgent
	// (which only fires for records present but without a Branch).
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestListIncludesStoppedWorktreeAgent(t *testing.T) {
	mgr, _, _ := newWorktreeTestManager(t, "leo")
	installFakeGit(t, map[string]git.BranchStatus{})

	rec, err := mgr.Spawn(context.Background(), SpawnSpec{
		Template: "coding",
		Repo:     "blackpaw-studio/leo",
		Branch:   "feat/list-stopped",
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if err := mgr.Stop(rec.Name); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	var found *Record
	for i, r := range mgr.List() {
		if r.Name == rec.Name {
			found = &mgr.List()[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("stopped worktree agent missing from List")
	}
	if found.Status != "stopped" {
		t.Errorf("status = %q, want stopped", found.Status)
	}
	if found.Branch != "feat/list-stopped" {
		t.Errorf("branch = %q, want feat/list-stopped", found.Branch)
	}
}
