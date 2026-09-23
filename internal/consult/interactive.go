package consult

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
)

const (
	ackTimeout = 10 * time.Second
	// lateAckWindow covers observed Codex MCP-startup delays, including 8.5 minutes.
	lateAckWindow    = 30 * time.Minute
	unmatchedGrace   = 5 * time.Second
	finalReportGrace = 30 * time.Second
	idleCloseAfter   = time.Hour
	stalledAfter     = 10 * time.Minute
)

// LaunchRequest contains the already validated dispatch details needed by an
// interactive runtime. Step 5 supplies the tmux implementation.
type LaunchRequest struct {
	ID, Harness, Model, Effort, Cwd, Name         string
	Template, Caller                              string
	Prompt                                        string
	Timeout                                       time.Duration
	Dispatched                                    bool
	CallerPaneID, CallerSessionID, CallerWindowID string
	Placement                                     ViewerPlacement
}
type InteractiveRuntime interface {
	Launch(context.Context, LaunchRequest) (paneID, window string, err error)
	Inject(ctx context.Context, paneID string, text string, arm func() error) error
	Alive(paneID string) bool
	Kill(paneID string) error
	ComposerEmpty(paneID string) bool
}

type openingInteractiveRuntime interface {
	InjectOpening(ctx context.Context, paneID string, text string, arm func() error) error
}

// paneAliveRuntime distinguishes a missing pane from an unavailable tmux
// probe. Existing runtimes can keep the simpler Alive method; callers that
// make destructive decisions use this richer optional capability.
type paneAliveRuntime interface {
	PaneAlive(paneID string) (alive bool, err error)
}
type sessionAliveRuntime interface{ SessionAlive(string) (bool, error) }
type paneDispatchRuntime interface {
	FindPaneByDispatchID(windowID, dispatchID string) (string, error)
}
type viewerOverridesRuntime interface {
	ViewerOverrides(context.Context, string) ViewerOverrides
}

func paneAlive(rt InteractiveRuntime, paneID string) (alive, certain bool) {
	if prober, ok := rt.(paneAliveRuntime); ok {
		alive, err := prober.PaneAlive(paneID)
		return alive, err == nil
	}
	return rt.Alive(paneID), true
}

func panePresent(rt InteractiveRuntime, paneID string) (present, certain bool) {
	if prober, ok := rt.(panePresenceRuntime); ok {
		presence, err := prober.PanePresence(paneID)
		return presence != PaneAbsent, err == nil
	}
	return paneAlive(rt, paneID)
}

type HookReport struct {
	EventID string          `json:"event_id"`
	Payload json.RawMessage `json:"payload"`
}
type SendResult struct {
	TurnID    string `json:"turn_id"`
	Delivered bool   `json:"delivered"`
}
type pendingClose struct {
	outcome TurnOutcome
	text    string
	until   time.Time
}

const maxInteractiveDedup = 512

type interactiveRecordHandle interface {
	SetRecord(Record) error
	AppendEvent(string, any) error
}

func (d *Dispatcher) persistLocked(s *runState, event string) {
	if h, ok := s.handle.(interactiveRecordHandle); ok {
		if err := h.SetRecord(cloneRecord(s.record)); err != nil {
			fmt.Printf("consult %s: recording: %v\n", s.record.ID, err)
			return
		}
		if event == "" {
			return
		}
		var data any = s.record.Status
		if event == "turn" && len(s.record.Turns) > 0 {
			data = s.record.Turns[len(s.record.Turns)-1]
		}
		_ = h.AppendEvent(event, data)
	} else {
		_ = s.handle.SetStatus(s.record.Status)
	}
}

func (d *Dispatcher) persistTurnLocked(s *runState, turn Turn) {
	if h, ok := s.handle.(interactiveRecordHandle); ok {
		if err := h.SetRecord(cloneRecord(s.record)); err != nil {
			fmt.Printf("consult %s: recording: %v\n", s.record.ID, err)
			return
		}
		_ = h.AppendEvent("turn", turn)
		return
	}
	_ = s.handle.SetStatus(s.record.Status)
}

func (d *Dispatcher) SetInteractiveRuntime(r InteractiveRuntime) {
	d.mu.Lock()
	d.interactiveRuntime = r
	d.mu.Unlock()
}

func (d *Dispatcher) startInteractive(ctx context.Context, s *runState, req Request, harnessName, model string, cfg *config.Config) (Started, error) {
	prompt := requestPrompt(req)
	d.mu.Lock()
	rt := d.interactiveRuntime
	d.mu.Unlock()
	if rt == nil {
		d.mu.Lock()
		t := d.openTurnLocked(s, TurnSourceOrchestrator, prompt, false)
		d.closeTurnLocked(s, t.TurnID, TurnRejected, "interactive runtime is not configured")
		d.finishInteractiveLocked(s, StatusFailed)
		d.mu.Unlock()
		return Started{}, invalidf("interactive runtime is not configured")
	}
	if harnessName == "opencode" {
		return Started{}, invalidf("interactive dispatch is not supported by harness %q", harnessName)
	}
	if !d.trySlot() {
		d.mu.Lock()
		t := d.openTurnLocked(s, TurnSourceOrchestrator, prompt, false)
		d.closeTurnLocked(s, t.TurnID, TurnRejected, "no capacity")
		d.finishInteractiveLocked(s, StatusFailed)
		d.mu.Unlock()
		return Started{ID: s.record.ID, Harness: harnessName, Model: model, Cwd: req.Cwd}, nil
	}
	d.mu.Lock()
	t := d.openTurnLocked(s, TurnSourceOrchestrator, prompt, true)
	d.persistLocked(s, "status")
	d.persistLocked(s, "turn")
	d.mu.Unlock()
	overrides := ViewerOverrides{}
	if provider, ok := rt.(viewerOverridesRuntime); ok && req.CallerSessionID != "" {
		overrides = provider.ViewerOverrides(ctx, req.CallerSessionID)
	}
	d.mu.Lock()
	placementRecord := cloneRecord(s.record)
	d.mu.Unlock()
	placement := d.placement.Decide(placementRecord, overrides, cfg, d.Records)
	d.mu.Lock()
	s.record.ViewerKind = placement.Kind
	s.record.ViewerTitle = viewerWindowName(s.record)
	d.persistLocked(s, "")
	placementRecord = cloneRecord(s.record)
	d.mu.Unlock()
	pane, window, err := rt.Launch(ctx, LaunchRequest{ID: s.record.ID, Harness: harnessName, Model: model, Effort: req.Effort, Cwd: req.Cwd, Name: req.Name, Template: req.Template, Caller: req.Caller, Prompt: req.Prompt, Timeout: req.Timeout, Dispatched: true, CallerPaneID: req.CallerPaneID, CallerSessionID: req.CallerSessionID, CallerWindowID: req.CallerWindowID, Placement: placement})
	if err != nil {
		d.placement.Cancel(placementRecord.ID)
		d.mu.Lock()
		d.closeTurnLocked(s, t.TurnID, TurnRejected, err.Error())
		d.finishInteractiveLocked(s, StatusFailed)
		d.mu.Unlock()
		return Started{}, err
	}
	published := d.placement.Publish(placementRecord.ID, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		if s.record.Status != StatusQueued {
			return false
		}
		s.record.PaneID = pane
		if placed, ok := rt.(interface{ ViewerKind(string) string }); ok {
			placement.Kind = placed.ViewerKind(pane)
		}
		s.record.ViewerKind = placement.Kind
		if placement.Kind == "window" {
			s.record.ViewerWindowID = window
			s.record.ViewerTitle = ""
		}
		d.persistLocked(s, "")
		return true
	})
	if !published {
		d.mu.Lock()
		s.record.ViewerKind = placement.Kind
		s.record.PaneID = pane
		d.persistLocked(s, "")
		placementRecord = cloneRecord(s.record)
		d.mu.Unlock()
		_, closeErr := d.closeRecordedPane(placementRecord, pane, rt.Kill, runtimeLayout(rt))
		if closeErr == nil {
			d.placement.Cancel(placementRecord.ID)
		}
		return Started{}, context.Canceled
	}
	go d.injectOpening(ctx, s, rt, t.TurnID, pane, prompt)
	return Started{ID: s.record.ID, Harness: harnessName, Model: model, Cwd: req.Cwd, Window: window}, nil
}

// injectOpening deliberately runs after Start returns: the TUI's readiness
// probe can take a minute, while launch itself is the only synchronous error.
func (d *Dispatcher) injectOpening(ctx context.Context, s *runState, rt InteractiveRuntime, turnID, pane, prompt string) {
	if d.beforeOpeningInject != nil {
		d.beforeOpeningInject()
	}
	if d.afterOpeningInject != nil {
		defer d.afterOpeningInject()
	}
	d.mu.Lock()
	settled := s.record.Status.Terminal() || s.record.Status == StatusSettling
	paneChanged := s.record.PaneID != pane
	t := turnByID(s.record, turnID)
	injectable := !settled && !paneChanged && t.Outcome == "" && s.record.Status == StatusQueued
	d.mu.Unlock()
	if !injectable {
		// A hook can close the opening turn before this goroutine is scheduled.
		// That advances the same live session; it does not orphan its pane.
		if !settled && !paneChanged {
			return
		}
		d.mu.Lock()
		rec := cloneRecord(s.record)
		d.mu.Unlock()
		_, _ = d.closeRecordedPane(rec, pane, rt.Kill, runtimeLayout(rt))
		return
	}
	injection := rt.Inject
	if opening, ok := rt.(openingInteractiveRuntime); ok {
		injection = opening.InjectOpening
	}
	err := injection(ctx, pane, prompt, func() error {
		d.mu.Lock()
		defer d.mu.Unlock()
		if s.record.Status.Terminal() || s.record.Status == StatusSettling || turnByID(s.record, turnID).Outcome != "" {
			return errors.New("dispatch settled during injection")
		}
		d.armTurnLocked(s, turnID)
		d.persistLocked(s, "turn")
		return nil
	})
	if err == nil {
		return
	}
	d.mu.Lock()
	// Opening injection is asynchronous. A late error belongs only to the
	// opening turn; never let it settle a run that has since advanced.
	if !s.record.Status.Terminal() && s.record.Status != StatusSettling &&
		len(s.record.Turns) > 0 &&
		s.record.Turns[len(s.record.Turns)-1].TurnID == turnID &&
		s.record.Turns[len(s.record.Turns)-1].Outcome == "" {
		d.closeTurnLocked(s, turnID, TurnRejected, err.Error())
		d.finishInteractiveLocked(s, StatusFailed)
	} else {
		fmt.Fprintf(os.Stderr, "dispatch %s: ignoring late opening injection error: %v\n", s.record.ID, err)
	}
	d.mu.Unlock()
}

func (d *Dispatcher) trySlot() bool {
	select {
	case d.sem <- struct{}{}:
		return true
	default:
		return false
	}
}
func (d *Dispatcher) openTurnLocked(s *runState, source TurnSource, text string, held bool) *Turn {
	d.expireArmedLocked(s)
	for i := range s.record.Turns {
		if s.record.Turns[i].Source == TurnSourceOrchestrator && s.record.Turns[i].Outcome == "" && !s.record.Turns[i].Delivered {
			d.closeTurnLocked(s, s.record.Turns[i].TurnID, TurnLost, "")
		}
	}
	t := Turn{TurnID: fmt.Sprintf("%s#%d", s.record.ID, len(s.record.Turns)+1), Source: source, StartedAt: d.now(), Text: text, SlotHeld: held}
	s.record.Turns = append(s.record.Turns, t)
	s.record.Status = StatusQueued
	if source == TurnSourceUser {
		s.record.Steered = true
		s.record.Status = StatusRunning
		s.record.startActive(d.now())
	}
	return &s.record.Turns[len(s.record.Turns)-1]
}
func (d *Dispatcher) expireArmedLocked(s *runState) {
	if s.armedTurn != "" && !d.now().Before(s.armedUntil) {
		s.armedTurn = ""
		s.armedUntil = time.Time{}
	}
}
func (d *Dispatcher) closeTurnLocked(s *runState, id string, outcome TurnOutcome, text string) bool {
	oldStatus := s.record.Status
	boundary := d.now()
	for i := range s.record.Turns {
		t := &s.record.Turns[i]
		if t.TurnID != id || t.Outcome != "" {
			continue
		}
		t.Outcome = outcome
		t.EndedAt = boundary
		if text != "" {
			t.Text = text
		}
		if t.SlotHeld {
			t.SlotHeld = false
			select {
			case <-d.sem:
			default:
			}
		}
		if s.armedTurn == id {
			s.armedTurn = ""
			s.armedUntil = time.Time{}
		}
		if !s.record.Status.Terminal() && s.record.Status != StatusSettling {
			s.record.Status = d.interactiveStatusLocked(s, boundary)
		}
		if !d.hasWorkingTurnLocked(s) {
			s.record.foldActive(boundary)
		}
		notificationStatus := turnNotificationStatus(s.record, t.TurnID)
		if outcome == TurnInterrupted && s.record.Status == StatusSettling && s.settleStatus == StatusTimeout {
			notificationStatus = StatusTimeout
		}
		d.completionCandidateLocked(s, t.TurnID, notificationStatus)
		d.persistTurnLocked(s, *t)
		if s.record.Status != oldStatus {
			d.persistLocked(s, "status")
		}
		return true
	}
	return false
}

func (d *Dispatcher) hasWorkingTurnLocked(s *runState) bool {
	for _, t := range s.record.Turns {
		if t.Outcome == "" && (t.Source == TurnSourceUser || t.Delivered) {
			return true
		}
	}
	return false
}

func (d *Dispatcher) interactiveStatusLocked(s *runState, boundary time.Time) Status {
	for _, t := range s.record.Turns {
		if t.Outcome == "" {
			return StatusRunning
		}
	}
	s.idleSince = boundary
	return StatusIdle
}

func (d *Dispatcher) Send(ctx context.Context, id, message string) (SendResult, error) {
	unlockSerial := d.serialLocks([]string{id})
	defer unlockSerial()
	for _, ch := range message {
		if (ch < 0x20 && ch != '\n') || ch == 0x7f {
			return SendResult{}, invalidf("message contains control characters")
		}
	}
	_, s, err := d.lookup(id)
	if err != nil || s == nil {
		return SendResult{}, fmt.Errorf("unknown dispatch %s", id)
	}
	d.mu.Lock()
	if s.record.Mode != ModeInteractive {
		d.mu.Unlock()
		return SendResult{}, errors.New("dispatch is not interactive")
	}
	if s.record.Status != StatusIdle {
		st := s.record.Status
		d.mu.Unlock()
		return SendResult{}, fmt.Errorf("dispatch is %s", st)
	}
	if s.releasing {
		d.mu.Unlock()
		return SendResult{}, errors.New("dispatch is being released")
	}
	if !d.trySlot() {
		t := d.openTurnLocked(s, TurnSourceOrchestrator, message, false)
		d.closeTurnLocked(s, t.TurnID, TurnRejected, "no capacity")
		d.mu.Unlock()
		return SendResult{TurnID: t.TurnID}, errors.New("no capacity")
	}
	t := d.openTurnLocked(s, TurnSourceOrchestrator, message, true)
	pane := s.record.PaneID
	rt := d.interactiveRuntime
	d.persistLocked(s, "turn")
	d.mu.Unlock()
	if rt == nil {
		d.mu.Lock()
		d.closeTurnLocked(s, t.TurnID, TurnRejected, "interactive runtime unavailable")
		d.mu.Unlock()
		return SendResult{TurnID: t.TurnID}, errors.New("interactive runtime unavailable")
	}
	if d.beforeSendInjectable != nil {
		d.beforeSendInjectable()
	}
	if !d.injectable(s, t.TurnID, pane) {
		d.mu.Lock()
		d.closeTurnLocked(s, t.TurnID, TurnRejected, "dispatch settled")
		d.mu.Unlock()
		return SendResult{TurnID: t.TurnID}, context.Canceled
	}
	err = rt.Inject(ctx, pane, message, func() error {
		d.mu.Lock()
		defer d.mu.Unlock()
		if s.record.Status == StatusSettling || s.record.Status.Terminal() || turnByID(s.record, t.TurnID).Outcome != "" {
			return errors.New("dispatch settled during injection")
		}
		if !s.record.Status.Terminal() {
			d.armTurnLocked(s, t.TurnID)
			d.persistLocked(s, "turn")
		}
		return nil
	})
	if err != nil {
		d.mu.Lock()
		d.closeTurnLocked(s, t.TurnID, TurnRejected, err.Error())
		d.mu.Unlock()
		return SendResult{TurnID: t.TurnID}, err
	}
	d.mu.Lock()
	delivered := turnByID(s.record, t.TurnID).Delivered
	d.mu.Unlock()
	return SendResult{TurnID: t.TurnID, Delivered: delivered}, nil
}

// injectable is the final pre-side-effect gate. It deliberately checks both
// the session and the particular turn because cancellation may race launch.
func (d *Dispatcher) injectable(s *runState, turnID, pane string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if s.record.Status.Terminal() || s.record.Status == StatusSettling || s.record.PaneID != pane {
		return false
	}
	t := turnByID(s.record, turnID)
	return t.Outcome == "" && s.record.Status == StatusQueued
}

func (d *Dispatcher) Report(id string, r HookReport) error {
	_, s, err := d.lookup(id)
	if err != nil || s == nil {
		fmt.Fprintf(os.Stderr, "dispatch %s: ignoring report for unknown run\n", id)
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if s.record.Mode != ModeInteractive || s.record.Status.Terminal() || s.releasing {
		fmt.Fprintf(os.Stderr, "dispatch %s: ignoring report for settled run\n", id)
		return nil
	}
	var p map[string]any
	if json.Unmarshal(r.Payload, &p) != nil {
		return nil
	}
	event := str(p, "hook_event_name")
	event = strings.ToLower(strings.ReplaceAll(event, "_", ""))
	if event == "" {
		return nil
	}
	if s.pendingCloses == nil {
		s.pendingCloses = map[string]pendingClose{}
	}
	if s.closedHarness == nil {
		s.closedHarness = map[string]bool{}
	}
	priorHook := s.record.HookActivity
	if r.EventID != "" { // keep a bounded dedup set in pending map namespace
		key := "@" + r.EventID
		if _, ok := s.pendingCloses[key]; ok {
			fmt.Fprintf(os.Stderr, "dispatch %s: ignoring duplicate report %s\n", id, r.EventID)
			return nil
		}
		s.pendingCloses[key] = pendingClose{}
		s.eventIDs = append(s.eventIDs, key)
		if len(s.eventIDs) > maxInteractiveDedup {
			delete(s.pendingCloses, s.eventIDs[0])
			s.eventIDs = s.eventIDs[1:]
		}
	}
	s.lastHook = d.now()
	s.record.HookActivity = s.lastHook
	if s.record.SessionID == "" {
		s.record.SessionID = str(p, "session_id")
	}
	switch event {
	case "userpromptsubmit":
		oldStatus := s.record.Status
		if s.record.Status == StatusSettling {
			return nil
		}
		hid := str(p, "turn_id")
		if hid == "" {
			hid = str(p, "harness_turn_id")
		}
		if hid != "" && s.closedHarness[hid] {
			fmt.Fprintf(os.Stderr, "dispatch %s: ignoring submit for closed harness turn %s\n", id, hid)
			return nil
		}
		var delivered *Turn
		if s.armedTurn != "" && d.now().Before(s.armedUntil) {
			delivered = d.deliverTurnLocked(s, s.armedTurn, hid)
		} else {
			prompt := str(p, "prompt")
			attributed, matched := false, (*Turn)(nil)
			if prompt != "" {
				matched = d.deliverOldestMatchingTurnLocked(s, prompt, hid)
				attributed = matched != nil
			}
			if !attributed {
				if hid == "" && !priorHook.IsZero() && d.now().Sub(priorHook) >= stalledAfter {
					for i := range s.record.Turns {
						t := &s.record.Turns[i]
						if t.Source == TurnSourceOrchestrator && t.Delivered && t.Outcome == "" {
							d.closeTurnLocked(s, t.TurnID, TurnLost, "")
							break
						}
					}
				}
				if hid == "" && hasWorkingTurnLocked(s) {
					// Claude carries no turn id, and its background-task
					// auto-continuation submits mid-turn before a single
					// Stop. Fold it into the working turn; a separate turn
					// would never close (#211).
					d.persistLocked(s, "")
					if s.record.Status != oldStatus {
						d.persistLocked(s, "status")
					}
					return nil
				}
				t := d.openTurnLocked(s, TurnSourceUser, "", false)
				t.HarnessTurnID = hid
			} else {
				delivered = matched
			}
		}
		closeApplied := false
		if hid != "" {
			if pc, ok := s.pendingCloses[hid]; ok && d.now().Before(pc.until) {
				closeApplied = d.closeHarnessLocked(s, hid, pc.outcome, pc.text)
			}
			delete(s.pendingCloses, hid)
		}
		if delivered != nil && !closeApplied {
			d.persistTurnLocked(s, *delivered)
		} else if !closeApplied {
			d.persistLocked(s, "turn")
		}
		if s.record.Status != oldStatus {
			d.persistLocked(s, "status")
		}
	case "stop", "interrupt":
		out := TurnFinished
		if event == "interrupt" {
			out = TurnInterrupted
		}
		hid := str(p, "turn_id")
		if hid == "" {
			hid = str(p, "harness_turn_id")
		}
		if hid != "" && s.closedHarness[hid] {
			fmt.Fprintf(os.Stderr, "dispatch %s: ignoring close for closed harness turn %s\n", id, hid)
			return nil
		}
		text := str(p, "last_assistant_message")
		if hid == "" {
			d.closeOldestLocked(s, out, text)
		} else if !d.closeHarnessLocked(s, hid, out, text) {
			s.pendingCloses[hid] = pendingClose{out, text, d.now().Add(unmatchedGrace)}
		}
	case "sessionend":
		d.beginSettlementLocked(s, StatusClosed, finalReportGrace)
	}
	return nil
}

func (d *Dispatcher) armTurnLocked(s *runState, turnID string) {
	armedAt := d.now()
	s.armedTurn = turnID
	s.armedUntil = armedAt.Add(ackTimeout)
	for i := range s.record.Turns {
		if s.record.Turns[i].TurnID == turnID {
			s.record.Turns[i].armedAt = armedAt
			return
		}
	}
}

func (d *Dispatcher) deliverTurnLocked(s *runState, turnID, harnessTurnID string) *Turn {
	for i := range s.record.Turns {
		if s.record.Turns[i].TurnID == turnID {
			s.record.Turns[i].Delivered = true
			s.record.Turns[i].HarnessTurnID = harnessTurnID
			s.armedTurn = ""
			s.armedUntil = time.Time{}
			s.record.Status = StatusRunning
			s.record.startActive(d.now())
			return &s.record.Turns[i]
		}
	}
	return nil
}

func (d *Dispatcher) deliverOldestMatchingTurnLocked(s *runState, prompt, harnessTurnID string) *Turn {
	want := normalizePrompt(prompt)
	now := d.now()
	for i := range s.record.Turns {
		t := &s.record.Turns[i]
		if t.Source == TurnSourceOrchestrator && t.Outcome == "" && !t.Delivered &&
			!t.armedAt.IsZero() && !now.After(t.armedAt.Add(lateAckWindow)) &&
			normalizePrompt(t.Text) == want {
			return d.deliverTurnLocked(s, t.TurnID, harnessTurnID)
		}
	}
	return nil
}

func normalizePrompt(text string) string { return strings.Join(strings.Fields(text), " ") }

func str(p map[string]any, k string) string { v, _ := p[k].(string); return v }
func (d *Dispatcher) closeHarnessLocked(s *runState, hid string, o TurnOutcome, text string) bool {
	found := false
	for i := range s.record.Turns {
		if s.record.Turns[i].Outcome == "" && s.record.Turns[i].HarnessTurnID == hid {
			d.closeTurnLocked(s, s.record.Turns[i].TurnID, o, text)
			found = true
		}
	}
	if found {
		if s.closedHarness == nil {
			s.closedHarness = map[string]bool{}
		}
		s.closedHarness[hid] = true
		s.closedIDs = append(s.closedIDs, hid)
		if len(s.closedIDs) > maxInteractiveDedup {
			delete(s.closedHarness, s.closedIDs[0])
			s.closedIDs = s.closedIDs[1:]
		}
	}
	return found
}

// hasWorkingTurnLocked reports an open turn the harness is executing: a user
// turn, or an orchestrator turn that was delivered. Armed-but-undelivered
// orchestrator turns are not running yet.
func hasWorkingTurnLocked(s *runState) bool {
	for _, t := range s.record.Turns {
		if t.Outcome == "" && (t.Source == TurnSourceUser || t.Delivered) {
			return true
		}
	}
	return false
}
func (d *Dispatcher) closeOldestLocked(s *runState, o TurnOutcome, text string) {
	for i := range s.record.Turns {
		if s.record.Turns[i].Outcome == "" {
			d.closeTurnLocked(s, s.record.Turns[i].TurnID, o, text)
			return
		}
	}
}
func turnByID(r Record, id string) Turn {
	for _, t := range r.Turns {
		if t.TurnID == id {
			return t
		}
	}
	return Turn{}
}

func (d *Dispatcher) beginSettlementLocked(s *runState, status Status, grace time.Duration) {
	if s.record.Status.Terminal() || s.record.Status == StatusSettling {
		return
	}
	boundary := d.now()
	s.record.Status = StatusSettling
	s.record.foldActive(boundary)
	s.settleStatus = status
	s.settleDeadline = boundary.Add(grace)
	d.persistLocked(s, "status")
}
func (d *Dispatcher) finishInteractiveLocked(s *runState, status Status) {
	if s.record.Status.Terminal() {
		return
	}
	s.record.foldActive(d.now())
	for _, t := range s.record.Turns {
		if t.Outcome == "" {
			o := TurnLost
			if status == StatusCanceled || status == StatusTimeout {
				o = TurnInterrupted
			}
			d.closeTurnLocked(s, t.TurnID, o, "")
		}
	}
	if status == StatusClosed {
		for _, t := range s.record.Turns {
			if t.Outcome == TurnFinished {
				status = StatusClosed
				goto done
			}
		}
		status = StatusFailed
	}
done:
	s.record.Status = status
	s.record.EndedAt = d.now()
	d.persistLocked(s, "status")
	d.releaseRunFilesLocked(s.record.ID)
	_ = s.handle.Close(status, nil)
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	s.killPending = true
}

// Sweep advances time-based interactive transitions. It is intentionally
// called by the daemon's existing housekeeping loop rather than owning a goroutine.
func (d *Dispatcher) Sweep(now time.Time) {
	defer d.SweepNotifications(d.daemonCtx)
	// The dispatcher clock is the single time authority. The argument remains
	// for the housekeeping API but callers that need deterministic time set
	// d.now (as tests do), avoiding mixed-clock deadlines.
	_ = now
	now = d.now()
	type probe struct {
		state         *runState
		pane          string
		callerSession string
		idle          bool
	}
	d.mu.Lock()
	var cleanups []*runState
	var probes []probe
	rt := d.interactiveRuntime
	for _, s := range d.runs {
		if s.record.Mode != ModeInteractive || s.releasing {
			continue
		}
		d.expireArmedLocked(s)
		for k, p := range s.pendingCloses {
			if strings.HasPrefix(k, "@") {
				continue
			}
			if !p.until.IsZero() && !now.Before(p.until) {
				delete(s.pendingCloses, k)
				fmt.Fprintf(os.Stderr, "dispatch %s: dropping unmatched harness turn %s after grace\n", s.record.ID, k)
			}
		}
		if !s.record.Status.Terminal() {
			if s.record.Timeout > 0 && now.Sub(s.record.StartedAt) >= s.record.Timeout {
				d.beginSettlementLocked(s, StatusTimeout, 0)
			} else if s.record.PaneID != "" && rt != nil {
				probes = append(probes, probe{state: s, pane: s.record.PaneID, callerSession: s.record.CallerSessionID, idle: s.record.Status == StatusIdle && !s.idleSince.IsZero() && now.Sub(s.idleSince) >= idleCloseAfter})
			}
			if s.record.Status == StatusSettling && !now.Before(s.settleDeadline) {
				d.finishInteractiveLocked(s, s.settleStatus)
			}
		}
		if s.killPending && s.record.PaneID != "" {
			cleanups = append(cleanups, s)
		}
	}
	d.mu.Unlock()
	// Runtime calls may block (tmux in Step 5), so make them without the
	// dispatcher lock and revalidate the state before applying observations.
	if rt != nil {
		for _, p := range probes {
			if sessionProbe, ok := rt.(sessionAliveRuntime); ok && p.callerSession != "" {
				alive, err := sessionProbe.SessionAlive(p.callerSession)
				if err == nil && !alive {
					d.mu.Lock()
					if !p.state.releasing && p.state.record.Status != StatusReleased && !p.state.record.Status.Terminal() {
						d.finishInteractiveLocked(p.state, StatusClosed)
					}
					if p.state.killPending && p.state.record.PaneID != "" {
						cleanups = append(cleanups, p.state)
					}
					d.mu.Unlock()
					continue
				}
			}
			empty, alive := false, false
			if prober, ok := rt.(panePresenceRuntime); ok {
				presence, err := prober.PanePresence(p.pane)
				if err != nil {
					continue
				}
				alive = presence == PanePresentAlive
			} else {
				alive = rt.Alive(p.pane)
			}
			if p.idle {
				empty = rt.ComposerEmpty(p.pane)
			}
			d.mu.Lock()
			if p.state.record.PaneID == p.pane && !p.state.record.Status.Terminal() {
				if !alive {
					d.beginSettlementLocked(p.state, StatusClosed, finalReportGrace)
				} else if p.idle && p.state.record.Status == StatusIdle && !p.state.idleSince.IsZero() && now.Sub(p.state.idleSince) >= idleCloseAfter && empty {
					d.beginSettlementLocked(p.state, StatusClosed, 0)
				}
				if p.state.record.Status == StatusSettling && !now.Before(p.state.settleDeadline) {
					d.finishInteractiveLocked(p.state, p.state.settleStatus)
				}
			}
			if p.state.killPending && p.state.record.PaneID != "" {
				cleanups = append(cleanups, p.state)
			}
			d.mu.Unlock()
		}
	}
	if rt != nil {
		for _, state := range cleanups {
			d.mu.Lock()
			if state.releasing || state.record.Status == StatusReleased || !state.killPending || state.record.PaneID == "" {
				d.mu.Unlock()
				continue
			}
			rec, pane := cloneRecord(state.record), state.record.PaneID
			d.mu.Unlock()
			kill := rt.Kill
			if prober, ok := rt.(panePresenceRuntime); ok {
				presence, err := prober.PanePresence(pane)
				if err != nil {
					continue
				}
				if presence == PaneAbsent {
					kill = func(string) error { return nil }
				}
			}
			_, _ = d.closeRecordedPane(rec, pane, kill, runtimeLayout(rt))
		}
	}
}
