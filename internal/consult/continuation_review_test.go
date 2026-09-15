package consult

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSynthesizeLegacyHeadlessTurnPreservesTerminalStatusAndError(t *testing.T) {
	rec := Record{ID: "d-legacy", Status: StatusFailed, Error: "boom", Text: "partial", StartedAt: time.Now(), EndedAt: time.Now()}
	synthesizeOpeningTurn(&rec)
	if rec.Mode != ModeHeadless {
		t.Fatalf("mode = %q", rec.Mode)
	}
	turn := rec.Turns[0]
	if turn.Status != StatusFailed || turn.Error != "boom" || turn.Text != "partial" || turn.Outcome != TurnInterrupted {
		t.Fatalf("turn = %+v", turn)
	}
}

func TestRemovedCodexWorktreeResumeArgvIncludesRecreatedGitMetadata(t *testing.T) {
	repo, stateDir := gitTestRepo(t), t.TempDir()
	baseRaw, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	base := strings.TrimSpace(string(baseRaw))
	branch, id := "leo/resume-argv", "d-resume-argv"
	if out, err := exec.Command("git", "-C", repo, "branch", branch, base).CombinedOutput(); err != nil {
		t.Fatalf("branch: %v: %s", err, out)
	}
	worktree := filepath.Join(stateDir, "worktrees", id)
	rec := Record{ID: id, Mode: ModeHeadless, Kind: "dispatch", Template: "codex", Harness: "codex", Model: "gpt-5", Cwd: worktree, Status: StatusDone, SessionID: "thread", Isolation: "worktree", Worktree: worktree, Branch: branch, BaseCommit: base, RepositoryRoot: repo, WorktreeState: WorktreeRemoved, Turns: []Turn{{TurnID: id + "#1", Outcome: TurnFinished, Status: StatusDone}}}
	recorder := NewFileRecorder(stateDir)
	h, err := recorder.Open(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Close(StatusDone, nil); err != nil {
		t.Fatal(err)
	}
	d := NewDispatcher(recorder)
	d.runs[id] = &runState{record: rec, handle: h, done: closedTestChannel()}
	var argv []string
	d.ExecCommandContext = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		argv = append([]string(nil), args...)
		return exec.CommandContext(ctx, "printf", "%s", `{"type":"thread.started","thread_id":"thread"}
{"type":"item.completed","item":{"type":"agent_message","text":"done"}}`)
	}
	sent, err := d.SendWithConfig(context.Background(), testConfig(), id, "next")
	if err != nil {
		t.Fatal(err)
	}
	_ = d.Wait(context.Background(), []string{sent.TurnID}, time.Second)
	joined := strings.Join(argv, " ")
	for _, want := range []string{filepath.Join(repo, ".git"), filepath.Join(repo, ".git", "worktrees", filepath.Base(worktree))} {
		if !strings.Contains(joined, want) {
			t.Fatalf("argv missing %q: %s", want, joined)
		}
	}
}

type resumeErrorRecorder struct{ nopRecorder }

func (resumeErrorRecorder) Resume(Record) (Handle, error) { return nil, errors.New("resume failed") }

func TestContinuationRecorderFailureRestoresInvocationState(t *testing.T) {
	d := NewDispatcher(resumeErrorRecorder{})
	priorDone := closedTestChannel()
	state := &runState{record: Record{ID: "d-resume-fail", Mode: ModeHeadless, Kind: "dispatch", Template: "claude", Harness: "claude", Model: "opus", Cwd: t.TempDir(), Status: StatusDone, SessionID: "sid", Turns: []Turn{{TurnID: "d-resume-fail#1", Outcome: TurnFinished, Status: StatusDone}}}, done: priorDone}
	d.runs[state.record.ID] = state
	if _, err := d.SendWithConfig(context.Background(), testConfig(), state.record.ID, "next"); err == nil {
		t.Fatal("expected recorder failure")
	}
	if state.done != priorDone || state.record.Status != StatusDone || len(state.record.Turns) != 1 {
		t.Fatalf("state mutated: %+v", state.record)
	}
}

func TestOldTurnWaitCleanupUsesCapturedInvocationChannel(t *testing.T) {
	d := NewDispatcher(nil)
	oldDone := closedTestChannel()
	state := &runState{record: Record{ID: "d-wait", Mode: ModeHeadless, Status: StatusDone, Isolation: "worktree", WorktreeState: WorktreeKept, Turns: []Turn{{TurnID: "d-wait#1", Outcome: TurnFinished, Status: StatusDone}}}, done: oldDone}
	d.runs[state.record.ID] = state
	d.waitResolvedHook = func() {
		state.done = make(chan struct{})
		state.record.Status = StatusRunning
		state.record.Turns = append(state.record.Turns, Turn{TurnID: "d-wait#2"})
	}
	done := make(chan struct{})
	go func() { _ = d.Wait(context.Background(), []string{"d-wait#1"}, time.Second); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("old-turn wait blocked on newer invocation")
	}
}

func TestMarkInterruptedSettlesHeadlessTurnAndUsesTurnNotificationKey(t *testing.T) {
	stateDir := t.TempDir()
	r := NewFileRecorder(stateDir)
	rec := Record{ID: "d-restart-turn", Mode: ModeHeadless, Kind: "dispatch", Template: "claude", Harness: "claude", Status: StatusRunning, StartedAt: time.Now(), Notify: true, CallerPaneID: "%1", CallerHarness: "codex", Turns: []Turn{{TurnID: "d-restart-turn#1", Source: TurnSourceOrchestrator}}}
	h, err := r.Open(rec)
	if err != nil {
		t.Fatal(err)
	}
	_ = h.Close(StatusRunning, nil)
	d := NewDispatcher(r)
	d.MarkInterrupted()
	got, err := LoadOne(stateDir, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Turns[0].Outcome != TurnInterrupted || got.Turns[0].Status != StatusFailed {
		t.Fatalf("turn=%+v", got.Turns[0])
	}
	if _, ok := got.Notifications[rec.ID+"#1"]; !ok {
		t.Fatalf("notifications=%+v", got.Notifications)
	}
	if _, ok := got.Notifications[rec.ID]; ok {
		t.Fatalf("run-keyed notification=%+v", got.Notifications)
	}
}

func TestLegacyModeContinuationCompletesSecondTurn(t *testing.T) {
	recorder := NewFileRecorder(t.TempDir())
	d := NewDispatcher(recorder)
	d.ExecCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "printf", "%s", `{"type":"result","session_id":"sid","result":"next"}`)
	}
	state := &runState{record: Record{ID: "d-legacy-mode", Kind: "dispatch", Template: "claude", Harness: "claude", Model: "opus", Cwd: t.TempDir(), Status: StatusDone, SessionID: "sid", Text: "first"}, done: closedTestChannel()}
	h, err := recorder.Open(state.record)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Close(StatusDone, nil); err != nil {
		t.Fatal(err)
	}
	state.handle = h
	d.runs[state.record.ID] = state
	sent, err := d.SendWithConfig(context.Background(), testConfig(), state.record.ID, "next")
	if err != nil {
		t.Fatal(err)
	}
	got := d.Wait(context.Background(), []string{sent.TurnID}, time.Second)[0]
	if got.Status != StatusDone || got.Outcome != TurnFinished {
		t.Fatalf("entry=%+v", got)
	}
}

func TestHeadlessEntryUsesSelectedTurnsTerminalMetadata(t *testing.T) {
	rec := Record{ID: "d-turns", Status: StatusDone, Text: "new", Turns: []Turn{
		{TurnID: "d-turns#1", Outcome: TurnInterrupted, Status: StatusTimeout, Error: "old timeout", Text: "old"},
		{TurnID: "d-turns#2", Outcome: TurnFinished, Status: StatusDone, Text: "new"},
	}}
	got := headlessEntry(rec, "d-turns#1", time.Now())
	if got.Status != StatusTimeout || got.Err != "old timeout" || got.Text != "old" {
		t.Fatalf("entry = %+v", got)
	}
}
