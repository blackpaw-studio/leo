package consult

import (
	"testing"

	"github.com/blackpaw-studio/leo/internal/observe"
)

// A tick's dispatch snapshot read before a rename and applied after it must
// not release the renamed agent's B-051 hold while its dispatch still runs.
func TestDispatchSnapshotAppliedAcrossARenameKeepsTheHold(t *testing.T) {
	rec := agentDispatch("d1", "old", StatusRunning)
	rec.CallerBridgeKey, rec.CallerBridgeLaunch = "k1", "k1-launch"
	owners := map[string]string{"k1": "old"}
	obs := NewDispatchObserver(func() []Record { return []Record{rec} }, nil,
		WithDispatchOwner(func(key, _ string) (string, bool) { name, ok := owners[key]; return name, ok }))
	store := observe.NewAttentionStore(nil)
	store.BindBridgeKey("k1", "k1-launch", "old")
	store.SetDispatchCounter(obs)
	store.Set("old", observe.AttentionWorking)
	if att := store.Set("old", observe.AttentionFinished); att.State != observe.AttentionWorking {
		t.Fatalf("finish with d1 running = %+v; want held working", att)
	}

	gen, counts := obs.DispatchSnapshot() // the state tick reads...
	owners["k1"] = "new"                  // ...the agent is renamed...
	store.Move("old", "new")
	store.ReconcileDispatches(gen, counts) // ...then the tick applies what it read.

	att, ok := store.Get("new")
	if !ok || att.State != observe.AttentionWorking || att.Outstanding == nil || att.Outstanding.Dispatches != 1 {
		t.Fatalf("new attention = %+v; want still held working with d1 outstanding", att)
	}
}

// A bridge key is reused when a deleted agent's name is taken again: the
// stopped agent's still-running dispatch must not hold its replacement.
func TestAStoppedAgentsDispatchDoesNotHoldItsReplacementOnTheReusedKey(t *testing.T) {
	rec := agentDispatch("d1", "alpha", StatusRunning)
	obs := NewDispatchObserver(func() []Record { return []Record{rec} }, nil, WithDispatchOwner(ownKey))
	store := observe.NewAttentionStore(nil)
	store.SetDispatchCounter(obs)
	store.BindBridgeKey("alpha", "alpha-launch", "alpha")
	store.UnregisterAgent("alpha") // stopped, then deleted
	store.Remove("alpha")

	store.BindBridgeKey("alpha", "alpha-launch2", "alpha") // a new alpha's launch reuses the key
	store.Set("alpha", observe.AttentionWorking)

	if att := store.Set("alpha", observe.AttentionFinished); att.State != observe.AttentionFinished || att.Outstanding != nil {
		t.Fatalf("replacement alpha = %+v; want finished, nothing of its predecessor's outstanding", att)
	}
}
