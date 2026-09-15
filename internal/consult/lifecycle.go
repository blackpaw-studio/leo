package consult

import (
	"fmt"
	"os"
	"time"
)

func cloneRecord(record Record) Record {
	record.Turns = append([]Turn(nil), record.Turns...)
	if record.Notifications != nil {
		notifications := make(map[string]Notification, len(record.Notifications))
		for key, value := range record.Notifications {
			notifications[key] = value
		}
		record.Notifications = notifications
	}
	record.InputTokens = clonePtr(record.InputTokens)
	record.OutputTokens = clonePtr(record.OutputTokens)
	record.CostUSD = clonePtr(record.CostUSD)
	record.UsageTurns = clonePtr(record.UsageTurns)
	record.ToolCalls = clonePtr(record.ToolCalls)
	if record.RunningSince != nil {
		runningSince := *record.RunningSince
		record.RunningSince = &runningSince
	}
	return record
}

func clonePtr[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

// persistRecordLocked serializes a full lifecycle snapshot with its mutation.
// Callers hold d.mu, preventing a late running write from overtaking cancel.
func (d *Dispatcher) persistRecordLocked(state *runState) {
	if h, ok := state.handle.(recordHandle); ok {
		if err := h.SetRecord(cloneRecord(state.record)); err != nil {
			fmt.Fprintf(os.Stderr, "consult %s: recording: %v\n", state.record.ID, err)
		}
		return
	}
	if err := state.handle.SetStatus(state.record.Status); err != nil {
		fmt.Fprintf(os.Stderr, "consult %s: recording: %v\n", state.record.ID, err)
	}
}

func (d *Dispatcher) complete(state *runState, status Status, text string, cause error) {
	d.mu.Lock()
	if state.record.Status.Terminal() {
		status, text = state.record.Status, state.record.Text
	} else {
		state.record.Status, state.record.Text, state.record.EndedAt = status, text, d.now()
		state.record.foldActive(state.record.EndedAt)
		if cause != nil {
			state.record.Error = cause.Error()
		}
		d.completionCandidateLocked(state, transitionKey(state.record.ID, state.record.Mode, ""), status)
	}
	d.persistRecordLocked(state)
	recordID := state.record.ID
	d.mu.Unlock()
	if text != "" {
		_ = state.handle.SetText(text)
	}
	finish(state.handle, recordID, status, cause)
	d.mu.Lock()
	d.pruneTerminalRunsLocked()
	d.mu.Unlock()
}

func entryFromRecord(rec Record, now time.Time) Entry {
	return Entry{ID: rec.ID, Status: rec.Status, Elapsed: rec.Elapsed(now), Active: secondsDuration(rec.LiveActiveSeconds(now)), Text: rec.Text, Err: rec.Error, InputTokens: clonePtr(rec.InputTokens), OutputTokens: clonePtr(rec.OutputTokens), CostUSD: clonePtr(rec.CostUSD), UsageTurns: clonePtr(rec.UsageTurns), ToolCalls: clonePtr(rec.ToolCalls), Worktree: rec.Worktree, Branch: rec.Branch}
}

func interactiveEntry(rec Record, turnID string, now time.Time) Entry {
	e := Entry{ID: rec.ID, Status: rec.Status, Elapsed: rec.Elapsed(now), Active: secondsDuration(rec.LiveActiveSeconds(now)), Err: rec.Error, TurnID: turnID, InputTokens: clonePtr(rec.InputTokens), OutputTokens: clonePtr(rec.OutputTokens), CostUSD: clonePtr(rec.CostUSD), UsageTurns: clonePtr(rec.UsageTurns), ToolCalls: clonePtr(rec.ToolCalls), Worktree: rec.Worktree, Branch: rec.Branch}
	t := turnByID(rec, turnID)
	e.Outcome, e.Delivered, e.Text = t.Outcome, t.Delivered, t.Text
	activity := rec.HookActivity
	if activity.IsZero() {
		activity = t.StartedAt
	}
	if t.Outcome == "" && !activity.IsZero() && now.Sub(activity) >= stalledAfter {
		e.Stalled = true
	}
	return e
}

func secondsDuration(seconds float64) time.Duration {
	return time.Duration(seconds * float64(time.Second))
}

func (d *Dispatcher) stateRecord(state *runState) Record {
	d.mu.Lock()
	defer d.mu.Unlock()
	return cloneRecord(state.record)
}
