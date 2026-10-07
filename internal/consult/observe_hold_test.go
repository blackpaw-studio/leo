package consult

import (
	"testing"

	"github.com/blackpaw-studio/leo/internal/observe"
)

// A tick's dispatch snapshot read before a rename and applied after it must
// not release the renamed agent's B-051 hold while its dispatch still runs.
func TestDispatchSnapshotAppliedAcrossARenameKeepsTheHold(t *testing.T) {
	rec := agentDispatch("d1", "old", StatusRunning)
	rec.CallerBridgeKey = "k1"
	owners := map[string]string{"k1": "old"}
	obs := NewDispatchObserver(func() []Record { return []Record{rec} }, nil,
		WithDispatchOwner(func(key string) (string, bool) { name, ok := owners[key]; return name, ok }))
	store := observe.NewAttentionStore(nil)
	store.BindBridgeKey("k1", "old")
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
