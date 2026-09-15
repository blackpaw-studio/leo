package consult

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"reflect"
	"testing"
	"time"
)

func TestRecordFoundationJSONRoundTripAndClone(t *testing.T) {
	legacy := []byte(`{"id":"d-old","template":"worker","harness":"claude","model":"sonnet","kind":"dispatch","cwd":"/tmp","prompt":"go","status":"done","started_at":"2026-09-15T00:00:00Z"}`)
	var old Record
	if err := json.Unmarshal(legacy, &old); err != nil {
		t.Fatal(err)
	}
	if old.InputTokens != nil || old.Notifications != nil {
		t.Fatalf("legacy fields unexpectedly populated: %+v", old)
	}
	n := true
	in, out, turns, tools, cost := int64(0), int64(2), 3, 4, 1.25
	when := time.Date(2026, 9, 15, 1, 2, 3, 0, time.UTC)
	old.Notify, old.InputTokens, old.OutputTokens, old.UsageTurns, old.ToolCalls, old.CostUSD = n, &in, &out, &turns, &tools, &cost
	old.CallerPaneID, old.CallerHarness, old.CallerSessionID = "%7", "codex", "$2"
	old.Isolation, old.Worktree, old.Branch, old.BaseCommit = "worktree", "/tmp/w", "leo/x", "abc"
	old.SourceCwd, old.RepositoryRoot, old.WorktreeState = "/tmp/src", "/tmp/repo", WorktreePresent
	old.Notifications = map[string]Notification{"d-old": {Disposition: NotificationPending, PendingAt: when}}
	clone := cloneRecord(old)
	if !reflect.DeepEqual(old, clone) {
		t.Fatalf("clone differs before mutation\nold=%+v\nclone=%+v", old, clone)
	}
	*clone.InputTokens = 99
	clone.Notifications["d-old"] = Notification{Disposition: NotificationFailed}
	if *old.InputTokens != 0 || old.Notifications["d-old"].Disposition != NotificationPending {
		t.Fatal("clone aliases pointer or map fields")
	}
	raw, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	var round Record
	if err := json.Unmarshal(raw, &round); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(old, round) {
		t.Fatalf("round trip mismatch\nold=%+v\nnew=%+v", old, round)
	}
}

func TestWaitAllowsOverlappingWaitersTimeoutAndSend(t *testing.T) {
	d := NewDispatcher(nil)
	s := &runState{record: Record{ID: "d-wait", Mode: ModeHeadless, Status: StatusRunning}, done: make(chan struct{})}
	d.runs[s.record.ID] = s
	first, second := make(chan []Entry, 1), make(chan []Entry, 1)
	go func() { first <- d.Wait(context.Background(), []string{s.record.ID}, time.Hour) }()
	go func() { second <- d.Wait(context.Background(), []string{s.record.ID}, time.Hour) }()
	deadline := time.Now().Add(time.Second)
	for d.waitCount(s.record.ID) != 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := d.waitCount(s.record.ID); got != 2 {
		t.Fatalf("registered waits = %d, want 2", got)
	}
	timed := make(chan []Entry, 1)
	go func() { timed <- d.Wait(context.Background(), []string{s.record.ID}, 10*time.Millisecond) }()
	select {
	case got := <-timed:
		if len(got) != 1 || got[0].Status != StatusRunning {
			t.Fatalf("timeout result = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("short wait blocked behind another wait")
	}
	sent := make(chan error, 1)
	go func() { _, err := d.Send(context.Background(), s.record.ID, "hello"); sent <- err }()
	select {
	case err := <-sent:
		if err == nil || err.Error() != "dispatch is not interactive" {
			t.Fatalf("send error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Send blocked behind pending Wait")
	}
	d.mu.Lock()
	s.record.Status, s.record.Text = StatusDone, "done"
	d.mu.Unlock()
	close(s.done)
	for name, ch := range map[string]<-chan []Entry{"first": first, "second": second} {
		select {
		case got := <-ch:
			if got[0].Status != StatusDone || got[0].Text != "done" {
				t.Fatalf("%s result = %+v", name, got)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s waiter did not return", name)
		}
	}
}

func TestWaitRegistersResolvedTargetsAtomically(t *testing.T) {
	d := NewDispatcher(nil)
	s := &runState{record: Record{ID: "d-atomic", Mode: ModeHeadless, Status: StatusRunning}, handle: nopHandle{}, done: make(chan struct{})}
	d.runs[s.record.ID] = s
	resolved, release, completed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	d.waitResolvedHook = func() { close(resolved); <-release }
	go func() {
		<-resolved
		d.mu.Lock()
		s.record.Status, s.record.Text = StatusDone, "done"
		d.mu.Unlock()
		close(s.done)
		close(completed)
	}()
	result := make(chan []Entry, 1)
	go func() { result <- d.Wait(context.Background(), []string{s.record.ID}, time.Hour) }()
	<-resolved
	select {
	case <-completed:
		t.Fatal("completion slipped between resolution and wait registration")
	default:
	}
	close(release)
	select {
	case got := <-result:
		if got[0].Status != StatusDone {
			t.Fatalf("wait result = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("wait did not observe completion")
	}
}

func TestSerialLocksAreReclaimed(t *testing.T) {
	d := NewDispatcher(nil)
	for i := 0; i < 20; i++ {
		unlock := d.serialLocks([]string{fmt.Sprintf("unknown-%d", i)})
		unlock()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.serial) != 0 {
		t.Fatalf("serial lock entries = %d, want 0", len(d.serial))
	}
}

func TestEntryFoundationJSONRoundTrip(t *testing.T) {
	in, out, turns, tools, cost := int64(0), int64(2), 3, 4, 1.25
	want := Entry{ID: "d-x", Status: StatusDone, InputTokens: &in, OutputTokens: &out, UsageTurns: &turns, ToolCalls: &tools, CostUSD: &cost, Worktree: "/tmp/w", Branch: "leo/x"}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got Entry
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("round trip mismatch: %#v != %#v", got, want)
	}
}

func TestDispatchNotifyDefaultAndIsolationValidation(t *testing.T) {
	d := NewDispatcher(nil)
	if _, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "q", Cwd: t.TempDir(), Isolation: "bogus"}); err == nil || err.Error() != `isolation must be empty or "worktree"` {
		t.Fatalf("invalid isolation error = %v", err)
	}
	rec := newFakeRecorder()
	d = NewDispatcher(rec)
	d.ExecCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "echo", `{"type":"result","result":"ok","is_error":false}`)
	}
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "q", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	<-d.runs[started.ID].done
	got, _ := d.Get(started.ID)
	if !got.Notify {
		t.Fatal("dispatch notify did not default true")
	}
	force := true
	result, err := d.Consult(context.Background(), testConfig(), Request{Template: "claude", Prompt: "q", Cwd: t.TempDir(), Notify: &force})
	if err != nil {
		t.Fatal(err)
	}
	consultRec, _ := d.Get(result.ID)
	if consultRec.Notify {
		t.Fatal("consult unexpectedly notifies")
	}
}

func TestHeadlessPersistsSessionIDEvenOnHarnessError(t *testing.T) {
	d := NewDispatcher(nil)
	d.ExecCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", `printf '%s\n' '{"type":"result","session_id":"sess-1","is_error":true,"errors":["bad"]}'`)
	}
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "q", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	<-d.runs[started.ID].done
	got, _ := d.Get(started.ID)
	if got.SessionID != "sess-1" {
		t.Fatalf("session id = %q", got.SessionID)
	}
}

func TestWaitCoverageUsesResolvedTransitionKeys(t *testing.T) {
	d := NewDispatcher(nil)
	s := &runState{record: Record{ID: "d-x", Mode: ModeInteractive, Status: StatusRunning, Turns: []Turn{{TurnID: "d-x#1", Source: TurnSourceOrchestrator}, {TurnID: "d-x#2", Source: TurnSourceOrchestrator}}}, done: make(chan struct{})}
	d.runs["d-x"] = s
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Wait(ctx, []string{"d-x#1"}, time.Hour); close(done) }()
	for i := 0; i < 100 && !d.waitCovers("d-x#1"); i++ {
		time.Sleep(time.Millisecond)
	}
	if !d.waitCovers("d-x#1") || d.waitCovers("d-x#2") {
		t.Fatalf("coverage older=%v newer=%v", d.waitCovers("d-x#1"), d.waitCovers("d-x#2"))
	}
	cancel()
	<-done
	if d.waitCovers("d-x#1") {
		t.Fatal("coverage leaked after cancel")
	}
}

func TestCollectSerializesPerRecord(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	d := NewDispatcherWithOnStart(nil, context.Background(), nil, func(Record) { entered <- struct{}{}; <-release })
	rec := Record{ID: "d-x"}
	first := make(chan struct{})
	second := make(chan struct{})
	go func() { d.Collect(rec); close(first) }()
	<-entered
	go func() { d.Collect(rec); close(second) }()
	select {
	case <-second:
		t.Fatal("second collect was not serialized")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	<-first
	<-second
}

func TestTransitionKey(t *testing.T) {
	if transitionKey("d-x", ModeHeadless, "") != "d-x" || transitionKey("d-x", ModeInteractive, "d-x#2") != "d-x#2" {
		t.Fatal("unexpected transition key")
	}
}
