package consult

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

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
	d.MarkInterrupted()

	if took := time.Since(started); took > 3*time.Second {
		t.Fatalf("MarkInterrupted took %s; the git timeout did not bound the status call", took)
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
