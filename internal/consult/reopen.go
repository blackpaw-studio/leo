package consult

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/redact"
	"github.com/blackpaw-studio/leo/internal/session"
)

// reopenPlan is what a reopen launches with, settled before anything about
// the run changes.
type reopenPlan struct {
	cwd string
	// transcript is the claude session's transcript; "" for codex.
	transcript string
	secrets    map[string]string
	rt         InteractiveRuntime
}

// reopenLocked relaunches a parked run's own session for a follow-up, in its
// own cwd or worktree, with message as the opening turn (state table rows 3
// and 4). Every check that can refuse runs before anything changes, and a
// refusal takes no slot. With every slot taken the turn waits in the shared
// line like any queued dispatch. The caller holds the run's serial lock.
func (d *Dispatcher) reopenLocked(cfg *config.Config, rec Record, message string) (SendResult, error) {
	plan, err := d.planReopen(cfg, rec)
	if err != nil {
		return SendResult{}, err
	}
	if rec, err = d.awaitPreviousPaneGone(rec, plan.rt); err != nil {
		return SendResult{}, err
	}
	// Admission is atomic with going live, as for a start: nestMu is held
	// until the reopened state is registered.
	d.nestMu.Lock()
	nestHeld := true
	defer func() {
		if nestHeld {
			d.nestMu.Unlock()
		}
	}()
	exempt, err := d.admitNested(rec.ParentDispatchID)
	if err != nil {
		return SendResult{}, err
	}
	runCtx, cancel := context.WithCancel(d.daemonCtx)
	state, err := d.reopenState(rec, plan, exempt, cancel)
	if err != nil {
		cancel()
		return SendResult{}, err
	}
	waiter, acquired := d.slotsFor(state).AcquireOrEnqueue()
	d.mu.Lock()
	d.runs[rec.ID] = state
	t := d.openTurnLocked(state, TurnSourceOrchestrator, message, acquired)
	turnID := t.TurnID
	state.awaitingSlot = !acquired
	d.persistLocked(state, "status")
	d.persistLocked(state, "turn")
	d.mu.Unlock()
	d.nestMu.Unlock()
	nestHeld = false

	req := reopenRequest(rec, plan.cwd, message)
	if !acquired {
		go d.launchWhenSlotFree(runCtx, state, req, rec.Harness, rec.Model, cfg, plan.rt, turnID, waiter)
		return SendResult{TurnID: turnID, Queued: true}, nil
	}
	if _, err := d.launchInteractive(runCtx, state, req, rec.Harness, rec.Model, cfg, plan.rt, turnID); err != nil {
		return SendResult{TurnID: turnID}, fmt.Errorf("reopening dispatch %s: %w", rec.ID, err)
	}
	d.mu.Lock()
	delivered := turnByID(state.record, turnID).Delivered
	d.mu.Unlock()
	return SendResult{TurnID: turnID, Delivered: delivered}, nil
}

// planReopen runs every check a reopen can fail before it changes anything:
// the record is resumable, its template still runs the same harness, its
// workspace is still there, and (claude) its session's transcript is where
// the reopened claude will look for it.
func (d *Dispatcher) planReopen(cfg *config.Config, rec Record) (reopenPlan, error) {
	refuse := func(reason string) (reopenPlan, error) {
		return reopenPlan{}, invalidf("dispatch %s closed and is not resumable: %s; start a new dispatch", rec.ID, reason)
	}
	if reason := notResumableReason(rec); reason != "" {
		return refuse(reason)
	}
	if cfg == nil {
		return reopenPlan{}, errors.New("configuration is required to reopen a closed dispatch")
	}
	d.mu.Lock()
	rt := d.interactiveRuntime
	d.mu.Unlock()
	if rt == nil {
		return reopenPlan{}, invalidf("interactive runtime is not configured")
	}
	tmpl, ok := cfg.Templates[rec.Template]
	if !ok {
		return refuse(fmt.Sprintf("template %q no longer exists", rec.Template))
	}
	h, err := harness.Get(cfg.TemplateHarness(tmpl))
	if err != nil {
		return refuse(err.Error())
	}
	if h.Name() != rec.Harness {
		return refuse(fmt.Sprintf("template %q harness changed from %q to %q", rec.Template, rec.Harness, h.Name()))
	}
	env, secrets, err := cfg.DispatchLaunchEnv(tmpl, rec.InheritedEnvironments, d.Env)
	if err != nil {
		return reopenPlan{}, fmt.Errorf("template %q: %w", rec.Template, err)
	}
	cwd, recreate, err := d.resumeWorkspace(rec)
	if err != nil {
		return refuse(err.Error())
	}
	if recreate {
		// A parked run's worktree is never cleaned up; one recorded as
		// removed is not this run's to recreate.
		return refuse("its worktree was removed")
	}
	plan := reopenPlan{cwd: cwd, secrets: secrets, rt: rt}
	if h.Name() == "claude" {
		if plan.transcript, err = claudeReopenTranscript(env, cwd, rec); err != nil {
			return refuse(err.Error())
		}
	}
	return plan, nil
}

// claudeReopenTranscript finds the session's transcript and checks it sits
// under the config dir the reopened claude resolves: one launched under
// another account's dir would not find the session and start a fresh one.
func claudeReopenTranscript(env map[string]string, cwd string, rec Record) (string, error) {
	dir, err := session.ConfigDir(env)
	if err != nil {
		return "", err
	}
	dir = filepath.Clean(dir)
	path := rec.TranscriptPath
	if path == "" {
		path = filepath.Join(dir, "projects", session.ProjectSlug(cwd), rec.SessionID+".jsonl")
	}
	path = filepath.Clean(path)
	if !pathWithin(dir, path) {
		return "", fmt.Errorf("session %s's transcript %s is not under the claude config dir this dispatch would reopen with (%s)", rec.SessionID, path, dir)
	}
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		return "", fmt.Errorf("session %s's transcript is missing (%s)", rec.SessionID, path)
	}
	return path, nil
}

// reopenState builds the reopened run's live state from the parked record:
// fresh runtime fields, a new usage invocation, and the recording resumed.
// Nothing is registered yet; the recording's resume persists the record.
func (d *Dispatcher) reopenState(rec Record, plan reopenPlan, exempt bool, cancel context.CancelFunc) (*runState, error) {
	prospective := cloneRecord(rec)
	prospective.Status, prospective.EndedAt, prospective.Error = StatusQueued, time.Time{}, ""
	prospective.SlotExempt = exempt
	// Each pane counts its own usage from zero: the parked invocation keeps
	// the earlier panes' totals.
	prospective.UsageInvocations = append(prospective.UsageInvocations, InvocationUsage{})
	handle, err := d.recorder.Resume(cloneRecord(prospective))
	if err != nil {
		return nil, fmt.Errorf("reopening dispatch %s recording: %w", rec.ID, err)
	}
	state := newRunState(prospective, handle, make(chan struct{}), cancel)
	state.scrub = redact.NewSecrets(plan.secrets)
	state.reopenUnconfirmed, state.reopenedAt = true, d.now()
	// claude restores a resumed session's running cost, which the earlier
	// panes' invocations counted already.
	state.bridgedUsage.carried = countedCost(rec.UsageInvocations)
	if plan.transcript != "" {
		d.primeLiveUsage(state, rec.SessionID, plan.transcript)
	}
	return state, nil
}

// primeLiveUsage starts a reopened claude run's live counts at the end of
// its transcript: the resumed session appends to the same file, whose
// earlier turns are already counted.
func (d *Dispatcher) primeLiveUsage(s *runState, sessionID, path string) {
	s.live.transcriptPath, s.live.sessionID = path, sessionID
	if d.liveUsage.read == nil {
		return
	}
	if err := s.live.tally.drain(d.liveUsage.read, path); err != nil {
		fmt.Fprintf(os.Stderr, "dispatch %s: reading transcript to prime live usage: %v\n", s.record.ID, err)
	}
	s.live.baseIn, s.live.baseOut = s.live.tally.totals()
}

// reopenRequest is the launch request for a reopen. Isolation stays empty
// on purpose: a launch that fails must not clean up the parked worktree.
// A nested run's caller is left out, as Start leaves the root's out of its
// own request, so placement never resolves the root agent's session.
func reopenRequest(rec Record, cwd, message string) Request {
	caller := rec.Caller
	if rec.ParentDispatchID != "" {
		caller = ""
	}
	return Request{
		Template: rec.Template, Model: rec.Model, Effort: rec.Effort, Role: rec.Role, Name: rec.Name, Kind: rec.Kind,
		Prompt: message, Cwd: cwd, Timeout: rec.Timeout, Caller: caller, Mode: ModeInteractive,
		CallerPaneID: rec.CallerPaneID, CallerHarness: rec.CallerHarness, CallerSessionID: rec.CallerSessionID, CallerWindowID: rec.CallerWindowID,
		ParentDispatchID: rec.ParentDispatchID, CallerBridgeKey: rec.CallerBridgeKey, CallerBridgeLaunch: rec.CallerBridgeLaunch,
		resumeSessionID: rec.SessionID,
	}
}
