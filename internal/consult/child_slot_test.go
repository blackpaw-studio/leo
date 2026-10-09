package consult

import (
	"context"
	"errors"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
)

// firstLaunch maps a newSlotDispatcher to the channel closed by its first
// launch. A run launches from its own goroutine, so Start returning does not
// mean the parent has launched; a test that starts a child must wait for it,
// or the child can take the parent's "stay running" launch and hang.
var firstLaunch sync.Map

// newSlotDispatcher is a dispatcher with one slot whose first launch (the
// parent) stays running and every later launch (a child) answers at once.
func newSlotDispatcher(t *testing.T) *Dispatcher {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	var mu sync.Mutex
	launches := 0
	launched := make(chan struct{})
	d := NewDispatcher(nil)
	firstLaunch.Store(d, launched)
	t.Cleanup(func() { firstLaunch.Delete(d) })
	d.ExecCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		mu.Lock()
		defer mu.Unlock()
		launches++
		if launches == 1 {
			close(launched)
			return exec.CommandContext(ctx, "sleep", "30")
		}
		return exec.CommandContext(ctx, "sh", "-c", `echo '{"type":"result","result":"child done","is_error":false}'`)
	}
	return d
}

// oneSlotConfig caps dispatch concurrency at one, which Start applies.
func oneSlotConfig() *config.Config {
	one := 1
	cfg := testConfig()
	cfg.Defaults.Dispatch.MaxConcurrent = &one
	return cfg
}

// startNestedOneSlot is startNested under oneSlotConfig.
func startNestedOneSlot(t *testing.T, d *Dispatcher, req Request) Record {
	t.Helper()
	req.Template, req.Prompt, req.Kind = "claude", "q", "dispatch"
	req.Cwd = t.TempDir()
	started, err := d.Start(context.Background(), oneSlotConfig(), req)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	rec, err := d.Get(started.ID)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

// startHeadlessParent starts the top-level headless dispatch of a
// newSlotDispatcher and returns once its process has launched, so the next
// launch is certainly a child's.
func startHeadlessParent(t *testing.T, d *Dispatcher, req Request) Record {
	t.Helper()
	parent := startNestedOneSlot(t, d, req)
	launched, _ := firstLaunch.Load(d)
	select {
	case <-launched.(chan struct{}):
	case <-time.After(10 * time.Second):
		t.Fatal("the parent never launched")
	}
	return parent
}

func TestChildOfALiveDispatchRunsAtConcurrencyOne(t *testing.T) {
	d := newSlotDispatcher(t)
	parent := startHeadlessParent(t, d, Request{Caller: "alpha"})
	child := startNestedOneSlot(t, d, Request{ParentDispatchID: parent.ID})

	entries := d.Wait(context.Background(), []string{child.ID}, 5*time.Second)
	if len(entries) != 1 || entries[0].Status != StatusDone || entries[0].Text != "child done" {
		t.Fatalf("entries = %+v: the child queued behind its own waiting parent", entries)
	}
	if got := d.slots.InUse(); got != 1 {
		t.Fatalf("pool slots in use = %d, want only the parent's 1 (the child takes none)", got)
	}
}

func TestInteractiveChildOfALiveDispatchStartsAtConcurrencyOne(t *testing.T) {
	d := newSlotDispatcher(t)
	rt := &fakeInteractiveRuntime{arm: true, empty: true}
	d.SetInteractiveRuntime(rt)
	parent := startHeadlessParent(t, d, Request{Caller: "alpha"})
	started, err := d.Start(context.Background(), oneSlotConfig(), Request{Template: "codex", Prompt: "q", Cwd: t.TempDir(), Mode: ModeInteractive, ParentDispatchID: parent.ID})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if started.Queued {
		t.Fatal("an interactive child of a live dispatch queued for a pool slot")
	}
	if got := d.slots.InUse(); got != 1 || d.slots.Waiting() != 0 {
		t.Fatalf("pool: in use %d waiting %d, want the parent's 1 and nobody waiting", got, d.slots.Waiting())
	}
}

func TestInteractiveParentAndItsHeadlessChildShareNothingAtConcurrencyOne(t *testing.T) {
	d := newSlotDispatcher(t)
	d.SetInteractiveRuntime(&fakeInteractiveRuntime{arm: true, empty: true})
	started, err := d.Start(context.Background(), oneSlotConfig(), Request{Template: "codex", Prompt: "q", Cwd: t.TempDir(), Mode: ModeInteractive, Caller: "alpha"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// The interactive parent launches no process here, so the first headless
	// launch is the child and must answer at once.
	d.ExecCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", `echo '{"type":"result","result":"child done","is_error":false}'`)
	}
	child := startNestedOneSlot(t, d, Request{ParentDispatchID: started.ID})
	if entries := d.Wait(context.Background(), []string{child.ID}, 5*time.Second); entries[0].Status != StatusDone {
		t.Fatalf("entries = %+v", entries)
	}
}

// The parent keeps its own slot while it waits, so a child that needs
// approval is reachable: the parent's wait returns needs_input and it can decide.
func TestParentWaitingOnAChildThatNeedsInputCanApproveIt(t *testing.T) {
	d := newSlotDispatcher(t)
	d.SetInteractiveRuntime(&fakeInteractiveRuntime{arm: true, empty: true})
	parent := startHeadlessParent(t, d, Request{Caller: "alpha"})
	started, err := d.Start(context.Background(), oneSlotConfig(), Request{Template: "codex", Prompt: "q", Cwd: t.TempDir(), Mode: ModeInteractive, ParentDispatchID: parent.ID})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	decided := make(chan PermissionDecision, 1)
	go func() {
		decided <- d.RequestPermission(context.Background(), started.ID, []byte(`{"tool_name":"Bash","tool_input":{"command":"ls"}}`), 20*time.Second)
	}()
	entries := d.Wait(context.Background(), []string{started.ID}, 5*time.Second)
	if entries[0].Status != StatusNeedsInput || entries[0].NeedsInput == nil {
		t.Fatalf("entry = %+v, want needs_input", entries[0])
	}
	if _, err := d.Decide(started.ID, Decision{Behavior: "allow", RequestID: entries[0].NeedsInput.RequestID}); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if got := <-decided; got.Behavior != "allow" {
		t.Fatalf("decision = %+v", got)
	}
}

func TestConsultFromADispatchRunsAtConcurrencyOne(t *testing.T) {
	d := newSlotDispatcher(t)
	parent := startHeadlessParent(t, d, Request{Caller: "alpha"})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	res, err := d.Consult(ctx, oneSlotConfig(), Request{Template: "claude", Prompt: "q", Cwd: t.TempDir(), ParentDispatchID: parent.ID})
	if err != nil || res.Text != "child done" {
		t.Fatalf("consult = %+v, %v", res, err)
	}
}

func TestTopLevelDispatchesStillQueueBehindThePool(t *testing.T) {
	d := newSlotDispatcher(t)
	startNestedOneSlot(t, d, Request{Caller: "alpha"})
	second := startNestedOneSlot(t, d, Request{Caller: "beta"})
	if got := d.slots.Waiting(); got != 1 {
		t.Fatalf("waiting = %d, want the second top-level dispatch queued", got)
	}
	if entries := d.Wait(context.Background(), []string{second.ID}, 300*time.Millisecond); entries[0].Status.Terminal() {
		t.Fatalf("second top-level dispatch ran past the cap: %+v", entries)
	}
}

func TestChildOfAFinishedDispatchStillUsesThePool(t *testing.T) {
	d := newNestedDispatcher(t) // every launch stays running
	parent := startNestedOneSlot(t, d, Request{Caller: "alpha"})
	if _, err := d.Cancel(parent.ID); err != nil {
		t.Fatal(err)
	}
	d.Wait(context.Background(), []string{parent.ID}, 5*time.Second)
	waitUntil(t, "the pool to drain", func() bool { return d.slots.InUse() == 0 })
	startNestedOneSlot(t, d, Request{Caller: "beta"}) // takes the only slot
	startNestedOneSlot(t, d, Request{ParentDispatchID: parent.ID})
	if got := d.slots.Waiting(); got != 1 {
		t.Fatalf("waiting = %d: a child of a finished dispatch must queue like any other", got)
	}
}

func TestNestingDepthIsCappedBeforeAnythingIsRecorded(t *testing.T) {
	d := newNestedDispatcher(t)
	parent := ""
	for depth := 0; depth <= MaxDispatchDepth; depth++ {
		rec := startNested(t, d, Request{ParentDispatchID: parent})
		parent = rec.ID
	}
	before := len(d.Records())
	_, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "q", Cwd: t.TempDir(), Kind: "dispatch", ParentDispatchID: parent})
	var invalid *ValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("Start past the depth cap = %v, want an invalid-request error", err)
	}
	if got := err.Error(); !containsAll(got, "depth", "4") {
		t.Fatalf("error %q should name the depth limit", got)
	}
	if after := len(d.Records()); after != before {
		t.Fatalf("a rejected dispatch left %d new records", after-before)
	}
}

func rejectedForDescendants(t *testing.T, d *Dispatcher, parent string) error {
	t.Helper()
	before := len(d.Records())
	_, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "q", Cwd: t.TempDir(), Kind: "dispatch", ParentDispatchID: parent})
	if err == nil {
		return nil
	}
	var invalid *ValidationError
	if !errors.As(err, &invalid) || !containsAll(err.Error(), "6", "descendants") {
		t.Fatalf("err = %v, want an invalid-request error naming the 6-descendant limit", err)
	}
	if after := len(d.Records()); after != before {
		t.Fatalf("a rejected dispatch left %d new records", after-before)
	}
	return err
}

func TestSeventhLiveDescendantOfOneRootIsRejected(t *testing.T) {
	d := newNestedDispatcher(t)
	root := startNested(t, d, Request{Caller: "alpha"})
	for i := 0; i < MaxLiveDescendantsPerRoot; i++ {
		startNested(t, d, Request{ParentDispatchID: root.ID})
	}
	if rejectedForDescendants(t, d, root.ID) == nil {
		t.Fatalf("the %dth live descendant was admitted", MaxLiveDescendantsPerRoot+1)
	}
	// Another root is unaffected.
	other := startNested(t, d, Request{Caller: "beta"})
	startNested(t, d, Request{ParentDispatchID: other.ID})
}

func TestGrandchildrenCountTowardTheRoot(t *testing.T) {
	d := newNestedDispatcher(t)
	root := startNested(t, d, Request{Caller: "alpha"})
	child := startNested(t, d, Request{ParentDispatchID: root.ID})
	for i := 0; i < MaxLiveDescendantsPerRoot-1; i++ {
		startNested(t, d, Request{ParentDispatchID: child.ID})
	}
	if rejectedForDescendants(t, d, root.ID) == nil {
		t.Fatal("a direct child was admitted past the root's cap")
	}
	if rejectedForDescendants(t, d, child.ID) == nil {
		t.Fatal("another grandchild was admitted past the root's cap")
	}
}

func TestConsultsCountTowardTheRootsDescendants(t *testing.T) {
	d := newNestedDispatcher(t)
	root := startNested(t, d, Request{Caller: "alpha"})
	for i := 0; i < MaxLiveDescendantsPerRoot; i++ {
		started, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "q", Cwd: t.TempDir(), Kind: "consult", ParentDispatchID: root.ID})
		if err != nil {
			t.Fatalf("consult %d: %v", i, err)
		}
		_ = started
	}
	if rejectedForDescendants(t, d, root.ID) == nil {
		t.Fatal("a dispatch was admitted past a cap already filled by consults")
	}
}

func TestFinishedDescendantsFreeTheirPlaceUnderTheCap(t *testing.T) {
	d := newNestedDispatcher(t)
	root := startNested(t, d, Request{Caller: "alpha"})
	var first Record
	for i := 0; i < MaxLiveDescendantsPerRoot; i++ {
		rec := startNested(t, d, Request{ParentDispatchID: root.ID})
		if i == 0 {
			first = rec
		}
	}
	if _, err := d.Cancel(first.ID); err != nil {
		t.Fatal(err)
	}
	d.Wait(context.Background(), []string{first.ID}, 5*time.Second)
	startNested(t, d, Request{ParentDispatchID: root.ID}) // a place opened up
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestConcurrentStartsCannotOvershootTheRootsCap(t *testing.T) {
	d := newNestedDispatcher(t)
	root := startNested(t, d, Request{Caller: "alpha"})
	var wg sync.WaitGroup
	var mu sync.Mutex
	admitted := 0
	for i := 0; i < 3*MaxLiveDescendantsPerRoot; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "q", Cwd: t.TempDir(), Kind: "dispatch", ParentDispatchID: root.ID})
			if err == nil {
				mu.Lock()
				admitted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if admitted != MaxLiveDescendantsPerRoot {
		t.Fatalf("admitted %d concurrent children, want exactly %d", admitted, MaxLiveDescendantsPerRoot)
	}
}
