package consult

// observedEffortLevels are the effort levels a harness may report running at.
var observedEffortLevels = map[string]bool{"low": true, "medium": true, "high": true, "xhigh": true, "max": true}

// ObservedEffortSink is a BridgeReportSink that records the effort a
// dispatch was seen running at; the Dispatcher is one.
type ObservedEffortSink interface {
	ApplyObservedEffort(id, level string)
}

// EffortLabel is how a dispatch's effort reads in a list: the requested
// effort; the observed one marked ~ when none was requested (the harness's
// default); both when the harness ran at another level than requested.
func (r Record) EffortLabel() string {
	switch {
	case r.Effort != "" && r.ObservedEffort != "" && r.ObservedEffort != r.Effort:
		return r.Effort + "→" + r.ObservedEffort
	case r.Effort != "":
		return r.Effort
	case r.ObservedEffort != "":
		return "~" + r.ObservedEffort
	}
	return ""
}

// ApplyObservedEffort records the effort an interactive dispatch reported
// running at. Unknown levels, and reports for unknown or settled runs, are
// ignored.
func (d *Dispatcher) ApplyObservedEffort(id, level string) {
	_, s, err := d.lookup(id)
	if err != nil || s == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if s.record.Mode != ModeInteractive || s.record.Status.Terminal() {
		return
	}
	d.applyObservedEffortLocked(s, level)
}

// applyObservedEffortLocked sets the run's observed effort and that of the
// turn it ran in: the latest one that started running.
func (d *Dispatcher) applyObservedEffortLocked(s *runState, level string) {
	if !observedEffortLevels[level] {
		return
	}
	changed := s.record.ObservedEffort != level
	s.record.ObservedEffort = level
	if i := currentTurnIndex(s.record.Turns); i >= 0 && s.record.Turns[i].ObservedEffort != level {
		s.record.Turns[i].ObservedEffort = level
		changed = true
	}
	if changed {
		d.persistLocked(s, "")
	}
}

// currentTurnIndex is the latest turn that started running (delivered, or
// typed by a user), so neither a queued turn nor the one before a fresh
// submission takes its effort; -1 when none has.
func currentTurnIndex(turns []Turn) int {
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].Delivered || turns[i].Source == TurnSourceUser {
			return i
		}
	}
	return -1
}

// effortFromPayload reads the effort a claude hook payload reports, as
// {"effort": {"level": "high"}}; "" when it reports none.
func effortFromPayload(p map[string]any) string {
	effort, ok := p["effort"].(map[string]any)
	if !ok {
		return ""
	}
	level, _ := effort["level"].(string)
	return level
}
