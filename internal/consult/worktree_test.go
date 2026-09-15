package consult

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
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
	d := worktreeDispatcher(t, stateDir, "sleep 30")
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

func TestMarkInterruptedPreservesRemovedWorktrees(t *testing.T) {
	stateDir := t.TempDir()
	recorder := NewFileRecorder(stateDir)
	for _, rec := range []Record{
		{ID: "d-removed", Template: "claude", Harness: "claude", Kind: "dispatch", Status: StatusDone, Isolation: "worktree", Worktree: "/gone", WorktreeState: WorktreeRemoved},
		{ID: "d-creating", Template: "claude", Harness: "claude", Kind: "dispatch", Status: StatusRunning, Isolation: "worktree", Worktree: filepath.Join(stateDir, "missing"), RepositoryRoot: gitTestRepo(t), WorktreeState: WorktreeCreating},
	} {
		h, err := recorder.Open(rec)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.Close(rec.Status, nil); err != nil {
			t.Fatal(err)
		}
	}
	d := NewDispatcher(recorder)
	d.MarkInterrupted()
	for _, id := range []string{"d-removed", "d-creating"} {
		rec, err := d.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if rec.WorktreeState != WorktreeRemoved {
			t.Fatalf("%s state = %q", id, rec.WorktreeState)
		}
	}
}

func TestCollectWaitsForIsolatedHeadlessReap(t *testing.T) {
	repo, stateDir := gitTestRepo(t), t.TempDir()
	d := worktreeDispatcher(t, stateDir, "sleep 0.2")
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "q", Cwd: repo, Isolation: "worktree"})
	if err != nil {
		t.Fatal(err)
	}
	var state *runState
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		d.mu.Lock()
		state = d.runs[started.ID]
		running := state.record.Status == StatusRunning
		d.mu.Unlock()
		if running {
			break
		}
	}
	d.mu.Lock()
	state.record.Status = StatusCanceled
	state.record.EndedAt = time.Now()
	d.persistRecordLocked(state)
	rec := cloneRecord(state.record)
	d.mu.Unlock()
	collected := make(chan struct{})
	go func() { d.Collect(rec); close(collected) }()
	select {
	case <-collected:
		t.Fatal("Collect returned before process reap")
	case <-time.After(50 * time.Millisecond):
	}
	select {
	case <-collected:
	case <-time.After(5 * time.Second):
		t.Fatal("Collect did not return after reap")
	}
}

func TestHeadlessBackgroundProcessGroupForcesRetention(t *testing.T) {
	requireProcessInventory(t)
	repo, stateDir := gitTestRepo(t), t.TempDir()
	d := worktreeDispatcher(t, stateDir, "sleep 5 </dev/null >/dev/null 2>&1 &")
	d.ProcessCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name != "ps" || !slices.Equal(args, []string{"-A", "-o", "pid=", "-o", "pgid=", "-o", "ppid="}) {
			t.Fatalf("process inventory argv = %q %q", name, args)
		}
		return exec.CommandContext(ctx, name, args...)
	}
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "q", Cwd: repo, Isolation: "worktree"})
	if err != nil {
		t.Fatal(err)
	}
	entry := d.Wait(context.Background(), []string{started.ID}, RunTimeout)[0]
	if entry.Worktree == "" {
		t.Fatalf("worktree removed while process group survived: %+v", entry)
	}
}

func TestHeadlessExitedProcessGroupAllowsRemoval(t *testing.T) {
	requireProcessInventory(t)
	repo, stateDir := gitTestRepo(t), t.TempDir()
	d := worktreeDispatcher(t, stateDir, "sleep 0.05 </dev/null >/dev/null 2>&1 & wait")
	d.ProcessCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name != "ps" || !slices.Equal(args, []string{"-A", "-o", "pid=", "-o", "pgid=", "-o", "ppid="}) {
			t.Fatalf("process inventory argv = %q %q", name, args)
		}
		return exec.CommandContext(ctx, name, args...)
	}
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "q", Cwd: repo, Isolation: "worktree"})
	if err != nil {
		t.Fatal(err)
	}
	entry := d.Wait(context.Background(), []string{started.ID}, RunTimeout)[0]
	rec, _ := d.Get(started.ID)
	if entry.Worktree != "" || rec.WorktreeState != WorktreeRemoved {
		state := d.runs[started.ID]
		out, _ := exec.Command("ps", "-A", "-o", "pid=", "-o", "pgid=", "-o", "ppid=").CombinedOutput()
		t.Fatalf("exited descendant retained worktree: pgid=%d inventory=%s entry=%+v record=%+v", state.pgid, out, entry, rec)
	}
}

func requireProcessInventory(t *testing.T) {
	t.Helper()
	if err := exec.Command("ps", "-A", "-o", "pid=", "-o", "pgid=", "-o", "ppid=").Run(); err != nil {
		t.Skipf("process inventory unavailable: %v", err)
	}
}

func TestProcessGroupInventoryExactArgvAndUncertainty(t *testing.T) {
	for name, command := range map[string][]string{
		"empty":     {"printf", ""},
		"failed":    {"false"},
		"malformed": {"printf", "not-process-data\n"},
	} {
		t.Run(name, func(t *testing.T) {
			d := NewDispatcher(nil)
			var gotName string
			var gotArgs []string
			d.ProcessCommand = func(ctx context.Context, executable string, args ...string) *exec.Cmd {
				gotName, gotArgs = executable, append([]string(nil), args...)
				return exec.CommandContext(ctx, command[0], command[1:]...)
			}
			present, known := d.processGroupPresent(4242)
			if gotName != "ps" || !slices.Equal(gotArgs, []string{"-A", "-o", "pid=", "-o", "pgid=", "-o", "ppid="}) {
				t.Fatalf("process inventory argv = %q %q", gotName, gotArgs)
			}
			if known {
				t.Fatalf("%s inventory unexpectedly known", name)
			}
			if present {
				t.Fatalf("%s inventory unexpectedly contains target group", name)
			}
		})
	}
}

func TestUnknownOrDaemonProcessGroupRetainsWorktree(t *testing.T) {
	for _, pgid := range []int{0, syscall.Getpgrp()} {
		t.Run(strconv.Itoa(pgid), func(t *testing.T) {
			d := NewDispatcher(nil)
			if d.headlessWritersGone(&runState{headlessStarted: true, pgid: pgid}) {
				t.Fatalf("unsafe pgid %d reported writer-free", pgid)
			}
		})
	}
}

func TestCancelTerminalHeadlessDoesNotSignalCachedProcessGroup(t *testing.T) {
	requireProcessInventory(t)
	repo, stateDir := gitTestRepo(t), t.TempDir()
	d := worktreeDispatcher(t, stateDir, "sleep 30 </dev/null >/dev/null 2>&1 &")
	d.ProcessCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name != "ps" || !slices.Equal(args, []string{"-A", "-o", "pid=", "-o", "pgid=", "-o", "ppid="}) {
			t.Fatalf("process inventory argv = %q %q", name, args)
		}
		return exec.CommandContext(ctx, name, args...)
	}
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "q", Cwd: repo, Isolation: "worktree"})
	if err != nil {
		t.Fatal(err)
	}
	entry := d.Wait(context.Background(), []string{started.ID}, RunTimeout)[0]
	if entry.Worktree == "" {
		t.Fatalf("initial collection removed live process-group worktree: %+v", entry)
	}
	rec, err := d.Cancel(started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.WorktreeState != WorktreeKept {
		t.Fatalf("cancel record = %+v", rec)
	}
	if _, err := os.Stat(rec.Worktree); err != nil {
		t.Fatalf("terminal cancel removed retained worktree: %v", err)
	}
}

func TestDetachedWriterOutsideProcessGroupIsDocumentedLimitation(t *testing.T) {
	t.Skip("portable process-group cleanup cannot contain a descendant that deliberately daemonizes with setsid; post-removal writes can be lost")
}

func TestIsolatedConsultCancellationReapsAndCleans(t *testing.T) {
	repo, stateDir := gitTestRepo(t), t.TempDir()
	d := NewDispatcher(NewFileRecorder(stateDir))
	for range maxConcurrent {
		d.sem <- struct{}{}
	}
	defer func() {
		for range maxConcurrent {
			<-d.sem
		}
	}()
	d.ExecCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "sleep 30")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := d.Consult(ctx, testConfig(), Request{Template: "claude", Prompt: "q", Cwd: repo, Isolation: "worktree"})
	if err == nil {
		t.Fatal("expected cancellation")
	}
	records := d.Records()
	if len(records) != 1 {
		t.Fatalf("records = %d", len(records))
	}
	if records[0].WorktreeState != WorktreeRemoved {
		t.Fatalf("record = %+v", records[0])
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
	wantRoots := []string{filepath.Join(repo, ".git"), filepath.Join(repo, ".git", "worktrees", filepath.Base(rec.Worktree)), filepath.Join(rec.Worktree, ".agents")}
	for i, want := range wantRoots {
		if canonical, err := filepath.EvalSymlinks(want); err == nil {
			wantRoots[i] = canonical
		} else if canonicalParent, parentErr := filepath.EvalSymlinks(filepath.Dir(want)); parentErr == nil {
			wantRoots[i] = filepath.Join(canonicalParent, filepath.Base(want))
		}
	}
	d.Wait(context.Background(), []string{started.ID}, RunTimeout)
	var roots map[string]bool
	for i := range gotArgs {
		if i > 0 && gotArgs[i-1] == "-c" && strings.HasPrefix(gotArgs[i], "sandbox_workspace_write.writable_roots=") {
			raw := strings.TrimPrefix(gotArgs[i], "sandbox_workspace_write.writable_roots=")
			roots = map[string]bool{}
			for _, item := range strings.Split(strings.Trim(raw, "[]"), ",") {
				value, err := strconv.Unquote(strings.TrimSpace(item))
				if err != nil {
					t.Fatal(err)
				}
				roots[value] = true
			}
		}
	}
	if roots == nil {
		t.Fatalf("writable roots argument missing: %v", gotArgs)
	}
	for _, want := range wantRoots {
		if !roots[want] {
			t.Errorf("writable roots missing exact member %q: %v", want, roots)
		}
	}
}

func worktreeDispatcher(t *testing.T, stateDir string, mutate string) *Dispatcher {
	t.Helper()
	d := NewDispatcher(NewFileRecorder(stateDir))
	d.ProcessCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name != "ps" || !slices.Equal(args, []string{"-A", "-o", "pid=", "-o", "pgid=", "-o", "ppid="}) {
			t.Fatalf("process inventory argv = %q %q", name, args)
		}
		return exec.CommandContext(ctx, "printf", "1 1 0\n")
	}
	d.ExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "ps" {
			return exec.CommandContext(ctx, name, args...)
		}
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
