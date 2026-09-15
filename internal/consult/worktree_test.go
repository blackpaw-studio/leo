package consult

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func gitTestRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	for _, args := range [][]string{{"init"}, {"config", "user.email", "leo@example.test"}, {"config", "user.name", "Leo Test"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "add", "tracked.txt")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}
	cmd = exec.Command("git", "commit", "-m", "base")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, out)
	}
	return repo
}

func TestWorktreeCommittedChangeIsKept(t *testing.T) {
	repo, stateDir := gitTestRepo(t), t.TempDir()
	d := worktreeDispatcher(t, stateDir, "git config user.email leo@example.test; git config user.name 'Leo Test'; printf changed >> tracked.txt; git add tracked.txt; git commit -m changed >/dev/null")
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "q", Cwd: repo, Isolation: "worktree"})
	if err != nil {
		t.Fatal(err)
	}
	entry := d.Wait(context.Background(), []string{started.ID}, RunTimeout)[0]
	rec, _ := d.Get(started.ID)
	if rec.WorktreeState != WorktreeKept || entry.Worktree == "" {
		t.Fatalf("entry=%+v record=%+v", entry, rec)
	}
}

func TestWorktreeTrackedDirtyChangeIsKept(t *testing.T) {
	repo, stateDir := gitTestRepo(t), t.TempDir()
	d := worktreeDispatcher(t, stateDir, "printf dirty >> tracked.txt")
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "q", Cwd: repo, Isolation: "worktree"})
	if err != nil {
		t.Fatal(err)
	}
	entry := d.Wait(context.Background(), []string{started.ID}, RunTimeout)[0]
	if entry.Worktree == "" || entry.Branch == "" {
		t.Fatalf("dirty worktree not retained: %+v", entry)
	}
}

func TestWorktreeBranchCollisionRegeneratesSuffix(t *testing.T) {
	repo, stateDir := gitTestRepo(t), t.TempDir()
	cmd := exec.Command("git", "-C", repo, "branch", "leo/claude-dead")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("branch: %v: %s", err, out)
	}
	d := worktreeDispatcher(t, stateDir, "")
	suffixes := []string{"dead", "beef"}
	d.WorktreeSuffix = func() string { suffix := suffixes[0]; suffixes = suffixes[1:]; return suffix }
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "q", Cwd: repo, Isolation: "worktree"})
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := d.Get(started.ID)
	if rec.Branch != "leo/claude-beef" {
		t.Fatalf("branch = %q", rec.Branch)
	}
	d.Wait(context.Background(), []string{started.ID}, RunTimeout)
}

func TestWorktreeRejectsNonRepositoryAndUnbornHead(t *testing.T) {
	for name, setup := range map[string]func(*testing.T) string{
		"non-repository": func(t *testing.T) string { return t.TempDir() },
		"unborn": func(t *testing.T) string {
			dir := t.TempDir()
			cmd := exec.Command("git", "init")
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git init: %v: %s", err, out)
			}
			return dir
		},
	} {
		t.Run(name, func(t *testing.T) {
			d := worktreeDispatcher(t, t.TempDir(), "")
			if _, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "q", Cwd: setup(t), Isolation: "worktree"}); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestWorktreeCancelReapsBeforeCleanRemoval(t *testing.T) {
	repo, stateDir := gitTestRepo(t), t.TempDir()
	d := NewDispatcher(NewFileRecorder(stateDir))
	d.ExecCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "sleep 30")
	}
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "q", Cwd: repo, Isolation: "worktree"})
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		rec, _ := d.Get(started.ID)
		if rec.Status == StatusRunning {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	rec, err := d.Cancel(started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != StatusCanceled || rec.WorktreeState != WorktreeRemoved {
		t.Fatalf("record=%+v", rec)
	}
	if _, err := os.Stat(rec.Worktree); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists: %v", err)
	}
}

func TestMarkInterruptedReconcilesCreatingWorktree(t *testing.T) {
	repo, stateDir := gitTestRepo(t), t.TempDir()
	recorder := NewFileRecorder(stateDir)
	id, branch := "d-recovery", "leo/recovery-1234"
	baseCmd := exec.Command("git", "-C", repo, "rev-parse", "HEAD")
	baseRaw, err := baseCmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	base := strings.TrimSpace(string(baseRaw))
	wt := filepath.Join(stateDir, worktreesDirName, id)
	cmd := exec.Command("git", "-C", repo, "worktree", "add", "-b", branch, wt, base)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v: %s", err, out)
	}
	h, err := recorder.Open(Record{ID: id, Template: "claude", Harness: "claude", Model: "opus", Kind: "dispatch", Cwd: wt, Status: StatusRunning, StartedAt: time.Now(), Isolation: "worktree", Worktree: wt, Branch: branch, BaseCommit: base, SourceCwd: repo, RepositoryRoot: repo, WorktreeState: WorktreeCreating})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Close(StatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	d := NewDispatcher(recorder)
	d.MarkInterrupted()
	rec, err := d.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != StatusFailed || rec.WorktreeState != WorktreeKept {
		t.Fatalf("record=%+v", rec)
	}
}

func TestCodexWorktreeArgsIncludeLinkedAndCommonGitDirs(t *testing.T) {
	repo, stateDir := gitTestRepo(t), t.TempDir()
	d := NewDispatcher(NewFileRecorder(stateDir))
	var gotArgs []string
	d.ExecCommandContext = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		gotArgs = append([]string(nil), args...)
		return exec.CommandContext(ctx, "printf", `%s\n`, `{"type":"thread.started","thread_id":"t1"}`, `{"type":"item.completed","item":{"type":"agent_message","text":"ok"}}`)
	}
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "codex", Prompt: "q", Cwd: repo, Isolation: "worktree"})
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := d.Get(started.ID)
	d.Wait(context.Background(), []string{started.ID}, RunTimeout)
	joined := strings.Join(gotArgs, " ")
	for _, want := range []string{filepath.Join(repo, ".git"), filepath.Join(repo, ".git", "worktrees", filepath.Base(rec.Worktree)), filepath.Join(rec.Worktree, ".agents")} {
		canonical, _ := filepath.EvalSymlinks(want)
		if canonical == "" {
			canonical = want
		}
		if !strings.Contains(joined, canonical) {
			t.Errorf("args missing %q: %v", canonical, gotArgs)
		}
	}
}

func worktreeDispatcher(t *testing.T, stateDir string, mutate string) *Dispatcher {
	t.Helper()
	d := NewDispatcher(NewFileRecorder(stateDir))
	d.ExecCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		script := mutate + "\nprintf '%s\\n' '{\"type\":\"result\",\"result\":\"ok\",\"is_error\":false}'"
		return exec.CommandContext(ctx, "sh", "-c", script)
	}
	return d
}

func TestWorktreeCleanCollectionRemovesCheckoutAndKeepsBranch(t *testing.T) {
	repo, stateDir := gitTestRepo(t), t.TempDir()
	d := worktreeDispatcher(t, stateDir, "")
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "q", Cwd: repo, Name: "Review API", Isolation: "worktree"})
	if err != nil {
		t.Fatal(err)
	}
	entry := d.Wait(context.Background(), []string{started.ID}, RunTimeout)[0]
	rec, err := d.Get(started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Status != StatusDone || rec.WorktreeState != WorktreeRemoved {
		t.Fatalf("entry=%+v record=%+v", entry, rec)
	}
	disk, err := LoadOne(stateDir, started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if disk.WorktreeState != WorktreeRemoved {
		t.Fatalf("disk worktree state = %q", disk.WorktreeState)
	}
	if _, err := os.Stat(rec.Worktree); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists: %v", err)
	}
	cmd := exec.Command("git", "-C", repo, "show-ref", "--verify", "refs/heads/"+rec.Branch)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("branch removed: %v: %s", err, out)
	}
}

func TestWorktreeDirtyCollectionKeepsCheckoutAndReportsIt(t *testing.T) {
	repo, stateDir := gitTestRepo(t), t.TempDir()
	d := worktreeDispatcher(t, stateDir, "printf dirty > untracked.txt")
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "q", Cwd: repo, Isolation: "worktree"})
	if err != nil {
		t.Fatal(err)
	}
	entry := d.Wait(context.Background(), []string{started.ID}, RunTimeout)[0]
	rec, _ := d.Get(started.ID)
	if rec.WorktreeState != WorktreeKept || entry.Worktree != rec.Worktree || entry.Branch != rec.Branch {
		t.Fatalf("entry=%+v record=%+v", entry, rec)
	}
	disk, err := LoadOne(stateDir, started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if disk.WorktreeState != WorktreeKept {
		t.Fatalf("disk worktree state = %q", disk.WorktreeState)
	}
	if _, err := os.Stat(filepath.Join(rec.Worktree, "untracked.txt")); err != nil {
		t.Fatal(err)
	}
}

func TestWorktreeCreationUsesRepositoryRootFromSourceSubdirectory(t *testing.T) {
	repo, stateDir := gitTestRepo(t), t.TempDir()
	sub := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	d := worktreeDispatcher(t, stateDir, "")
	var gitCalls [][]string
	d.GitCommand = func(name string, args ...string) *exec.Cmd {
		gitCalls = append(gitCalls, append([]string{name}, args...))
		return exec.Command(name, args...)
	}
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "q", Cwd: sub, Name: "Odd / Name", Isolation: "worktree"})
	if err != nil {
		t.Fatal(err)
	}
	d.Wait(context.Background(), []string{started.ID}, RunTimeout)
	rec, _ := d.Get(started.ID)
	canonicalRepo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	if rec.RepositoryRoot != canonicalRepo || rec.SourceCwd != sub || started.Cwd != rec.Worktree {
		t.Fatalf("started=%+v record=%+v", started, rec)
	}
	wantPrefix := []string{"git", "-C", canonicalRepo, "worktree", "add", "-b", rec.Branch, rec.Worktree, rec.BaseCommit}
	if !slices.ContainsFunc(gitCalls, func(call []string) bool { return slices.Equal(call, wantPrefix) }) {
		t.Fatalf("git calls missing exact add argv %v: %v", wantPrefix, gitCalls)
	}
	if !strings.HasPrefix(rec.Branch, "leo/odd-name-") {
		t.Fatalf("branch = %q", rec.Branch)
	}
}
