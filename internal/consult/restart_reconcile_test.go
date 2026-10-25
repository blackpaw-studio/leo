package consult

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// markInterruptedAndReconcile runs a restart's fast pass, then drains the
// background pass it queued, as a daemon does once it is serving.
func markInterruptedAndReconcile(t *testing.T, d *Dispatcher) RestartSummary {
	t.Helper()
	summary := d.MarkInterrupted()
	d.ReconcileRestart(context.Background(), 1)
	return summary
}

// slowGitFor makes the dispatcher's git seam hang (sleep 5) for any call
// whose argv contains sub, and run real git otherwise.
func slowGitFor(d *Dispatcher, sub ...string) {
	d.GitCommand = func(name string, args ...string) *exec.Cmd {
		for i := range args {
			if i+len(sub) <= len(args) && slices.Equal(args[i:i+len(sub)], sub) {
				return exec.Command("sleep", "5")
			}
		}
		return exec.Command(name, args...)
	}
}

// captureStartupLog collects the dispatcher's startup log lines.
func captureStartupLog(d *Dispatcher) func() []string {
	var mu sync.Mutex
	var lines []string
	d.SetStartupLog(func(format string, args ...any) {
		mu.Lock()
		lines = append(lines, fmt.Sprintf(format, args...))
		mu.Unlock()
	})
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(lines)
	}
}

func timeoutLines(lines []string) []string {
	var out []string
	for _, line := range lines {
		if strings.Contains(line, "git timed out") {
			out = append(out, line)
		}
	}
	return out
}

func TestMarkInterruptedGitTimeoutKeepsWorktree(t *testing.T) {
	repo, stateDir := gitTestRepo(t), t.TempDir()
	// Released with no pane: a clean worktree at its base commit that the
	// restart would remove if its status check answered.
	rec := seedIsolated(t, repo, stateDir, "d-slow", func(r *Record) { r.Status = StatusReleased })
	d := NewDispatcher(NewFileRecorder(stateDir))
	d.restartGitTimeout = 50 * time.Millisecond
	slowGitFor(d, "status")
	logs := captureStartupLog(d)

	started := time.Now()
	markInterruptedAndReconcile(t, d)

	if took := time.Since(started); took > 3*time.Second {
		t.Fatalf("restart reconcile took %s; the git timeout did not bound the status call", took)
	}
	if !dirExists(rec.Worktree) || !branchExists(t, repo, rec.Branch) {
		t.Fatalf("worktree dir present=%v branch present=%v; a timed-out check must leave both", dirExists(rec.Worktree), branchExists(t, repo, rec.Branch))
	}
	if got, _ := LoadOne(stateDir, rec.ID); got.WorktreeState != WorktreeKept {
		t.Fatalf("worktree_state = %q, want kept", got.WorktreeState)
	}
	lines := timeoutLines(logs())
	if len(lines) != 1 || !strings.Contains(lines[0], "d-slow") || !strings.Contains(lines[0], "status --porcelain") || !strings.Contains(lines[0], "worktree left in place") {
		t.Fatalf("timeout log lines = %q, want one naming d-slow and its status call", lines)
	}
}

func TestReconcileCreatingTimeoutDefers(t *testing.T) {
	repo, stateDir := gitTestRepo(t), t.TempDir()
	// A headless run that died while its worktree was being created; the
	// checkout never appeared, so a listing that answered would mark it removed.
	rec := Record{
		ID: "d-make", Kind: "dispatch", Template: "claude", Mode: ModeHeadless, Status: StatusRunning, StartedAt: adoptNow,
		Isolation: "worktree", WorktreeState: WorktreeCreating, RepositoryRoot: repo, SourceCwd: repo,
		Worktree: filepath.Join(stateDir, worktreesDirName, "d-make"), Branch: "leo/d-make",
	}
	restartRecord(t, stateDir, rec)
	d := NewDispatcher(NewFileRecorder(stateDir))
	d.restartGitTimeout = 50 * time.Millisecond
	slowGitFor(d, "worktree", "list")
	logs := captureStartupLog(d)

	started := time.Now()
	d.MarkInterrupted()

	if took := time.Since(started); took > 3*time.Second {
		t.Fatalf("MarkInterrupted took %s; the git timeout did not bound the worktree listing", took)
	}
	if got, _ := LoadOne(stateDir, rec.ID); got.WorktreeState != WorktreeDeferred || got.Status != StatusFailed {
		t.Fatalf("status=%s worktree_state=%q, want failed / deferred", got.Status, got.WorktreeState)
	}
	if lines := timeoutLines(logs()); len(lines) != 1 || !strings.Contains(lines[0], "d-make") {
		t.Fatalf("timeout log lines = %q, want one naming d-make", lines)
	}
}

// countingRuntime counts every call the dispatcher makes into its runtime
// beyond the base interface's no-ops: each one stands for a tmux call.
type countingRuntime struct {
	*endStateRuntime
	calls atomic.Int64
}

func (r *countingRuntime) PanePresence(pane string) (PanePresence, error) {
	r.calls.Add(1)
	return r.endStateRuntime.PanePresence(pane)
}
func (r *countingRuntime) PaneAlive(pane string) (bool, error) {
	r.calls.Add(1)
	return r.endStateRuntime.PaneAlive(pane)
}
func (r *countingRuntime) Alive(pane string) bool {
	r.calls.Add(1)
	return r.endStateRuntime.Alive(pane)
}
func (r *countingRuntime) Kill(pane string) error {
	r.calls.Add(1)
	return r.endStateRuntime.Kill(pane)
}
func (r *countingRuntime) FindPaneByDispatchID(window, id string) (string, error) {
	r.calls.Add(1)
	return r.endStateRuntime.FindPaneByDispatchID(window, id)
}
func (r *countingRuntime) ProbeAdoption(pane, id string) (AdoptProbe, error) {
	r.calls.Add(1)
	return r.endStateRuntime.ProbeAdoption(pane, id)
}
func (r *countingRuntime) ReapplyLayout(target string) error {
	r.calls.Add(1)
	return r.endStateRuntime.ReapplyLayout(target)
}

func TestMarkInterruptedNoExecForTerminalRecords(t *testing.T) {
	d, rt, repo, stateDir := endStateFixture(t)
	// Keep only the records that were terminal at shutdown, and add three
	// in-flight records whose worktrees were being created in one repository.
	for _, id := range []string{"d-c-present", "d-c-absent", "d-r-headless", "d-r-nopane", "d-r-adopt"} {
		if err := os.Remove(filepath.Join(Dir(stateDir), id+".json")); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"d-c-1", "d-c-2", "d-c-3"} {
		restartRecord(t, stateDir, Record{ID: id, Kind: "dispatch", Template: "worker", Mode: ModeHeadless,
			Status: StatusRunning, StartedAt: adoptNow, Isolation: "worktree", WorktreeState: WorktreeCreating,
			RepositoryRoot: repo, SourceCwd: repo, Worktree: filepath.Join(stateDir, worktreesDirName, id), Branch: "leo/" + id})
	}
	counting := &countingRuntime{endStateRuntime: rt}
	d.SetInteractiveRuntime(counting)
	calls := recordGit(d)

	d.MarkInterrupted()

	if n := counting.calls.Load(); n != 0 {
		t.Errorf("fast pass made %d tmux calls, want 0", n)
	}
	if len(*calls) != 1 || !slices.Equal((*calls)[0][len((*calls)[0])-3:], []string{"worktree", "list", "--porcelain"}) {
		t.Errorf("fast pass git calls = %q, want exactly one worktree list", *calls)
	}
	for _, id := range []string{"d-c-1", "d-c-2", "d-c-3"} {
		if got, _ := LoadOne(stateDir, id); got.Status != StatusFailed || got.WorktreeState != WorktreeRemoved {
			t.Errorf("%s: status=%s worktree_state=%q, want failed / removed", id, got.Status, got.WorktreeState)
		}
	}
}
