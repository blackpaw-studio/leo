package consult

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
)

// Claude's shell hooks all carry a prompt_id: a turn's UserPromptSubmit,
// PreToolUse and Stop share one. The dispatcher attributes each report to
// its turn by that id, never by arrival order or prompt text.

func idHook(t *testing.T, eventID, event, promptID string, fields map[string]any) HookReport {
	t.Helper()
	payload := map[string]any{"hook_event_name": event, "prompt_id": promptID, "session_id": "s-1"}
	for k, v := range fields {
		payload[k] = v
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return HookReport{EventID: eventID, Payload: b}
}

func idSubmit(t *testing.T, eventID, promptID, prompt string) HookReport {
	t.Helper()
	return idHook(t, eventID, "UserPromptSubmit", promptID, map[string]any{"prompt": prompt})
}

func idStop(t *testing.T, eventID, promptID, message string) HookReport {
	t.Helper()
	return idHook(t, eventID, "Stop", promptID, map[string]any{"last_assistant_message": message, "stop_hook_active": false, "background_tasks": []any{}, "session_crons": []any{}})
}

func idStopWaiting(t *testing.T, eventID, promptID, message string) HookReport {
	t.Helper()
	return idHook(t, eventID, "Stop", promptID, map[string]any{"last_assistant_message": message, "background_tasks": shellAndMonitor["background_tasks"], "session_crons": []any{}})
}

// startArmedClaude starts a claude dispatch whose opening is armed but whose
// submit has not been reported yet.
func startArmedClaude(t *testing.T) (*Dispatcher, *fakeInteractiveRuntime, string, *time.Time) {
	t.Helper()
	now := time.Date(2026, time.October, 8, 12, 0, 0, 0, time.UTC)
	d := NewDispatcher(newFakeRecorder())
	d.now = sharedClock(&now)
	rt := &fakeInteractiveRuntime{arm: true, empty: true}
	d.SetInteractiveRuntime(rt)
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "hello", Cwd: t.TempDir(), Mode: ModeInteractive})
	if err != nil {
		t.Fatal(err)
	}
	waitForArmed(t, d, started.ID)
	return d, rt, started.ID, &now
}

func reportAll(t *testing.T, d *Dispatcher, id string, reports ...HookReport) {
	t.Helper()
	for _, r := range reports {
		if err := d.Report(id, r); err != nil {
			t.Fatal(err)
		}
	}
}

func idleRecord(t *testing.T, d *Dispatcher, id string) Record {
	t.Helper()
	rec, err := d.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

const openingText = dispatchPreamble + " hello"

func TestPromptIDAttribution(t *testing.T) {
	cases := []struct {
		name     string
		reports  func(t *testing.T) []HookReport
		wantText string // the opening turn's result
		wantOpen bool   // the opening turn is still open
		steered  bool
		turns    int
	}{
		{
			name: "the opening's submit and stop share one id",
			reports: func(t *testing.T) []HookReport {
				return []HookReport{idSubmit(t, "u1", "a", openingText), idStop(t, "s1", "a", "done")}
			},
			wantText: "done", turns: 1,
		},
		{
			name: "a stop that beats its submit closes the armed opening, and the late submit is dropped",
			reports: func(t *testing.T) []HookReport {
				return []HookReport{idStop(t, "s1", "a", "done"), idSubmit(t, "u1", "a", openingText)}
			},
			wantText: "done", turns: 1,
		},
		{
			name: "a replayed submit under a new event id is not a second turn",
			reports: func(t *testing.T) []HookReport {
				return []HookReport{idSubmit(t, "u1", "a", openingText), idSubmit(t, "u2", "a", openingText), idStop(t, "s1", "a", "done")}
			},
			wantText: "done", turns: 1,
		},
		{
			name: "a submit that arrives after its stop opens no steered turn",
			reports: func(t *testing.T) []HookReport {
				return []HookReport{idSubmit(t, "u1", "a", openingText), idStop(t, "s1", "a", "done"), idSubmit(t, "u2", "a", openingText)}
			},
			wantText: "done", turns: 1,
		},
		{
			name: "a replayed stop under a new event id closes nothing else",
			reports: func(t *testing.T) []HookReport {
				return []HookReport{idSubmit(t, "u1", "a", openingText), idStop(t, "s1", "a", "done"), idStop(t, "s2", "a", "done")}
			},
			wantText: "done", turns: 1,
		},
		// Live claude 2.1.294: a prompt queued into a running turn fires its
		// UserPromptSubmit at once under the running turn's id; the queued
		// turn's own id appears only on its later Stop.
		{
			name: "a prompt queued into the running turn shares its id and steers without a turn",
			reports: func(t *testing.T) []HookReport {
				return []HookReport{idSubmit(t, "u1", "a", openingText), idSubmit(t, "u2", "a", "typed by a human"), idStop(t, "s1", "a", "both done")}
			},
			wantText: "both done", steered: true, turns: 1,
		},
		{
			name: "the stop of a queued prompt that ran as its own turn closes nothing else",
			reports: func(t *testing.T) []HookReport {
				return []HookReport{idSubmit(t, "u1", "a", openingText), idSubmit(t, "u2", "a", "typed by a human"), idStop(t, "s1", "a", "first"), idStop(t, "s2", "b", "second")}
			},
			wantText: "first", steered: true, turns: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, _, id, _ := startArmedClaude(t)
			reportAll(t, d, id, tc.reports(t)...)
			rec := idleRecord(t, d, id)
			if len(rec.Turns) != tc.turns {
				t.Fatalf("turns = %d, want %d: %+v", len(rec.Turns), tc.turns, rec.Turns)
			}
			first := rec.Turns[0]
			if !first.Delivered || first.Outcome != TurnFinished || first.Text != tc.wantText {
				t.Fatalf("opening turn = %+v, want finished %q", first, tc.wantText)
			}
			if rec.Steered != tc.steered || rec.Status != StatusIdle {
				t.Fatalf("steered=%v status=%s, want steered=%v idle: %+v", rec.Steered, rec.Status, tc.steered, rec)
			}
		})
	}
}

// A human submit at idle is a turn of its own, bound by its id: its Stop
// closes it, and identical text elsewhere cannot confuse the two.
func TestPromptIDHumanTurnAtIdleClosesByItsOwnID(t *testing.T) {
	d, _, id, _ := startArmedClaude(t)
	reportAll(t, d, id, idSubmit(t, "u1", "a", openingText), idStop(t, "s1", "a", "first"), idSubmit(t, "u2", "b", "typed"))
	rec := idleRecord(t, d, id)
	if len(rec.Turns) != 2 || rec.Turns[1].Source != TurnSourceUser || !rec.Steered || rec.Status != StatusRunning {
		t.Fatalf("human turn record=%+v", rec)
	}
	reportAll(t, d, id, idStop(t, "s2", "b", "reply"))
	rec = idleRecord(t, d, id)
	if rec.Turns[1].Outcome != TurnFinished || rec.Turns[1].Text != "reply" || rec.Turns[0].Text != "first" || rec.Status != StatusIdle {
		t.Fatalf("after human stop record=%+v", rec)
	}
}

// Two follow-ups with identical text are told apart by their prompt ids,
// whatever order their hooks arrive in.
func TestPromptIDIdenticalTextSendsKeepTheirOwnResults(t *testing.T) {
	d, _, id, _ := startArmedClaude(t)
	reportAll(t, d, id, idSubmit(t, "u1", "a", openingText), idStop(t, "s1", "a", "opening result"))
	first, err := d.Send(context.Background(), id, "go")
	if err != nil {
		t.Fatal(err)
	}
	reportAll(t, d, id, idSubmit(t, "u2", "b", "go"), idStop(t, "s2", "b", "first go"))
	second, err := d.Send(context.Background(), id, "go")
	if err != nil {
		t.Fatal(err)
	}
	// The second send's stop beats its submit.
	reportAll(t, d, id, idStop(t, "s3", "c", "second go"), idSubmit(t, "u3", "c", "go"))
	rec := idleRecord(t, d, id)
	if len(rec.Turns) != 3 || rec.Steered || rec.Status != StatusIdle {
		t.Fatalf("record=%+v", rec)
	}
	for turn, want := range map[string]string{rec.Turns[0].TurnID: "opening result", first.TurnID: "first go", second.TurnID: "second go"} {
		if got := turnByID(rec, turn); got.Outcome != TurnFinished || got.Text != want {
			t.Fatalf("turn %s = %+v, want finished %q", turn, got, want)
		}
	}
}

// The wake that continues a waiting turn gets a new prompt_id; its Stop
// finishes the turn the orchestrator is waiting on.
func TestPromptIDWakeAliasesTheWaitingTurn(t *testing.T) {
	d, _, id, now := startArmedClaude(t)
	reportAll(t, d, id, idSubmit(t, "u1", "a", openingText), idStopWaiting(t, "s1", "a", "I'll wait for the build."))
	rec := idleRecord(t, d, id)
	if rec.Status != StatusWaiting || len(rec.Turns) != 1 || rec.Turns[0].Outcome != "" {
		t.Fatalf("stop with pending work: %+v", rec)
	}
	advanceClock(now, time.Minute)
	reportAll(t, d, id, idSubmit(t, "u2", "w", taskNotification))
	rec = idleRecord(t, d, id)
	if rec.Status != StatusRunning || rec.PendingWork != nil || len(rec.Turns) != 1 || rec.Steered {
		t.Fatalf("after the wake: %+v", rec)
	}
	reportAll(t, d, id, idStop(t, "s2", "w", "Build passed."))
	rec = idleRecord(t, d, id)
	if len(rec.Turns) != 1 || rec.Turns[0].Outcome != TurnFinished || rec.Turns[0].Text != "I'll wait for the build.\n\nBuild passed." || rec.Status != StatusIdle {
		t.Fatalf("after the wake's stop: %+v", rec)
	}
}

func TestPromptIDWakeStopBeforeWakeSubmitStillFinishesTheWaitingTurn(t *testing.T) {
	d, _, id, _ := startArmedClaude(t)
	reportAll(t, d, id, idSubmit(t, "u1", "a", openingText), idStopWaiting(t, "s1", "a", "waiting"),
		idStop(t, "s2", "w", "Build passed."), idSubmit(t, "u2", "w", taskNotification))
	rec := idleRecord(t, d, id)
	if len(rec.Turns) != 1 || rec.Turns[0].Outcome != TurnFinished || rec.Turns[0].Text != "waiting\n\nBuild passed." || rec.Steered || rec.Status != StatusIdle {
		t.Fatalf("record=%+v", rec)
	}
}

// A stop for an id nothing is waiting on is held for its submit and never
// closes a turn that already belongs to another id.
func TestPromptIDStrayStopNeverClosesATurnBoundToAnotherID(t *testing.T) {
	d, _, id, _ := startArmedClaude(t)
	reportAll(t, d, id, idSubmit(t, "u1", "a", openingText), idStop(t, "s1", "stray", "not yours"))
	rec := idleRecord(t, d, id)
	if len(rec.Turns) != 1 || rec.Turns[0].Outcome != "" || rec.Status != StatusRunning {
		t.Fatalf("stray stop closed a bound turn: %+v", rec)
	}
	reportAll(t, d, id, idStop(t, "s2", "a", "mine"))
	if rec = idleRecord(t, d, id); rec.Turns[0].Text != "mine" || rec.Status != StatusIdle {
		t.Fatalf("record=%+v", rec)
	}
}

// Runs whose payloads carry no ids keep attributing by arming and order.
func TestPromptIDAbsentKeepsArrivalOrderAttribution(t *testing.T) {
	now := time.Date(2026, time.October, 8, 12, 0, 0, 0, time.UTC)
	d, _, id := startClaudeInteractive(t, &now)
	reportAll(t, d, id, claudeHook(t, "stop-1", "Stop", ""))
	rec := idleRecord(t, d, id)
	if len(rec.Turns) != 1 || rec.Turns[0].Outcome != TurnFinished || rec.Status != StatusIdle {
		t.Fatalf("id-less run record=%+v", rec)
	}
}

// Every arrival order of a two-turn script, replays included, yields each
// turn closed once with its own message and no extra turns. The second
// turn is a follow-up, so it starts only after the first closed.
func TestPromptIDEveryArrivalOrderAttributesEachTurnExactlyOnce(t *testing.T) {
	build := func(t *testing.T, promptID, prompt, message string) []HookReport {
		return []HookReport{
			idSubmit(t, "submit-"+promptID, promptID, prompt),
			idStop(t, "stop-"+promptID, promptID, message),
			idSubmit(t, "submit-"+promptID, promptID, prompt), // same event id: dedup drops it
			idStop(t, "stop-"+promptID+"-replay", promptID, message),
		}
	}
	for _, order := range permutations(4) {
		t.Run(fmt.Sprint(order), func(t *testing.T) {
			d, _, id, _ := startArmedClaude(t)
			first := build(t, "a", openingText, "result one")
			for _, i := range order {
				reportAll(t, d, id, first[i])
			}
			rec := idleRecord(t, d, id)
			if rec.Status != StatusIdle || len(rec.Turns) != 1 || rec.Turns[0].Text != "result one" || rec.Turns[0].Outcome != TurnFinished {
				t.Fatalf("first turn record=%+v", rec)
			}
			sent, err := d.Send(context.Background(), id, "second")
			if err != nil {
				t.Fatal(err)
			}
			second := build(t, "b", "second", "result two")
			for _, i := range order {
				reportAll(t, d, id, second[i])
			}
			rec = idleRecord(t, d, id)
			got := turnByID(rec, sent.TurnID)
			if len(rec.Turns) != 2 || rec.Steered || rec.Status != StatusIdle || got.Outcome != TurnFinished || got.Text != "result two" || rec.Turns[0].Text != "result one" {
				t.Fatalf("two-turn record=%+v", rec)
			}
			if d.slots.InUse() != 0 {
				t.Fatalf("slots in use = %d", d.slots.InUse())
			}
		})
	}
}

// permutations returns every ordering of 0..n-1.
func permutations(n int) [][]int {
	var out [][]int
	var rec func(prefix []int, rest []int)
	rec = func(prefix, rest []int) {
		if len(rest) == 0 {
			out = append(out, append([]int(nil), prefix...))
			return
		}
		for i := range rest {
			next := append(append([]int(nil), rest[:i]...), rest[i+1:]...)
			rec(append(prefix, rest[i]), next)
		}
	}
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	rec(nil, idx)
	return out
}

// ---- bridged runs ----------------------------------------------------

// A bridged run's turn.start names the leo command that submitted it, so a
// sent turn is claimed by command id, never by text or arming.

func bridgeStart(t *testing.T, eventID, turnID, prompt, commandID string) HookReport {
	t.Helper()
	fields := map[string]any{"hook_event_name": "UserPromptSubmit", "bridge_turn_id": turnID, "prompt": prompt}
	if commandID != "" {
		fields["command_id"] = commandID
		fields["origin"] = "plugin"
	}
	b, _ := json.Marshal(fields)
	return HookReport{EventID: eventID, Payload: b}
}

// bridgeWake is a turn.start the mod reports for a background task's
// notification: the origin says so, whatever the text.
func bridgeWake(t *testing.T, eventID, turnID, prompt string) HookReport {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"hook_event_name": "UserPromptSubmit", "bridge_turn_id": turnID, "prompt": prompt, "origin": originTaskNotification})
	return HookReport{EventID: eventID, Payload: b}
}

func bridgeStop(t *testing.T, eventID, turnID, message string) HookReport {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"hook_event_name": "Stop", "bridge_turn_id": turnID, "last_assistant_message": message})
	return HookReport{EventID: eventID, Payload: b}
}

func startBridgedIdle(t *testing.T) (*Dispatcher, *bridgedFakeRuntime, string) {
	t.Helper()
	d := NewDispatcher(newFakeRecorder())
	rt := &bridgedFakeRuntime{fakeInteractiveRuntime: &fakeInteractiveRuntime{arm: true, empty: true}, bridges: true}
	d.SetInteractiveRuntime(rt)
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "hello", Cwd: t.TempDir(), Mode: ModeInteractive})
	if err != nil {
		t.Fatal(err)
	}
	waitForArmed(t, d, started.ID)
	open := bridge.OpeningID(DispatchBridgeKey(started.ID), requestPrompt(Request{Prompt: "hello"}))
	reportAll(t, d, started.ID, bridgeStart(t, "bridge:turn.start:t1", "t1", "hello", open), bridgeStop(t, "bridge:turn.complete:t1", "t1", "opening result"))
	rec := idleRecord(t, d, started.ID)
	if rec.Status != StatusIdle || len(rec.Turns) != 1 || rec.Turns[0].Outcome != TurnFinished {
		t.Fatalf("bridged opening record=%+v", rec)
	}
	return d, rt, started.ID
}

func TestBridgedOpeningIsClaimedByItsCommandID(t *testing.T) {
	d, _, id := startBridgedIdle(t)
	rec := idleRecord(t, d, id)
	if rec.Turns[0].Text != "opening result" || rec.Steered {
		t.Fatalf("record=%+v", rec)
	}
}

func TestBridgedSendIsClaimedByCommandIDEvenWhenAPersonTypedTheSameText(t *testing.T) {
	d, rt, id := startBridgedIdle(t)
	sent, err := d.Send(context.Background(), id, "go")
	if err != nil {
		t.Fatal(err)
	}
	cmd := rt.lastCommandID()
	if cmd == "" {
		t.Fatal("the runtime was handed no command id for the follow-up")
	}
	// A person's identical prompt starts a turn first; it is theirs, not the send's.
	reportAll(t, d, id, bridgeStart(t, "e1", "t2", "go", ""))
	rec := idleRecord(t, d, id)
	if st := turnByID(rec, sent.TurnID); st.Delivered || len(rec.Turns) != 3 || !rec.Steered {
		t.Fatalf("a person's identical prompt claimed the send: %+v", rec)
	}
	reportAll(t, d, id, bridgeStart(t, "e2", "t3", "go", cmd), bridgeStop(t, "e3", "t3", "send result"), bridgeStop(t, "e4", "t2", "person result"))
	rec = idleRecord(t, d, id)
	if st := turnByID(rec, sent.TurnID); st.Outcome != TurnFinished || st.Text != "send result" {
		t.Fatalf("send turn = %+v", st)
	}
	if person := rec.Turns[2]; person.Source != TurnSourceUser || person.Outcome != TurnFinished || person.Text != "person result" || rec.Status != StatusIdle {
		t.Fatalf("person's turn = %+v record=%+v", person, rec)
	}
}

func TestBridgedStopBeforeItsStartIsHeldUntilTheStartArrives(t *testing.T) {
	d, rt, id := startBridgedIdle(t)
	sent, err := d.Send(context.Background(), id, "go")
	if err != nil {
		t.Fatal(err)
	}
	reportAll(t, d, id, bridgeStop(t, "e1", "t2", "send result"))
	if st := turnByID(idleRecord(t, d, id), sent.TurnID); st.Outcome != "" {
		t.Fatalf("a bridged stop with no start closed the armed send by guesswork: %+v", st)
	}
	reportAll(t, d, id, bridgeStart(t, "e2", "t2", "go", rt.lastCommandID()))
	rec := idleRecord(t, d, id)
	if st := turnByID(rec, sent.TurnID); st.Outcome != TurnFinished || st.Text != "send result" || len(rec.Turns) != 2 || rec.Steered || rec.Status != StatusIdle {
		t.Fatalf("record=%+v", rec)
	}
}

// A bridge turn that no leo command started and that is not a harness
// injection is a person's: it steers, and an injection does not.
func TestBridgedUnstampedTurnsAreAttributedByKind(t *testing.T) {
	d, _, id := startBridgedIdle(t)
	reportAll(t, d, id, bridgeWake(t, "e1", "t2", taskNotification))
	rec := idleRecord(t, d, id)
	if len(rec.Turns) != 2 || rec.Steered || rec.Turns[1].Source != TurnSourceUser {
		t.Fatalf("injected turn record=%+v", rec)
	}
	reportAll(t, d, id, bridgeStop(t, "e2", "t2", "handled"), bridgeStart(t, "e3", "t3", "typed", ""))
	if rec = idleRecord(t, d, id); !rec.Steered || len(rec.Turns) != 3 {
		t.Fatalf("typed turn record=%+v", rec)
	}
}

func TestBridgedWaitingTurnIsContinuedByTheWakeTurn(t *testing.T) {
	d, rt, id := startBridgedIdle(t)
	sent, err := d.Send(context.Background(), id, "build it")
	if err != nil {
		t.Fatal(err)
	}
	waiting, _ := json.Marshal(map[string]any{"hook_event_name": "Stop", "bridge_turn_id": "t2", "last_assistant_message": "waiting", "background_tasks": shellAndMonitor["background_tasks"], "session_crons": []any{}})
	reportAll(t, d, id, bridgeStart(t, "e1", "t2", "build it", rt.lastCommandID()), HookReport{EventID: "e2", Payload: waiting})
	if rec := idleRecord(t, d, id); rec.Status != StatusWaiting || turnByID(rec, sent.TurnID).Outcome != "" {
		t.Fatalf("waiting record=%+v", rec)
	}
	reportAll(t, d, id, bridgeWake(t, "e3", "t3", taskNotification), bridgeStop(t, "e4", "t3", "Build passed."))
	rec := idleRecord(t, d, id)
	if st := turnByID(rec, sent.TurnID); st.Outcome != TurnFinished || st.Text != "waiting\n\nBuild passed." || len(rec.Turns) != 2 || rec.Steered || rec.Status != StatusIdle {
		t.Fatalf("record=%+v", rec)
	}
}

// A turn.start naming a command no open sent turn holds (its turn already
// closed) is leo's own doing, so it does not read as a person steering.
func TestBridgedTurnForAnUnknownCommandDoesNotSteer(t *testing.T) {
	d, _, id := startBridgedIdle(t)
	reportAll(t, d, id, bridgeStart(t, "e1", "t2", "late", "cmd-nobody-holds"))
	rec := idleRecord(t, d, id)
	if rec.Steered || len(rec.Turns) != 2 || rec.Turns[1].Source != TurnSourceUser {
		t.Fatalf("record=%+v", rec)
	}
}

// A turn.complete that leaves background work pending only pauses its turn,
// even when it beats the turn.start that names the turn.
func TestBridgedStopWithPendingWorkBeforeItsStartStillWaits(t *testing.T) {
	d, rt, id := startBridgedIdle(t)
	sent, err := d.Send(context.Background(), id, "build it")
	if err != nil {
		t.Fatal(err)
	}
	waiting, _ := json.Marshal(map[string]any{"hook_event_name": "Stop", "bridge_turn_id": "t2", "last_assistant_message": "waiting", "background_tasks": shellAndMonitor["background_tasks"], "session_crons": []any{}})
	reportAll(t, d, id, HookReport{EventID: "e1", Payload: waiting}, bridgeStart(t, "e2", "t2", "build it", rt.lastCommandID()))
	rec := idleRecord(t, d, id)
	if st := turnByID(rec, sent.TurnID); st.Outcome != "" || !st.Delivered || rec.Status != StatusWaiting {
		t.Fatalf("record=%+v", rec)
	}
}

// The mod reports the origin of the prompt that started a turn: a background
// task's notification continues the working turn whatever text it carries.
func TestBridgedWakeOriginContinuesTheWorkingTurnWithoutItsEnvelope(t *testing.T) {
	d, rt, id := startBridgedIdle(t)
	sent, err := d.Send(context.Background(), id, "build it")
	if err != nil {
		t.Fatal(err)
	}
	reportAll(t, d, id, bridgeStart(t, "e1", "t2", "build it", rt.lastCommandID()))
	wake, _ := json.Marshal(map[string]any{"hook_event_name": "UserPromptSubmit", "bridge_turn_id": "t3", "prompt": "background job finished", "origin": "task-notification"})
	reportAll(t, d, id, HookReport{EventID: "e2", Payload: wake}, bridgeStop(t, "e3", "t3", "Build passed."))
	rec := idleRecord(t, d, id)
	if st := turnByID(rec, sent.TurnID); st.Outcome != TurnFinished || st.Text != "Build passed." || len(rec.Turns) != 2 || rec.Steered || rec.Status != StatusIdle {
		t.Fatalf("record=%+v", rec)
	}
}

// The mod's command stamp is the only claim on a sent turn: a deliver whose
// text happens to be a notification envelope is still the sent turn's.
func TestBridgedStampedStartWinsOverEnvelopeText(t *testing.T) {
	d, rt, id := startBridgedIdle(t)
	sent, err := d.Send(context.Background(), id, taskNotification)
	if err != nil {
		t.Fatal(err)
	}
	reportAll(t, d, id, bridgeStart(t, "e1", "t2", taskNotification, rt.lastCommandID()), bridgeStop(t, "e2", "t2", "done"))
	rec := idleRecord(t, d, id)
	if st := turnByID(rec, sent.TurnID); !st.Delivered || st.Outcome != TurnFinished || st.Text != "done" || len(rec.Turns) != 2 || rec.Steered || rec.Status != StatusIdle {
		t.Fatalf("record=%+v", rec)
	}
}

// A bridged start's text is never read for a notification envelope: only the
// origin the mod reports makes it a wake. An unstamped turn quoting the
// envelope is a person's.
func TestBridgedEnvelopeTextWithoutTheWakeOriginIsAPersonsTurn(t *testing.T) {
	d, _, id := startBridgedIdle(t)
	reportAll(t, d, id, bridgeStart(t, "e1", "t2", taskNotification, ""))
	rec := idleRecord(t, d, id)
	if len(rec.Turns) != 2 || rec.Turns[1].Source != TurnSourceUser || !rec.Steered {
		t.Fatalf("record=%+v", rec)
	}
}

// A stamp naming no open sent turn (unknown, or its turn already closed) is
// leo's own submit whose turn is gone: it claims nothing, and it is not a
// wake, so it neither continues nor steers the turn waiting on its own.
func TestBridgedStaleStampDoesNotBindToOrSteerAWaitingTurn(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stamp func(first string) string
	}{
		{"unknown command", func(string) string { return "cmd-nobody-holds" }},
		{"closed turn's command", func(first string) string { return first }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, rt, id := startBridgedIdle(t)
			done, err := d.Send(context.Background(), id, "first")
			if err != nil {
				t.Fatal(err)
			}
			firstCmd := rt.lastCommandID()
			reportAll(t, d, id, bridgeStart(t, "e1", "t2", "first", firstCmd), bridgeStop(t, "e2", "t2", "first done"))
			waiting, err := d.Send(context.Background(), id, "build it")
			if err != nil {
				t.Fatal(err)
			}
			pending, _ := json.Marshal(map[string]any{"hook_event_name": "Stop", "bridge_turn_id": "t3", "last_assistant_message": "waiting", "background_tasks": shellAndMonitor["background_tasks"], "session_crons": []any{}})
			reportAll(t, d, id, bridgeStart(t, "e3", "t3", "build it", rt.lastCommandID()), HookReport{EventID: "e4", Payload: pending})
			if rec := idleRecord(t, d, id); rec.Status != StatusWaiting || turnByID(rec, waiting.TurnID).Outcome != "" {
				t.Fatalf("waiting record=%+v", rec)
			}

			reportAll(t, d, id, bridgeStart(t, "e5", "t4", "stale", tc.stamp(firstCmd)), bridgeStop(t, "e6", "t4", "stale done"))
			rec := idleRecord(t, d, id)
			if w := turnByID(rec, waiting.TurnID); w.Outcome != "" || w.HarnessTurnID != "t3" || rec.Steered {
				t.Fatalf("a stale stamp reached the waiting turn: %+v steered=%v", w, rec.Steered)
			}
			if f := turnByID(rec, done.TurnID); f.Outcome != TurnFinished || f.Text != "first done" {
				t.Fatalf("the finished turn changed: %+v", f)
			}
			if len(rec.Turns) != 4 || rec.Turns[3].Source != TurnSourceUser || rec.Turns[3].Outcome != TurnFinished || rec.Turns[3].Text != "stale done" {
				t.Fatalf("the stale start did not run as a turn of its own: %+v", rec.Turns)
			}
		})
	}
}
