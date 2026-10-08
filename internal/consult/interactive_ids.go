package consult

import (
	"context"
	"fmt"
	"os"
	"slices"
)

// Exact turn attribution. Every harness hook that carries an id names the
// turn it belongs to, so a report is routed to its turn by that id rather
// than by arrival order or prompt text. An id is a key with a namespace:
//
//	p:<prompt_id>   claude shell hooks (UserPromptSubmit, Stop share one)
//	t:<turn id>     the leo-bridge mod (turn.start, turn.complete)
//	c:<turn_id>     codex hooks
//
// A run's turns hold the keys bound to them, and a finished turn keeps its
// keys, so a replayed or late report for a finished turn is dropped instead
// of opening a spurious one for as long as the turn is in the run's record.
// Payloads without any id (older claude, id-less hooks) keep the
// arrival-order path in Report.

// originTaskNotification is the origin the leo-bridge mod reports for a turn
// the harness started on a background task's notification.
const originTaskNotification = "task-notification"

const (
	keyPrompt = 'p'
	keyBridge = 't'
	keyCodex  = 'c'
)

type commandIDKey struct{}

// withCommandID carries the id of the bridge command a follow-up will travel
// under to the runtime, which sends it under that id when it can.
func withCommandID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, commandIDKey{}, id)
}

// commandIDFrom is the command id withCommandID set, or "".
func commandIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(commandIDKey{}).(string)
	return id
}

// harnessKey is the namespaced id a hook payload carries, or "" for none.
func harnessKey(p map[string]any) string {
	if id := str(p, "prompt_id"); id != "" {
		return string(keyPrompt) + ":" + id
	}
	if id := str(p, "bridge_turn_id"); id != "" {
		return string(keyBridge) + ":" + id
	}
	for _, field := range []string{"turn_id", "harness_turn_id"} {
		if id := str(p, field); id != "" {
			return string(keyCodex) + ":" + id
		}
	}
	return ""
}

func keyID(key string) string { return key[2:] }

// isBridgeKey reports a key from the leo-bridge mod, whose turn.start names
// the command that submitted it: a bridged run claims sent turns by that
// command id alone, never by arming or text.
func isBridgeKey(key string) bool { return key[0] == keyBridge }

func (t *Turn) hasKey(key string) bool {
	if slices.Contains(t.keys, key) {
		return true
	}
	return len(t.keys) == 0 && t.HarnessTurnID != "" && t.HarnessTurnID == keyID(key)
}

// isBound reports a turn some harness id already names.
func (t *Turn) isBound() bool { return len(t.keys) > 0 || t.HarnessTurnID != "" }

func (d *Dispatcher) bindKeyLocked(s *runState, turnID, key string) {
	for i := range s.record.Turns {
		t := &s.record.Turns[i]
		if t.TurnID != turnID {
			continue
		}
		if !slices.Contains(t.keys, key) {
			t.keys = append(t.keys, key)
		}
		if t.HarnessTurnID == "" {
			t.HarnessTurnID = keyID(key)
		}
		return
	}
}

// turnForKeyLocked is the open turn key is bound to, or nil.
func turnForKeyLocked(s *runState, key string) *Turn {
	for i := range s.record.Turns {
		if t := &s.record.Turns[i]; t.Outcome == "" && t.hasKey(key) {
			return t
		}
	}
	return nil
}

// isKeyClosedLocked reports a key that belongs to a finished turn.
func isKeyClosedLocked(s *runState, key string) bool {
	for i := range s.record.Turns {
		if t := &s.record.Turns[i]; t.Outcome != "" && t.hasKey(key) {
			return true
		}
	}
	return false
}

func turnIndexByID(turns []Turn, id string) int {
	for i := range turns {
		if turns[i].TurnID == id {
			return i
		}
	}
	return -1
}

// waitingTurnLocked is the open turn a Stop with pending work left paused:
// the one whose wake carries the session on.
func waitingTurnLocked(s *runState) *Turn {
	if s.record.PendingWork == nil {
		return nil
	}
	for i := range s.record.Turns {
		t := &s.record.Turns[i]
		if t.Outcome == "" && (t.Source == TurnSourceUser || t.Delivered) {
			return t
		}
	}
	return nil
}

// lastWorkingTurnLocked is the most recent open turn that is running: a
// user's, or a sent one whose submit has arrived.
func lastWorkingTurnLocked(s *runState) *Turn {
	for i := len(s.record.Turns) - 1; i >= 0; i-- {
		t := &s.record.Turns[i]
		if t.Outcome == "" && !t.Queued && (t.Source == TurnSourceUser || t.Delivered) {
			return t
		}
	}
	return nil
}

// submitKeyedLocked attributes a UserPromptSubmit that carries an id.
func (d *Dispatcher) submitKeyedLocked(s *runState, key string, p map[string]any) {
	if isKeyClosedLocked(s, key) {
		fmt.Fprintf(os.Stderr, "dispatch %s: ignoring submit for closed harness turn %s\n", s.record.ID, key)
		return
	}
	oldStatus := s.record.Status
	prompt := str(p, "prompt")
	injected := s.record.Harness == "claude" && (isHarnessInjection(prompt) || str(p, "origin") == originTaskNotification)
	persistPlaced := func() { d.persistLocked(s, "") }
	if known := turnForKeyLocked(s, key); known != nil {
		d.noteKnownSubmitLocked(s, known, prompt, injected)
	} else {
		turnID := d.startKeyedTurnLocked(s, key, prompt, injected, str(p, "command_id"))
		persistPlaced = func() {
			if i := turnIndexByID(s.record.Turns, turnID); i >= 0 {
				d.persistTurnLocked(s, s.record.Turns[i])
			}
		}
	}
	d.applyObservedEffortLocked(s, effortFromPayload(p), currentTurnIndex(s.record.Turns))
	closed := false
	// A Stop held for this id is this turn's, however long it waited: the
	// grace bounds how long a Stop may wait unclaimed, not whose it is.
	if pc, ok := s.pendingCloses[key]; ok {
		if t := turnForKeyLocked(s, key); t != nil {
			d.finishKeyedLocked(s, t, pc.outcome, pc.text, pc.work)
			closed = true
		}
		delete(s.pendingCloses, key)
	}
	if !closed {
		persistPlaced()
	}
	if s.record.Status != oldStatus {
		d.persistLocked(s, "status")
	}
}

// noteKnownSubmitLocked handles a submit under an id its turn already
// answers to: a replay of the turn's own submit, or a prompt queued into the
// running turn. Claude fires a queued prompt's UserPromptSubmit at once under
// the running turn's id (verified on 2.1.294), and then either drains it
// inside that turn (one Stop) or runs it as a turn of its own after the Stop
// (a Stop under a new id, see stopKeyedLocked). Either way no turn opens
// here; a human's queued prompt is steering.
func (d *Dispatcher) noteKnownSubmitLocked(s *runState, t *Turn, prompt string, injected bool) {
	norm := normalizePrompt(prompt)
	if norm == "" || injected {
		return
	}
	if t.submitted == "" {
		t.submitted = norm
		return
	}
	if norm != t.submitted {
		s.record.Steered = true
	}
}

// startKeyedTurnLocked places a submit under an id no open turn answers to
// and returns the turn it belongs to. In order: the waiting turn its wake
// continues, the sent turn it delivers, else a new turn of the user's.
func (d *Dispatcher) startKeyedTurnLocked(s *runState, key, prompt string, injected bool, commandID string) string {
	norm := normalizePrompt(prompt)
	if w := waitingTurnLocked(s); w != nil {
		// Whatever woke the session (its background work's notification, a
		// wakeup, a human) carries the waiting turn on under a new id.
		id := w.TurnID
		d.resumeWaitingLocked(s)
		d.bindKeyLocked(s, id, key)
		if !injected {
			s.record.Steered = true
		}
		return id
	}
	d.resumeWaitingLocked(s)
	if injected {
		// A wake that reaches leo before the Stop that leaves its turn
		// waiting (that Stop is delayed) continues the turn all the same:
		// the Stop then keeps it waiting, and the wake's own Stop ends it.
		if w := lastWorkingTurnLocked(s); w != nil {
			d.bindKeyLocked(s, w.TurnID, key)
			return w.TurnID
		}
	}
	var t *Turn
	if !injected {
		t = d.claimSentTurnLocked(s, key, prompt, commandID)
	}
	if t == nil {
		// A turn.start naming a command that has no open sent turn (one
		// already closed lost or rejected) is leo's own, not a person's.
		t = d.openUserTurnLocked(s, key, injected || commandID != "")
	}
	t.submitted = norm
	d.bindKeyLocked(s, t.TurnID, key)
	return t.TurnID
}

// openUserTurnLocked opens a turn nobody at leo sent. Only a prompt a human
// typed steers the run; Claude Code's own injections do not. A typed turn at
// idle overtakes (and loses) a sent prompt still waiting to submit, except
// on a bridged run, where a sent turn is claimed by command id whenever it
// does start and an overtaking turn cannot cost it its place.
func (d *Dispatcher) openUserTurnLocked(s *runState, key string, injected bool) *Turn {
	if !injected {
		s.record.Steered = true
	}
	if injected || isBridgeKey(key) {
		return d.appendTurnLocked(s, TurnSourceUser, "", false)
	}
	return d.openTurnLocked(s, TurnSourceUser, "", false)
}

// claimSentTurnLocked delivers the sent turn this submit starts: on a
// bridged run the one whose command id the turn.start names; otherwise the
// armed one, else the oldest sent turn whose text it matches (the late ack of
// a slow start, see lateAckWindow).
func (d *Dispatcher) claimSentTurnLocked(s *runState, key, prompt, commandID string) *Turn {
	hid := keyID(key)
	if isBridgeKey(key) {
		if commandID == "" {
			return nil
		}
		for i := range s.record.Turns {
			if t := &s.record.Turns[i]; t.Outcome == "" && !t.Delivered && t.Source == TurnSourceOrchestrator && t.commandID == commandID {
				return d.deliverTurnLocked(s, t.TurnID, hid)
			}
		}
		return nil
	}
	if s.armedTurn != "" && d.now().Before(s.armedUntil) {
		return d.deliverTurnLocked(s, s.armedTurn, hid)
	}
	return d.matchSubmitLocked(s, prompt, hid, false)
}

// stopKeyedLocked closes the turn key names. A Stop for an id no turn
// answers to yet is either ahead of its submit or the end of a turn this run
// never saw start (a queued prompt Claude ran after the previous Stop): it
// adopts the turn it can only be (see adoptStopLocked) and otherwise waits,
// briefly, for the submit that names it. It never closes a turn some other
// id already names. With a sent turn armed for a submit that has not
// arrived, the Stop waits for that submit before it may close the turn (see
// adoptHeldStopLocked): it could just as well end a turn a person queued.
func (d *Dispatcher) stopKeyedLocked(s *runState, key, event string, p map[string]any, out TurnOutcome, text string) {
	if isKeyClosedLocked(s, key) {
		fmt.Fprintf(os.Stderr, "dispatch %s: ignoring close for closed harness turn %s\n", s.record.ID, key)
		return
	}
	var work *PendingWork
	if event == "stop" {
		work = pendingWorkFromStop(p)
	}
	t := turnForKeyLocked(s, key)
	if t == nil {
		if armed := armedUndeliveredLocked(s); armed != nil && !isBridgeKey(key) {
			s.pendingCloses[key] = pendingClose{outcome: out, text: text, until: d.now().Add(unmatchedGrace), work: work, armed: armed.TurnID}
			return
		}
		t = d.adoptStopLocked(s, key)
	}
	if t == nil {
		s.pendingCloses[key] = pendingClose{outcome: out, text: text, until: d.now().Add(unmatchedGrace), work: work}
		return
	}
	idx := turnIndexByID(s.record.Turns, t.TurnID)
	d.finishKeyedLocked(s, t, out, text, work)
	d.applyObservedEffortLocked(s, effortFromPayload(p), idx)
}

// finishKeyedLocked ends t on a Stop: a Stop that leaves background work
// pending only pauses it.
func (d *Dispatcher) finishKeyedLocked(s *runState, t *Turn, out TurnOutcome, text string, work *PendingWork) {
	if work != nil {
		d.waitOnBackgroundLocked(s, work)
		return
	}
	s.record.PendingWork = nil
	d.closeTurnLocked(s, t.TurnID, out, text)
}

// armedUndeliveredLocked is the sent turn armed for a submit that has not
// arrived, or nil.
func armedUndeliveredLocked(s *runState) *Turn {
	for i := range s.record.Turns {
		t := &s.record.Turns[i]
		if t.Outcome == "" && !t.Queued && t.Source == TurnSourceOrchestrator && !t.Delivered && !t.armedAt.IsZero() {
			return t
		}
	}
	return nil
}

// adoptHeldStopLocked settles a Stop held for an armed turn's submit once
// its grace has run out, and reports whether it closed that turn. A submit
// for the turn under another id delivered it meanwhile: the Stop was some
// other turn's and is dropped. With none seen the turn's submit was lost and
// the Stop can only be its own.
func (d *Dispatcher) adoptHeldStopLocked(s *runState, key string, pc pendingClose) bool {
	i := turnIndexByID(s.record.Turns, pc.armed)
	if pc.armed == "" || i < 0 {
		return false
	}
	t := &s.record.Turns[i]
	if t.Outcome != "" || t.Delivered {
		return false
	}
	d.deliverTurnLocked(s, t.TurnID, keyID(key))
	d.bindKeyLocked(s, t.TurnID, key)
	d.finishKeyedLocked(s, t, pc.outcome, pc.text, pc.work)
	return true
}

// adoptStopLocked binds key to the turn a Stop under an unseen id can only
// belong to, or returns nil. In order: the waiting turn (the wake's submit
// has not arrived), the single working turn no id names yet, the oldest open
// turn no id names. A bridged run adopts nothing: its turn.start always
// names the turn, so the Stop waits for it. A sent turn armed for a submit
// that has not arrived is not adopted here (see adoptHeldStopLocked).
func (d *Dispatcher) adoptStopLocked(s *runState, key string) *Turn {
	if isBridgeKey(key) {
		return nil
	}
	var working, oldest *Turn
	workingUnbound := 0
	for i := range s.record.Turns {
		t := &s.record.Turns[i]
		if t.Outcome != "" || t.Queued {
			continue
		}
		if !t.isBound() && (t.Source == TurnSourceUser || t.Delivered) {
			working = t
			workingUnbound++
		}
		if oldest == nil && !t.isBound() {
			oldest = t
		}
	}
	var t *Turn
	switch {
	case waitingTurnLocked(s) != nil:
		t = waitingTurnLocked(s)
	case workingUnbound == 1:
		t = working
	default:
		t = oldest
	}
	if t != nil {
		d.bindKeyLocked(s, t.TurnID, key)
	}
	return t
}

func (d *Dispatcher) setCommandIDLocked(s *runState, turnID, commandID string) {
	if i := turnIndexByID(s.record.Turns, turnID); i >= 0 {
		s.record.Turns[i].commandID = commandID
	}
}

// stopUnkeyedLocked closes on a Stop or Interrupt that carries no id. Such a
// payload fires once the session goes idle: every working turn ends on it,
// since turns drained in one agent loop share a single Stop. A run that has
// shown ids closes only the one working turn (see stopOneLocked): an id-less
// payload amid ids is not evidence about the others.
func (d *Dispatcher) stopUnkeyedLocked(s *runState, event string, p map[string]any, out TurnOutcome, text, effort string) {
	if s.idMode {
		d.stopOneLocked(s, event, p, out, text, effort)
		return
	}
	if event == "stop" && !hasWorkingTurnLocked(s) {
		// The Stop overtook its turn's submit: it confirms the armed
		// turn ran before anything else reads the turn as working.
		d.confirmArmedLocked(s)
	}
	if event == "stop" && hasWorkingTurnLocked(s) {
		if w := pendingWorkFromStop(p); w != nil {
			d.applyObservedEffortLocked(s, effort, currentTurnIndex(s.record.Turns))
			d.waitOnBackgroundLocked(s, w)
			return
		}
	}
	s.record.PendingWork = nil
	d.applyObservedEffortLocked(s, effort, currentTurnIndex(s.record.Turns))
	d.closeWorkingLocked(s, out, text)
}

// stopOneLocked closes the working turn an id-less Stop must mean: the
// only one, else the oldest open one. With none open it does nothing.
func (d *Dispatcher) stopOneLocked(s *runState, event string, p map[string]any, out TurnOutcome, text, effort string) {
	var target *Turn
	for i := range s.record.Turns {
		t := &s.record.Turns[i]
		if t.Outcome != "" || t.Queued {
			continue
		}
		if t.Source == TurnSourceUser || t.Delivered {
			target = t
			break
		}
		if target == nil {
			target = t
		}
	}
	if target == nil {
		return
	}
	var work *PendingWork
	if event == "stop" {
		work = pendingWorkFromStop(p)
	}
	idx := turnIndexByID(s.record.Turns, target.TurnID)
	d.finishKeyedLocked(s, target, out, text, work)
	d.applyObservedEffortLocked(s, effort, idx)
}
