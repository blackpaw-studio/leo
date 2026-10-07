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
	// waitingStalledAfter is how long a waiting run may go without hook
	// activity before it reads stalled: background work can end without
	// waking the session (a Monitor that expired, a shell killed outright),
	// and nothing else would say so.
	waitingStalledAfter = 2 * time.Hour
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

// bridgeOpeningRuntime is a runtime that can carry a claude dispatch over
// the leo-bridge mod (see TmuxInteractiveRuntime).
type bridgeOpeningRuntime interface {
	// BridgesOpening reports whether a launch of harnessName will deliver
	// its opening over the bridge, which claude then submits itself.
	BridgesOpening(ctx context.Context, harnessName string) bool
	// AwaitOpening settles a launched pane's opening: handled is false for
	// a pane the bridge does not carry; paste says the opening still needs
	// a paste (the bridge never connected and the legacy relaunch could
	// not carry it on argv).
	AwaitOpening(ctx context.Context, paneID string) (handled, paste bool, err error)
}

// messageFramer rewrites a follow-up into the text the pane will actually
// receive, which is what its submit will echo back.
type messageFramer interface {
	FrameMessage(paneID, message string) string
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
		return d.queueInteractive(ctx, s, req, harnessName, model, cfg, rt), nil
	}
	d.mu.Lock()
	t := d.openTurnLocked(s, TurnSourceOrchestrator, prompt, true)
	d.persistLocked(s, "status")
	d.persistLocked(s, "turn")
	d.mu.Unlock()
	return d.launchInteractive(ctx, s, req, harnessName, model, cfg, rt, t.TurnID)
}

// queueInteractive records an interactive dispatch that found every slot
// taken: its opening turn waits, without a pane, until launchWhenSlotFree
// claims a slot and launches it exactly as an immediate start would.
func (d *Dispatcher) queueInteractive(ctx context.Context, s *runState, req Request, harnessName, model string, cfg *config.Config, rt InteractiveRuntime) Started {
	d.mu.Lock()
	t := d.openTurnLocked(s, TurnSourceOrchestrator, requestPrompt(req), false)
	s.awaitingSlot = true
	d.persistLocked(s, "status")
	d.persistLocked(s, "turn")
	d.mu.Unlock()
	go d.launchWhenSlotFree(ctx, s, req, harnessName, model, cfg, rt, t.TurnID)
	return Started{ID: s.record.ID, Harness: harnessName, Model: model, Cwd: req.Cwd, Queued: true}
}

// launchWhenSlotFree blocks on a concurrency slot for a queued interactive
// dispatch, then launches it. A run settled while it waited (canceled, timed
// out) never takes a slot and never gets a pane.
func (d *Dispatcher) launchWhenSlotFree(ctx context.Context, s *runState, req Request, harnessName, model string, cfg *config.Config, rt InteractiveRuntime, turnID string) {
	select {
	case d.sem <- struct{}{}:
	case <-s.done:
		return
	case <-ctx.Done():
		d.mu.Lock()
		s.awaitingSlot = false
		d.finishInteractiveLocked(s, StatusCanceled)
		d.mu.Unlock()
		return
	}
	d.mu.Lock()
	if s.record.Status.Terminal() || s.record.Status == StatusSettling || turnByID(s.record, turnID).Outcome != "" {
		d.mu.Unlock()
		<-d.sem
		return
	}
	// Admission: from here the slot belongs to the turn (released once, by
	// whatever closes it).
	s.awaitingSlot = false
	for i := range s.record.Turns {
		if s.record.Turns[i].TurnID == turnID {
			s.record.Turns[i].SlotHeld = true
			d.persistTurnLocked(s, s.record.Turns[i])
		}
	}
	d.mu.Unlock()
	if _, err := d.launchInteractive(ctx, s, req, harnessName, model, cfg, rt, turnID); err != nil {
		fmt.Fprintf(os.Stderr, "dispatch %s: launching queued interactive dispatch: %v\n", s.record.ID, err)
		if req.Isolation == "worktree" {
			d.cleanupWorktree(s.record.ID)
		}
	}
}

// launchInteractive opens turnID's pane. turnID must already hold a slot.
func (d *Dispatcher) launchInteractive(ctx context.Context, s *runState, req Request, harnessName, model string, cfg *config.Config, rt InteractiveRuntime, turnID string) (Started, error) {
	prompt := requestPrompt(req)
	bridgesOpening := false
	if b, ok := rt.(bridgeOpeningRuntime); ok {
		bridgesOpening = b.BridgesOpening(ctx, harnessName)
	}
	if bridgesOpening || claudeDeliversPromptViaArgv(harnessName, prompt) {
		// Claude submits an argv- or bridge-delivered opening brief itself,
		// as an ordinary UserPromptSubmit, which can arrive (via the hook's
		// own "leo dispatch report" process) before Launch even returns, let
		// alone before the async injectOpening goroutine below would have
		// armed it. Arm turn 1 here, before Launch, so no hook can ever
		// observe it unarmed. A bridged launch that falls back to a paste is
		// armed again by that paste.
		d.mu.Lock()
		d.armTurnLocked(s, turnID)
		d.persistLocked(s, "")
		d.mu.Unlock()
	}
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
	// Prompt carries the dispatch-preamble-wrapped opening text (not the raw
	// req.Prompt): a claude Launch reads it straight into the process's argv
	// as the opening brief (see TmuxInteractiveRuntime.Launch), so it must
	// already be what the model should see, identical to what a tmux paste
	// would have delivered.
	pane, window, err := rt.Launch(ctx, LaunchRequest{ID: s.record.ID, Harness: harnessName, Model: model, Effort: req.Effort, Cwd: req.Cwd, Name: req.Name, Template: req.Template, Caller: req.Caller, Prompt: prompt, Timeout: req.Timeout, Dispatched: true, CallerPaneID: req.CallerPaneID, CallerSessionID: req.CallerSessionID, CallerWindowID: req.CallerWindowID, Placement: placement})
	if err != nil {
		d.placement.Cancel(placementRecord.ID)
		d.mu.Lock()
		d.closeTurnLocked(s, turnID, TurnRejected, err.Error())
		d.finishInteractiveLocked(s, StatusFailed)
		d.mu.Unlock()
		return Started{}, err
	}
	// Publication is a pane op, so it is ordered against a cancellation's
	// kill: whichever runs second sees the other's effect.
	published := false
	d.mu.Lock()
	publishing := d.enqueuePaneOpLocked(s, paneOpPublish, func() {
		published = d.publishPane(s, rt, placement, pane, window)
	})
	d.mu.Unlock()
	<-publishing
	if !published {
		return Started{}, context.Canceled
	}
	go d.injectOpening(ctx, s, rt, harnessName, turnID, pane, prompt)
	return Started{ID: s.record.ID, Harness: harnessName, Model: model, Cwd: req.Cwd, Placement: placement.Kind, Pane: pane, Window: window}, nil
}

// publishPane attaches a launched pane to its run, or kills it when the run
// was settled or canceled while it launched. It runs as a pane op.
func (d *Dispatcher) publishPane(s *runState, rt InteractiveRuntime, placement ViewerPlacement, pane, window string) bool {
	id := s.record.ID
	published := d.placement.Publish(id, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		// A claude opening turn armed before Launch can already have been
		// delivered (Running) or even finished (Idle) by the time Launch
		// returns, if its hook raced ahead of this goroutine. Any
		// non-terminal, non-settling status still has a live pane to attach.
		if s.killRequested || s.record.Status.Terminal() || s.record.Status == StatusSettling {
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
		// The opening turn can finish before Launch returns, when there
		// was no pane yet to hide.
		d.nudgePaneLocked(s)
		return true
	})
	if published {
		return true
	}
	d.mu.Lock()
	s.record.ViewerKind = placement.Kind
	s.record.PaneID = pane
	d.persistLocked(s, "")
	rec := cloneRecord(s.record)
	d.mu.Unlock()
	if _, err := d.closeRecordedPane(rec, pane, rt.Kill, runtimeLayout(rt)); err == nil {
		d.placement.Cancel(id)
	}
	return false
}

// injectOpening deliberately runs after Start returns: the TUI's readiness
// probe can take a minute, while launch itself is the only synchronous error.
func (d *Dispatcher) injectOpening(ctx context.Context, s *runState, rt InteractiveRuntime, harnessName, turnID, pane, prompt string) {
	if d.beforeOpeningInject != nil {
		d.beforeOpeningInject()
	}
	if d.afterOpeningInject != nil {
		defer d.afterOpeningInject()
	}
	if !d.openingNeedsPaste(ctx, s, rt, harnessName, turnID, pane, prompt) {
		return
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
		d.reconcileOpeningPane(s, rt, pane)
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
	d.failOpening(s, turnID, err)
}

// openingNeedsPaste settles every opening that is not a tmux paste and
// reports whether one is still needed. Claude submits an opening that rode
// the bridge or its launch-time argv (see TmuxInteractiveRuntime.Launch)
// itself: a pasted brief arrives wrapped as <pasted_content>, which the
// model can refuse as untrusted. Turn 1 of those was armed synchronously in
// startInteractive, before Launch, since claude's submit can race ahead of
// this goroutine even being scheduled. A bridged launch whose mod never
// connects is relaunched the legacy way, which may still need the paste.
func (d *Dispatcher) openingNeedsPaste(ctx context.Context, s *runState, rt InteractiveRuntime, harnessName, turnID, pane, prompt string) bool {
	if b, ok := rt.(bridgeOpeningRuntime); ok {
		handled, paste, err := b.AwaitOpening(ctx, pane)
		switch {
		case !handled:
		case err != nil:
			d.failOpening(s, turnID, err)
			return false
		case paste:
			return true
		default:
			d.reconcileOpeningPane(s, rt, pane)
			return false
		}
	}
	if claudeDeliversPromptViaArgv(harnessName, prompt) {
		d.reconcileOpeningPane(s, rt, pane)
		return false
	}
	return true
}

// reconcileOpeningPane closes pane if the run settled, or moved to a
// different pane, while its opening was on its way.
func (d *Dispatcher) reconcileOpeningPane(s *runState, rt InteractiveRuntime, pane string) {
	d.mu.Lock()
	closed := d.enqueuePaneOpLocked(s, paneOpKill, func() { d.closeStalePane(s, rt, pane) })
	d.mu.Unlock()
	<-closed
}

// failOpening fails the run over its opening turn's delivery error. Opening
// delivery is asynchronous: a late error belongs only to the opening turn
// and never settles a run that has since advanced.
func (d *Dispatcher) failOpening(s *runState, turnID string, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	// A cancellation tearing the pane down settles the run itself.
	if !s.killRequested && !s.record.Status.Terminal() && s.record.Status != StatusSettling &&
		len(s.record.Turns) > 0 &&
		s.record.Turns[len(s.record.Turns)-1].TurnID == turnID &&
		s.record.Turns[len(s.record.Turns)-1].Outcome == "" {
		d.closeTurnLocked(s, turnID, TurnRejected, err.Error())
		d.finishInteractiveLocked(s, StatusFailed)
		return
	}
	fmt.Fprintf(os.Stderr, "dispatch %s: ignoring late opening injection error: %v\n", s.record.ID, err)
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
	return d.appendTurnLocked(s, source, text, held)
}

// appendTurnLocked opens a turn without expiring undelivered orchestrator
// turns; openTurnLocked is the default.
func (d *Dispatcher) appendTurnLocked(s *runState, source TurnSource, text string, held bool) *Turn {
	t := Turn{TurnID: fmt.Sprintf("%s#%d", s.record.ID, len(s.record.Turns)+1), Source: source, StartedAt: d.now(), Text: text, SlotHeld: held}
	s.record.Turns = append(s.record.Turns, t)
	s.record.Status = StatusQueued
	if source == TurnSourceUser {
		s.record.Status = StatusRunning
		s.record.startActive(d.now())
		// A user-typed turn keeps the pane where the user typed it, even if
		// a hide is already queued or under way.
		s.paneWant = s.record.ViewerKind
	} else {
		s.paneWant = "split"
	}
	d.nudgePaneLocked(s)
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
			if s.record.Status == StatusIdle {
				// A prompt still pending when its turn ended was answered in
				// the pane or abandoned; either way nobody needs it now.
				d.dropPermissionsLocked(s)
			} else if len(s.permissions) > 0 {
				d.refreshNeedsInputLocked(s)
			}
		}
		// Panes show only while the orchestrator has work in them; a turn
		// the user typed never moves one.
		if t.Source == TurnSourceOrchestrator && !d.hasOpenTurnLocked(s) {
			s.paneWant = viewerHidden
		}
		d.nudgePaneLocked(s)
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

func (d *Dispatcher) hasOpenTurnLocked(s *runState) bool {
	for _, t := range s.record.Turns {
		if t.Outcome == "" {
			return true
		}
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
			if s.record.PendingWork != nil {
				return StatusWaiting
			}
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
	if s.awaitingSlot {
		d.mu.Unlock()
		return SendResult{}, errors.New("dispatch is queued for a concurrency slot and has not started; leo_wait on it first")
	}
	if s.record.Status == StatusNeedsInput && s.record.NeedsInput != nil {
		need := *s.record.NeedsInput
		d.mu.Unlock()
		return SendResult{}, fmt.Errorf("dispatch is waiting on a permission decision (%s: %s, request %s); answer with decision allow|deny (and an optional reason) instead of a message", need.Tool, need.Summary, need.RequestID)
	}
	if s.record.Status == StatusWaiting {
		pending := s.record.PendingWork.Summary()
		d.mu.Unlock()
		return SendResult{}, fmt.Errorf("dispatch is waiting on background work (%s) and will continue its turn when it finishes; leo_wait on it first", pending)
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
	if f, ok := d.interactiveRuntime.(messageFramer); ok {
		// Record what the pane will receive: its submit echoes that text,
		// which a late ack is matched by.
		message = f.FrameMessage(s.record.PaneID, message)
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
	shown := d.nudgePaneLocked(s)
	d.mu.Unlock()
	if rt == nil {
		d.mu.Lock()
		d.closeTurnLocked(s, t.TurnID, TurnRejected, "interactive runtime unavailable")
		d.mu.Unlock()
		return SendResult{TurnID: t.TurnID}, errors.New("interactive runtime unavailable")
	}
	select {
	case <-shown:
	case <-ctx.Done():
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
		prompt := str(p, "prompt")
		injected := s.record.Harness == "claude" && isHarnessInjection(prompt)
		// Whatever woke a waiting run (its background work's notification,
		// a wakeup, a human) carries its open turn on.
		d.resumeWaitingLocked(s)
		var delivered *Turn
		if !injected && s.armedTurn != "" && d.now().Before(s.armedUntil) {
			delivered = d.deliverTurnLocked(s, s.armedTurn, hid)
		} else if matched := d.matchSubmitLocked(s, prompt, hid, injected); matched != nil {
			delivered = matched
		} else {
			// Only a prompt a human typed steers the run; Claude Code's own
			// injections (background-task notifications) do not.
			if !injected {
				s.record.Steered = true
			}
			if hid == "" && hasWorkingTurnLocked(s) {
				// Claude carries no turn id and drains queued prompts (typed
				// or injected) inside the running turn, before a single Stop.
				// Fold into the working turn, however long it was hook-silent:
				// a separate turn would never close (#211), and closing this
				// one would report it lost while it is still working.
				if s.record.Status != oldStatus {
					d.persistLocked(s, "status")
				} else {
					d.persistLocked(s, "")
				}
				return nil
			}
			var t *Turn
			if injected {
				// Claude started a turn on its own; a sent prompt still
				// waiting to submit keeps waiting rather than being lost.
				t = d.appendTurnLocked(s, TurnSourceUser, "", false)
			} else {
				t = d.openTurnLocked(s, TurnSourceUser, "", false)
			}
			t.HarnessTurnID = hid
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
		if event == "stop" && hid == "" && !hasWorkingTurnLocked(s) {
			// The Stop overtook its turn's submit: it confirms the armed
			// turn ran before anything else reads the turn as working.
			d.confirmArmedLocked(s)
		}
		if event == "stop" && hid == "" && hasWorkingTurnLocked(s) {
			if w := pendingWorkFromStop(p); w != nil {
				d.waitOnBackgroundLocked(s, w)
				break
			}
		}
		s.record.PendingWork = nil
		if hid == "" {
			d.closeWorkingLocked(s, out, text)
		} else if !d.closeHarnessLocked(s, hid, out, text) {
			s.pendingCloses[hid] = pendingClose{out, text, d.now().Add(unmatchedGrace)}
		}
	case "sessionend":
		d.beginSettlementLocked(s, StatusClosed, finalReportGrace)
	}
	if len(s.permissions) > 0 {
		d.refreshNeedsInputLocked(s)
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

// harnessInjectionMarkers are the envelope tags of prompts the harness
// submits on its own, which fire UserPromptSubmit like a typed prompt.
var harnessInjectionMarkers = []string{
	"task-notification", // Claude Code: a background task finished
}

// isHarnessInjection reports whether the whole prompt is one injection
// envelope, so a human quoting a tag still counts as typing.
func isHarnessInjection(prompt string) bool {
	prompt = strings.TrimSpace(prompt)
	for _, m := range harnessInjectionMarkers {
		if strings.HasPrefix(prompt, "<"+m+">") && strings.HasSuffix(prompt, "</"+m+">") {
			return true
		}
	}
	return false
}

// matchSubmitLocked attributes a submit to a sent turn by prompt text. A
// harness injection never claims a sent turn.
func (d *Dispatcher) matchSubmitLocked(s *runState, prompt, hid string, injected bool) *Turn {
	if injected || prompt == "" {
		return nil
	}
	return d.deliverOldestMatchingTurnLocked(s, prompt, hid)
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

// closeWorkingLocked handles an id-less close, which Claude fires once the
// session goes idle: every working turn ends on it, since turns drained in one
// agent loop share a single Stop. With none working it falls back to the
// oldest open turn; a sent prompt still waiting to submit is otherwise left
// pending.
func (d *Dispatcher) closeWorkingLocked(s *runState, o TurnOutcome, text string) {
	var working []string
	for _, t := range s.record.Turns {
		if t.Outcome == "" && (t.Source == TurnSourceUser || t.Delivered) {
			working = append(working, t.TurnID)
		}
	}
	for _, id := range working {
		d.closeTurnLocked(s, id, o, text)
	}
	if len(working) > 0 {
		return
	}
	for i := range s.record.Turns {
		if s.record.Turns[i].Outcome == "" {
			d.closeTurnLocked(s, s.record.Turns[i].TurnID, o, text)
			return
		}
	}
}

// waitOnBackgroundLocked handles a Stop that leaves background work pending:
// the session is paused, not done, so the working turn stays open (leo_wait
// keeps blocking) and the run reads waiting until that work wakes it.
func (d *Dispatcher) waitOnBackgroundLocked(s *runState, w *PendingWork) {
	old := s.record.Status
	s.record.PendingWork = w
	s.record.foldActive(d.now())
	if s.record.Status != StatusNeedsInput {
		s.record.Status = StatusWaiting
	}
	if s.record.Status != old {
		d.persistLocked(s, "status")
	} else {
		d.persistLocked(s, "")
	}
}

// confirmArmedLocked marks the oldest open turn delivered when it is a sent
// turn that was armed (its submit expected): a Stop can reach the
// dispatcher before that turn's UserPromptSubmit. No age bound applies: an
// id-less Stop with no working turn can only be the armed turn's, however
// long it ran (replays are deduplicated by event id), and the turn would be
// closed by the fallback anyway. Its submit may still arrive late; see the
// known limitations in docs/configuration/dispatches.md.
func (d *Dispatcher) confirmArmedLocked(s *runState) {
	for _, t := range s.record.Turns {
		if t.Outcome != "" {
			continue
		}
		if t.Source == TurnSourceOrchestrator && !t.Delivered && !t.armedAt.IsZero() {
			if delivered := d.deliverTurnLocked(s, t.TurnID, ""); delivered != nil {
				d.persistTurnLocked(s, *delivered)
			}
		}
		return
	}
}

// resumeWaitingLocked returns a waiting run to running as its session
// starts working again; the caller persists the change.
func (d *Dispatcher) resumeWaitingLocked(s *runState) {
	if s.record.PendingWork == nil && s.record.Status != StatusWaiting {
		return
	}
	s.record.PendingWork = nil
	if s.record.Status == StatusWaiting {
		s.record.Status = StatusRunning
	}
	s.record.startActive(d.now())
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
	d.dropPermissionsLocked(s)
	boundary := d.now()
	s.record.PendingWork = nil
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
	d.dropPermissionsLocked(s)
	s.record.foldActive(d.now())
	s.record.PendingWork = nil
	for _, t := range s.record.Turns {
		if t.Outcome == "" {
			o := TurnLost
			if status == StatusCanceled || status == StatusTimeout {
				o = TurnInterrupted
			}
			d.closeTurnLocked(s, t.TurnID, o, endedTurnText(s.record.ID, t))
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

// undeliveredPreviewRunes bounds how much of an undelivered follow-up its
// notice quotes.
const undeliveredPreviewRunes = 200

// endedTurnText is the result text of turn t, still open when dispatch id
// ends: a follow-up its session was never seen to start says so, quoting
// how it began, since nothing else would tell the orchestrator (dispatches
// do not outlive their launch, so it is not kept for another). It does not
// claim the follow-up never ran: a paste can land unseen, and the bridge's
// ack is not followed here. Any other turn keeps the text it has ("" leaves
// it as is).
func endedTurnText(id string, t Turn) string {
	if t.Source != TurnSourceOrchestrator || t.Delivered {
		return ""
	}
	return fmt.Sprintf("dispatch %s ended, and this follow-up was never seen to start: it may not have run. It began: %q", id, previewRunes(t.Text, undeliveredPreviewRunes))
}

// previewRunes is text cut to at most n runes, marked when cut.
func previewRunes(text string, n int) string {
	runes := []rune(text)
	if len(runes) <= n {
		return text
	}
	return string(runes[:n]) + "…"
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
		retries := make([]<-chan struct{}, 0, len(cleanups))
		for _, state := range cleanups {
			d.mu.Lock()
			if !d.paneOpQueuedLocked(state, paneOpKill) {
				state := state
				retries = append(retries, d.enqueuePaneOpLocked(state, paneOpKill, func() { d.retryPaneKill(state, rt) }))
			}
			d.mu.Unlock()
		}
		for _, done := range retries {
			<-done
		}
	}
}
