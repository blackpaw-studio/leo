package consult

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/harness"
)

// SendWithConfig sends to interactive sessions or starts a new native
// invocation for a completed headless dispatch.
func (d *Dispatcher) SendWithConfig(ctx context.Context, cfg *config.Config, id, message string) (SendResult, error) {
	rec, _, err := d.lookup(id)
	if err != nil {
		return SendResult{}, fmt.Errorf("unknown dispatch %s", id)
	}
	if rec.Mode == ModeInteractive {
		return d.Send(ctx, id, message)
	}
	return d.continueHeadless(cfg, rec, message)
}

func (d *Dispatcher) continueHeadless(cfg *config.Config, rec Record, message string) (SendResult, error) {
	unlock := d.serialLocks([]string{rec.ID})
	defer unlock()
	for _, ch := range message {
		if (ch < 0x20 && ch != '\n') || ch == 0x7f {
			return SendResult{}, invalidf("message contains control characters")
		}
	}
	if cfg == nil {
		return SendResult{}, errors.New("configuration is required for headless continuation")
	}
	// Refresh after acquiring the per-run serialization lock.
	rec, state, _ := d.lookup(rec.ID)
	if rec.Mode == "" {
		rec.Mode = ModeHeadless
	}
	if !rec.Status.Terminal() {
		return SendResult{}, fmt.Errorf("dispatch is %s", rec.Status)
	}
	if rec.SessionID == "" {
		return SendResult{}, errors.New("dispatch has no resumable session id")
	}
	if state != nil {
		select {
		case <-state.done:
		default:
			return SendResult{}, errors.New("dispatch process is still being reaped")
		}
	}
	tmpl, ok := cfg.Templates[rec.Template]
	if !ok {
		return SendResult{}, fmt.Errorf("template %q no longer exists", rec.Template)
	}
	h, err := harness.Get(cfg.TemplateHarness(tmpl))
	if err != nil {
		return SendResult{}, err
	}
	if h.Name() != rec.Harness {
		return SendResult{}, fmt.Errorf("template %q harness changed from %q to %q", rec.Template, rec.Harness, h.Name())
	}
	if len(h.SessionArgs(harness.SessionState{Mode: harness.SessionResume, ID: rec.SessionID})) == 0 {
		return SendResult{}, fmt.Errorf("harness %q does not support headless continuation", h.Name())
	}
	decoded, err := h.DecodeOptions(cfg.TemplateHarnessOptions(tmpl))
	if err != nil {
		return SendResult{}, fmt.Errorf("template %q harness_options: %w", rec.Template, err)
	}
	cwd, recreate, err := d.resumeWorkspace(rec)
	if err != nil {
		return SendResult{}, err
	}
	spec := harness.LaunchSpec{Kind: harness.KindTask, Name: rec.Name, Model: rec.Model, MaxTurns: cfg.TemplateMaxTurns(tmpl), Workspace: cwd, Prompt: message, Options: decoded, Dispatched: true, Session: harness.SessionState{Mode: harness.SessionResume, ID: rec.SessionID}}
	if spec.Name == "" {
		spec.Name = "dispatch"
	}
	_, err = h.Args(spec)
	if err != nil {
		return SendResult{}, fmt.Errorf("building %s resume args: %w", h.Name(), err)
	}
	harnessEnv, err := h.Env(spec)
	if err != nil {
		return SendResult{}, fmt.Errorf("building %s resume env: %w", h.Name(), err)
	}

	d.mu.Lock()
	if !d.trySlot() {
		d.mu.Unlock()
		return SendResult{}, errors.New("no capacity")
	}
	if recreate {
		if err := os.MkdirAll(filepath.Dir(rec.Worktree), dirPerm); err != nil {
			<-d.sem
			d.mu.Unlock()
			return SendResult{}, err
		}
		if _, err := d.git("-C", rec.RepositoryRoot, "worktree", "add", rec.Worktree, rec.Branch); err != nil {
			<-d.sem
			d.mu.Unlock()
			return SendResult{}, fmt.Errorf("recreating retained worktree: %w", err)
		}
	}
	// Rebuild after recreation: Codex discovers Git metadata while rendering
	// its sandbox writable roots.
	args, err := h.Args(spec)
	if err != nil {
		var rollbackErr error
		if recreate {
			rollbackErr = d.rollbackRecreatedWorktreeLocked(rec)
		}
		<-d.sem
		d.mu.Unlock()
		return SendResult{}, errors.Join(fmt.Errorf("building %s resume args after workspace preparation: %w", h.Name(), err), rollbackErr)
	}
	prospective := rec
	if state != nil {
		prospective = cloneRecord(state.record)
	}
	prospective.Mode = ModeHeadless
	if len(prospective.Turns) == 0 {
		synthesizeOpeningTurn(&prospective)
	}
	now := d.now()
	turn := Turn{TurnID: fmt.Sprintf("%s#%d", rec.ID, len(prospective.Turns)+1), Source: TurnSourceOrchestrator, StartedAt: now, Delivered: true, SlotHeld: true, Text: message}
	prospective.Turns = append(prospective.Turns, turn)
	prospective.Status, prospective.Text, prospective.Error = StatusQueued, "", ""
	prospective.EndedAt, prospective.Cwd = time.Time{}, cwd
	if prospective.WorktreeState == WorktreeRemoved {
		prospective.WorktreeState = WorktreePresent
	}
	prospective.UsageInvocations = append(prospective.UsageInvocations, InvocationUsage{})
	done := make(chan struct{})
	runCtx, cancel := context.WithCancel(d.daemonCtx)
	handle, openErr := d.recorder.Resume(cloneRecord(prospective))
	if openErr != nil {
		cancel()
		var rollbackErr error
		if recreate {
			rollbackErr = d.rollbackRecreatedWorktreeLocked(rec)
		}
		<-d.sem
		d.mu.Unlock()
		return SendResult{}, errors.Join(fmt.Errorf("reopening dispatch recording: %w", openErr), rollbackErr)
	}
	if state == nil {
		state = &runState{}
		d.runs[rec.ID] = state
	}
	state.record, state.handle = prospective, handle
	state.done, state.cancel = done, cancel
	d.persistLocked(state, "turn")
	d.mu.Unlock()
	go d.runInvocation(runCtx, state, done, h, rec.Model, tmpl.Env, args, harnessEnv, cwd, rec.Timeout, true)
	return SendResult{TurnID: turn.TurnID, Delivered: true}, nil
}

func (d *Dispatcher) rollbackRecreatedWorktreeLocked(rec Record) error {
	if _, err := d.git("-C", rec.RepositoryRoot, "worktree", "remove", rec.Worktree); err == nil {
		return nil
	} else {
		if state := d.runs[rec.ID]; state != nil {
			state.record.WorktreeState = WorktreeKept
			d.persistRecordLocked(state)
		}
		return fmt.Errorf("rolling back recreated worktree; retained as kept: %w", err)
	}
}

func synthesizeOpeningTurn(rec *Record) {
	if rec.Mode == "" {
		rec.Mode = ModeHeadless
	}
	outcome := TurnFinished
	if rec.Status != StatusDone {
		outcome = TurnInterrupted
	}
	turnID := rec.ID + "#1"
	rec.Turns = append(rec.Turns, Turn{TurnID: turnID, Source: TurnSourceOrchestrator, StartedAt: rec.StartedAt, EndedAt: rec.EndedAt, Delivered: true, Outcome: outcome, Status: rec.Status, Error: rec.Error, Text: rec.Text})
	if notification, ok := rec.Notifications[rec.ID]; ok {
		if rec.Notifications == nil {
			rec.Notifications = make(map[string]Notification)
		}
		if _, exists := rec.Notifications[turnID]; !exists {
			rec.Notifications[turnID] = notification
		}
		delete(rec.Notifications, rec.ID)
	}
}

func (d *Dispatcher) resumeWorkspace(rec Record) (string, bool, error) {
	if rec.Isolation != "worktree" {
		if info, err := os.Stat(rec.Cwd); err != nil || !info.IsDir() {
			return "", false, errors.New("dispatch workspace is unavailable")
		}
		return rec.Cwd, false, nil
	}
	switch rec.WorktreeState {
	case WorktreeKept:
		if info, err := os.Stat(rec.Worktree); err != nil || !info.IsDir() {
			return "", false, errors.New("retained worktree is unavailable")
		}
		return rec.Worktree, false, nil
	case WorktreeRemoved:
		head, err := d.git("-C", rec.RepositoryRoot, "rev-parse", "--verify", rec.Branch+"^{commit}")
		if err != nil || strings.TrimSpace(string(head)) != rec.BaseCommit {
			return "", false, errors.New("retained worktree branch no longer points at base commit")
		}
		return rec.Worktree, true, nil
	default:
		return "", false, fmt.Errorf("worktree is not retained (state %q)", rec.WorktreeState)
	}
}
