package consult

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// releaseTree is a live root plus MaxLiveDescendantsPerRoot idle, pool-exempt
// interactive children that opted into release_on_finish, with the shared
// pool's only slot held by an unrelated top-level run.
func releaseTree(t *testing.T) (*Dispatcher, *fakeInteractiveRuntime, []string) {
	t.Helper()
	d := NewDispatcher(newFakeRecorder())
	now := time.Unix(1000, 0)
	d.now = func() time.Time { return now }
	rt := &fakeInteractiveRuntime{alive: true}
	d.SetInteractiveRuntime(rt)
	d.slots = newSlotLimiter(1)
	if !d.slots.TryAcquire() {
		t.Fatal("could not occupy the shared pool")
	}
	d.runs["d-root"] = newRunState(Record{ID: "d-root", Kind: "dispatch", Mode: ModeHeadless, Status: StatusRunning, StartedAt: now}, &durableTestHandle{}, make(chan struct{}), nil)
	var ids []string
	for i := 0; i < MaxLiveDescendantsPerRoot; i++ {
		id := fmt.Sprintf("d-c%d", i)
		ids = append(ids, id)
		d.runs[id] = newRunState(Record{
			ID: id, Kind: "dispatch", Mode: ModeInteractive, Status: StatusIdle, PaneID: fmt.Sprintf("%%%d", i+10),
			ParentDispatchID: "d-root", SlotExempt: true, ReleaseOnFinish: true, StartedAt: now,
			Turns: []Turn{{TurnID: id + "#1", Source: TurnSourceOrchestrator, Delivered: true, Outcome: TurnFinished, Text: "answer"}},
		}, &durableTestHandle{}, make(chan struct{}), nil)
	}
	return d, rt, ids
}

func admitsAnotherChild(d *Dispatcher) error {
	d.nestMu.Lock()
	defer d.nestMu.Unlock()
	_, err := d.admitNested("d-root")
	return err
}

func TestReleasedChildFreesItsPlaceUnderTheRootsCapWithoutTouchingThePool(t *testing.T) {
	for _, tc := range []struct {
		name    string
		release func(d *Dispatcher, id string) error
	}{
		{"release_on_finish", func(d *Dispatcher, id string) error {
			return d.WaitAndRelease(context.Background(), []string{id}, 0, func([]Entry) error { return nil })
		}},
		{"manual leo_release", func(d *Dispatcher, id string) error { _, err := d.Release(id); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, rt, ids := releaseTree(t)
			if admitsAnotherChild(d) == nil {
				t.Fatal("the root's cap is full yet another child was admitted")
			}
			if err := tc.release(d, ids[0]); err != nil {
				t.Fatal(err)
			}
			if rec := status(d, ids[0]); rec.Status != StatusReleased || rt.killCount() != 1 {
				t.Fatalf("status=%s kills=%d", rec.Status, rt.killCount())
			}
			if err := admitsAnotherChild(d); err != nil {
				t.Fatalf("a released child kept its place under the root's cap: %v", err)
			}
			// Releasing an exempt child must not hand back a place in the
			// shared pool it never took: it stays held by its real owner.
			if d.slots.used != 1 || d.exempt.used != 0 {
				t.Fatalf("pool used=%d exempt used=%d, want 1 and 0", d.slots.used, d.exempt.used)
			}
			// A second release of the same run is a no-op, not a second
			// release of anything.
			if err := tc.release(d, ids[0]); err != nil {
				t.Fatal(err)
			}
			if d.slots.used != 1 || d.exempt.used != 0 || rt.killCount() != 1 {
				t.Fatalf("double release: pool used=%d exempt used=%d kills=%d", d.slots.used, d.exempt.used, rt.killCount())
			}
		})
	}
}
