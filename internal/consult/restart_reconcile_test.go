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

// seedTerminalRecords persists n released interactive records whose worktree
// metadata points at existing directories, so each one's background
// assessment starts with a git call (and, with no real repository behind
// it, ends kept).
func seedTerminalRecords(t *testing.T, stateDir string, n int) []string {
	t.Helper()
	root := t.TempDir()
	ids := make([]string, 0, n)
	for i := range n {
		id := fmt.Sprintf("d-t-%02d", i)
		wt := filepath.Join(root, id)
		if err := os.MkdirAll(wt, 0o700); err != nil {
			t.Fatal(err)
		}
		restartRecord(t, stateDir, Record{ID: id, Kind: "dispatch", Template: "worker", Mode: ModeInteractive,
			Status: StatusReleased, StartedAt: adoptNow, Isolation: "worktree", WorktreeState: WorktreePresent,
			RepositoryRoot: root, Worktree: wt, BaseCommit: "base", Branch: "leo/" + id})
		ids = append(ids, id)
	}
	return ids
}

// gitGate is a git seam that holds every call until released, tracking how
// many it holds at once.
type gitGate struct {
	mu            sync.Mutex
	inFlight, max int
	entered       chan struct{}
	release       chan struct{}
	once          sync.Once
}

func newGitGate(t *testing.T, d *Dispatcher) *gitGate {
	g := &gitGate{entered: make(chan struct{}, 64), release: make(chan struct{})}
	t.Cleanup(g.open)
	d.GitCommand = func(string, ...string) *exec.Cmd {
		g.mu.Lock()
		g.inFlight++
		g.max = max(g.max, g.inFlight)
		g.mu.Unlock()
		g.entered <- struct{}{}
		<-g.release
		g.mu.Lock()
		g.inFlight--
		g.mu.Unlock()
		return exec.Command("false")
	}
	return g
}

// open releases every held call, and every later one.
func (g *gitGate) open() { g.once.Do(func() { close(g.release) }) }

func (g *gitGate) awaitEntered(t *testing.T, n int) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for i := range n {
		select {
		case <-g.entered:
		case <-deadline:
			t.Fatalf("only %d of %d git calls ever ran at once", i, n)
		}
	}
}

func (g *gitGate) maxInFlight() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.max
}

func recordBytes(t *testing.T, stateDir string, ids []string) map[string][]byte {
	t.Helper()
	out := make(map[string][]byte, len(ids))
	for _, id := range ids {
		b, err := os.ReadFile(filepath.Join(Dir(stateDir), id+".json"))
		if err != nil {
			t.Fatal(err)
		}
		out[id] = b
	}
	return out
}

func TestReconcileRestartBoundsConcurrency(t *testing.T) {
	stateDir := t.TempDir()
	seedTerminalRecords(t, stateDir, 12)
	d := NewDispatcher(NewFileRecorder(stateDir))
	g := newGitGate(t, d)
	d.MarkInterrupted()

	done := make(chan RestartReconcileSummary, 1)
	go func() { done <- d.ReconcileRestart(context.Background(), 4) }()
	g.awaitEntered(t, 4)
	g.open()
	summary := <-done

	if got := g.maxInFlight(); got != 4 {
		t.Fatalf("at most %d git calls ran at once, want 4", got)
	}
	if summary.Records != 12 || summary.Kept != 12 || summary.GitCalls != 12 || summary.Canceled {
		t.Fatalf("summary = %+v, want 12 records kept over 12 git calls", summary)
	}
}

func TestReconcileRestartStopsOnCancel(t *testing.T) {
	stateDir := t.TempDir()
	ids := seedTerminalRecords(t, stateDir, 12)
	d := NewDispatcher(NewFileRecorder(stateDir))
	g := newGitGate(t, d)
	d.MarkInterrupted()
	before := recordBytes(t, stateDir, ids)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan RestartReconcileSummary, 1)
	go func() { done <- d.ReconcileRestart(ctx, 4) }()
	g.awaitEntered(t, 4)
	cancel()
	g.open()
	summary := <-done

	if !summary.Canceled || summary.Records != 4 {
		t.Fatalf("summary = %+v, want canceled after the 4 jobs in flight", summary)
	}
	after := recordBytes(t, stateDir, ids)
	untouched := 0
	for _, id := range ids {
		if string(before[id]) == string(after[id]) {
			untouched++
		}
	}
	if untouched < len(ids)-4 {
		t.Fatalf("%d of %d records untouched; every record the pass did not reach must stay as loaded", untouched, len(ids))
	}
}

// sweepHookRuntime runs hook once its sweep has listed the run files and
// before it decides on any of them.
type sweepHookRuntime struct {
	*fakeInteractiveRuntime
	dir  string
	hook func()
}

func (r sweepHookRuntime) ReleaseRunFiles(string) {}

func (r sweepHookRuntime) SweepRunFiles(keep func(string) bool) int {
	matches, _ := filepath.Glob(filepath.Join(r.dir, dispatchSpillPrefix+"*.json"))
	r.hook()
	removed := 0
	for _, m := range matches {
		if id := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(m), dispatchSpillPrefix), ".json"); !keep(id) {
			_ = os.Remove(m)
			removed++
		}
	}
	return removed
}

func TestReconcileRestartKeepsSpillOfRunStartedDuringPass(t *testing.T) {
	stateDir, dir := t.TempDir(), t.TempDir()
	d := NewDispatcher(NewFileRecorder(stateDir))
	started := writeSpill(t, dir, "d-new")
	dead := writeSpill(t, dir, "d-dead")
	d.SetInteractiveRuntime(sweepHookRuntime{fakeInteractiveRuntime: &fakeInteractiveRuntime{}, dir: dir, hook: func() {
		// A dispatch starts once the daemon is serving: its run is
		// registered before its launch writes the spill.
		d.mu.Lock()
		d.runs["d-new"] = newRunState(Record{ID: "d-new", Kind: "dispatch", Mode: ModeInteractive, Status: StatusRunning}, nopHandle{}, make(chan struct{}), nil)
		d.mu.Unlock()
	}})
	d.MarkInterrupted()

	summary := d.ReconcileRestart(context.Background(), 4)

	if _, err := os.Stat(started); err != nil {
		t.Fatalf("spill of a run started during the pass was removed: %v", err)
	}
	assertGone(t, dead)
	if want := (SweepSummary{Kept: 1, Removed: 1}); summary.Sweep != want {
		t.Fatalf("sweep = %+v, want %+v", summary.Sweep, want)
	}
}

func TestReconcileRestartSkipsRunOwnedAfterFastPass(t *testing.T) {
	stateDir := t.TempDir()
	ids := seedTerminalRecords(t, stateDir, 1)
	d := NewDispatcher(NewFileRecorder(stateDir))
	calls := recordGit(d)
	d.MarkInterrupted()
	// A follow-up reopened the record once the daemon was serving: its run
	// owns the record now, and its own terminal transitions clean up.
	rec, err := LoadOne(stateDir, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	d.runs[rec.ID] = newRunState(rec, nopHandle{}, make(chan struct{}), nil)
	d.mu.Unlock()
	before := recordBytes(t, stateDir, ids)

	summary := d.ReconcileRestart(context.Background(), 4)

	if summary.Records != 1 || summary.Skipped != 1 || len(*calls) != 0 {
		t.Fatalf("summary = %+v git calls = %q, want the job skipped untouched", summary, *calls)
	}
	if after := recordBytes(t, stateDir, ids); string(after[rec.ID]) != string(before[rec.ID]) {
		t.Fatal("a skipped record was rewritten")
	}
}
