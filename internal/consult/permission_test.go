package consult

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func permissionPayload(t *testing.T, tool string, input map[string]any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]any{"hook_event_name": "PermissionRequest", "session_id": "s1", "tool_name": tool, "tool_input": input})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// startRunningClaude starts a claude interactive dispatch whose opening turn
// is delivered and working, with the real clock.
func startRunningClaude(t *testing.T) (*Dispatcher, string) {
	t.Helper()
	now := time.Now()
	d, _, id := startClaudeInteractive(t, &now)
	d.now = time.Now
	if rec, _ := d.Get(id); rec.Status != StatusRunning {
		t.Fatalf("status = %s, want running", rec.Status)
	}
	return d, id
}

// requestAsync files a permission request the way the hook's long poll does
// and returns the channel its decision arrives on.
func requestAsync(t *testing.T, ctx context.Context, d *Dispatcher, id string, timeout time.Duration) <-chan PermissionDecision {
	t.Helper()
	out := make(chan PermissionDecision, 1)
	go func() {
		out <- d.RequestPermission(ctx, id, permissionPayload(t, "Bash", map[string]any{"command": "go test ./...", "description": "run tests"}), timeout)
	}()
	waitForStatus(t, d, id, StatusNeedsInput)
	return out
}

func waitForStatus(t *testing.T, d *Dispatcher, id string, want Status) Record {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		rec, _ := d.Get(id)
		if rec.Status == want {
			return rec
		}
		select {
		case <-deadline:
			t.Fatalf("status = %s, want %s", rec.Status, want)
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func receiveDecision(t *testing.T, ch <-chan PermissionDecision) PermissionDecision {
	t.Helper()
	select {
	case got := <-ch:
		return got
	case <-time.After(time.Second):
		t.Fatal("permission request never returned")
		return PermissionDecision{}
	}
}

func TestPermissionAllowRoundTrip(t *testing.T) {
	d, id := startRunningClaude(t)
	decision := requestAsync(t, context.Background(), d, id, time.Minute)

	rec, _ := d.Get(id)
	if rec.NeedsInput == nil || rec.NeedsInput.Kind != "permission" || rec.NeedsInput.Tool != "Bash" || rec.NeedsInput.Summary != "go test ./..." || rec.NeedsInput.RequestID == "" {
		t.Fatalf("needs_input = %+v", rec.NeedsInput)
	}
	entry := d.Wait(context.Background(), []string{id}, 5*time.Second)[0]
	if entry.Status != StatusNeedsInput || entry.NeedsInput == nil || entry.NeedsInput.RequestID != rec.NeedsInput.RequestID {
		t.Fatalf("wait did not return early on needs_input: %+v", entry)
	}

	if _, err := d.Decide(id, Decision{Behavior: "allow"}); err != nil {
		t.Fatal(err)
	}
	if got := receiveDecision(t, decision); got != (PermissionDecision{Behavior: "allow"}) {
		t.Fatalf("decision = %+v", got)
	}
	rec = waitForStatus(t, d, id, StatusRunning)
	if rec.NeedsInput != nil {
		t.Fatalf("needs_input left behind: %+v", rec.NeedsInput)
	}
}

func TestPermissionDenyCarriesReason(t *testing.T) {
	d, id := startRunningClaude(t)
	decision := requestAsync(t, context.Background(), d, id, time.Minute)
	rec, _ := d.Get(id)
	if _, err := d.Decide(id, Decision{Behavior: "deny", Reason: "use make test", RequestID: rec.NeedsInput.RequestID}); err != nil {
		t.Fatal(err)
	}
	if got := receiveDecision(t, decision); got != (PermissionDecision{Behavior: "deny", Message: "use make test"}) {
		t.Fatalf("decision = %+v", got)
	}
}

func TestPermissionTimeoutFallsBackAndLateDecisionIsStale(t *testing.T) {
	d, id := startRunningClaude(t)
	decision := requestAsync(t, context.Background(), d, id, 50*time.Millisecond)
	if got := receiveDecision(t, decision); got != (PermissionDecision{}) {
		t.Fatalf("timed-out request decided %+v, want no decision", got)
	}
	waitForStatus(t, d, id, StatusRunning)
	if _, err := d.Decide(id, Decision{Behavior: "allow"}); err == nil || !strings.Contains(err.Error(), "no pending permission") {
		t.Fatalf("late decision err = %v, want stale rejection", err)
	}
}

func TestPermissionHookDisconnectWithdrawsRequest(t *testing.T) {
	d, id := startRunningClaude(t)
	ctx, cancel := context.WithCancel(context.Background())
	decision := requestAsync(t, ctx, d, id, time.Minute)
	cancel()
	if got := receiveDecision(t, decision); got != (PermissionDecision{}) {
		t.Fatalf("withdrawn request decided %+v", got)
	}
	waitForStatus(t, d, id, StatusRunning)
}

func TestPermissionDecisionForAnotherRequestIsStale(t *testing.T) {
	d, id := startRunningClaude(t)
	requestAsync(t, context.Background(), d, id, time.Minute)
	if _, err := d.Decide(id, Decision{Behavior: "allow", RequestID: id + "#perm99"}); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("err = %v, want stale request id rejection", err)
	}
	if _, err := d.Decide(id, Decision{Behavior: "maybe"}); err == nil {
		t.Fatal("unknown decision accepted")
	}
}

func TestPermissionSendMessageRejectedWithHint(t *testing.T) {
	d, id := startRunningClaude(t)
	requestAsync(t, context.Background(), d, id, time.Minute)
	_, err := d.Send(context.Background(), id, "hello")
	if err == nil || !strings.Contains(err.Error(), "decision") {
		t.Fatalf("send err = %v, want a decision hint", err)
	}
}

func TestPermissionTurnEndWithdrawsRequest(t *testing.T) {
	d, id := startRunningClaude(t)
	decision := requestAsync(t, context.Background(), d, id, time.Minute)
	// The prompt was answered in the pane and the turn ran to its end.
	if err := d.Report(id, claudeHook(t, "stop-1", "Stop", "")); err != nil {
		t.Fatal(err)
	}
	if got := receiveDecision(t, decision); got != (PermissionDecision{}) {
		t.Fatalf("decision = %+v, want none once the turn ended", got)
	}
	rec := waitForStatus(t, d, id, StatusIdle)
	if rec.NeedsInput != nil {
		t.Fatalf("needs_input left behind: %+v", rec.NeedsInput)
	}
}

func TestPermissionCancelWithdrawsRequest(t *testing.T) {
	d, id := startRunningClaude(t)
	decision := requestAsync(t, context.Background(), d, id, time.Minute)
	if _, err := d.Cancel(id); err != nil {
		t.Fatal(err)
	}
	if got := receiveDecision(t, decision); got != (PermissionDecision{}) {
		t.Fatalf("decision = %+v after cancel", got)
	}
}

func TestPermissionRequestNotifiesCaller(t *testing.T) {
	d := NewDispatcher(newFakeRecorder())
	rt := &fakeInteractiveRuntime{arm: true, empty: true}
	d.SetInteractiveRuntime(rt)
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Name: "impl", Prompt: "hello", Cwd: t.TempDir(), Mode: ModeInteractive, CallerPaneID: "%0", CallerHarness: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	waitForArmed(t, d, started.ID)
	d.mu.Lock()
	d.runs[started.ID].handle = &durableTestHandle{}
	d.mu.Unlock()
	_ = d.Report(started.ID, claudeHook(t, "submit-1", "UserPromptSubmit", dispatchPreamble+" hello"))
	requestAsync(t, context.Background(), d, started.ID, time.Minute)
	rec, _ := d.Get(started.ID)
	n, ok := rec.Notifications[rec.NeedsInput.RequestID]
	if !ok || n.Disposition != NotificationPending {
		t.Fatalf("notifications = %+v, want a pending needs_input notification", rec.Notifications)
	}
	for _, want := range []string{started.ID, "needs_input", "Bash", "decision"} {
		if !strings.Contains(n.Message, want) {
			t.Fatalf("notification %q lacks %q", n.Message, want)
		}
	}
}

func TestPermissionRequestForSettledRunReturnsAtOnce(t *testing.T) {
	d, id := startRunningClaude(t)
	if _, err := d.Cancel(id); err != nil {
		t.Fatal(err)
	}
	done := make(chan PermissionDecision, 1)
	go func() {
		done <- d.RequestPermission(context.Background(), id, permissionPayload(t, "Bash", map[string]any{"command": "ls"}), time.Minute)
	}()
	if got := receiveDecision(t, done); got != (PermissionDecision{}) {
		t.Fatalf("decision = %+v", got)
	}
}
