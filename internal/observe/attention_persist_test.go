package observe

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// restartedStore simulates a daemon restart: a fresh store reading what the
// previous one flushed to path.
func restartedStore(t *testing.T, path string) *AttentionStore {
	t.Helper()
	s := NewAttentionStore(nil)
	enablePersistence(t, s, path, t.Logf)
	return s
}

// enablePersistence starts s's writer and stops it, final flush included,
// before the test's temp dir goes.
func enablePersistence(t *testing.T, s *AttentionStore, path string, logf func(string, ...any)) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s.EnablePersistence(ctx, path, logf)
	t.Cleanup(func() {
		cancel()
		s.WaitPersistence()
	})
}

func persistingStore(t *testing.T, path string) *AttentionStore {
	t.Helper()
	return restartedStore(t, path)
}

func TestRestoreReturnsAtRestAttentionAboveEveryRevisionServedBefore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "attention.json")
	s := persistingStore(t, path)
	s.RegisterToken("tok", "a")
	s.Set("a", AttentionWorking)
	fin, _ := s.SetByTokenFinished("tok", "all done")
	// A later working transition on another agent pushed revisions higher.
	for i := 0; i < 20; i++ {
		s.Set("b", AttentionWorking)
	}
	s.FlushPersistence()

	r := restartedStore(t, path)
	if !r.Restore("a", "tok") {
		t.Fatal("Restore(a, tok) = false, want true")
	}
	got, ok := r.Get("a")
	if !ok || got.State != AttentionFinished || got.Preview != "all done" {
		t.Fatalf("restored = %+v, %v; want finished with preview", got, ok)
	}
	if got.Revision <= fin.Revision || got.Revision <= 21 {
		t.Fatalf("restored revision %d not above anything served before (finished at %d, max 21)", got.Revision, fin.Revision)
	}
	if next := r.Set("fresh", AttentionWorking); next.Revision <= 21 {
		t.Fatalf("first revision of another agent after restart = %d, want > 21", next.Revision)
	}
}

func TestRestoreRoundTripsNeedsInputAndErrored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "attention.json")
	s := persistingStore(t, path)
	s.RegisterToken("t1", "asking")
	s.RegisterToken("t2", "broken")
	s.SetByTokenNeedsInput("t1", AttentionReason{Kind: AttentionReasonPermission, Tool: "Bash", Detail: "rm -rf x"})
	s.Set("broken", AttentionErrored)
	s.FlushPersistence()

	r := restartedStore(t, path)
	if !r.Restore("asking", "t1") || !r.Restore("broken", "t2") {
		t.Fatal("Restore returned false for a persisted agent")
	}
	asking, _ := r.Get("asking")
	if asking.State != AttentionNeedsInput || asking.Reason == nil || *asking.Reason != (AttentionReason{Kind: AttentionReasonPermission, Tool: "Bash", Detail: "rm -rf x"}) {
		t.Fatalf("asking = %+v, reason %+v", asking, asking.Reason)
	}
	if broken, _ := r.Get("broken"); broken.State != AttentionErrored {
		t.Fatalf("broken = %+v, want errored", broken)
	}
}

func TestRestoreNeverRestoresWorkingOrUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "attention.json")
	s := persistingStore(t, path)
	s.RegisterToken("w", "working")
	s.RegisterToken("u", "unknown")
	s.Set("working", AttentionWorking)
	s.Set("unknown", AttentionUnknown)
	s.FlushPersistence()

	r := restartedStore(t, path)
	if r.Restore("working", "w") || r.Restore("unknown", "u") {
		t.Fatal("Restore returned true for a non-at-rest state")
	}
	if _, ok := r.Get("working"); ok {
		t.Fatal("working agent has attention after restore, want untracked")
	}
}

func TestRestoreRequiresMatchingToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "attention.json")
	s := persistingStore(t, path)
	s.RegisterToken("old", "a")
	s.Set("a", AttentionFinished)
	s.FlushPersistence()

	for name, tc := range map[string]struct{ agent, token string }{
		"new token":         {"a", "new"},
		"empty token":       {"a", ""},
		"recreated (other)": {"other", "old"},
	} {
		t.Run(name, func(t *testing.T) {
			r := restartedStore(t, path)
			if r.Restore(tc.agent, tc.token) {
				t.Fatal("Restore = true, want false")
			}
			if _, ok := r.Get(tc.agent); ok {
				t.Fatal("agent has attention, want untracked")
			}
		})
	}
}

func TestRestoredRecordSurvivesFlushBeforeItsAgentIsAdopted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "attention.json")
	s := persistingStore(t, path)
	s.RegisterToken("ta", "a")
	s.RegisterToken("tb", "b")
	s.Set("a", AttentionFinished)
	s.Set("b", AttentionErrored)
	s.FlushPersistence()

	r := restartedStore(t, path)
	r.Restore("a", "ta")
	r.Set("c", AttentionWorking) // unrelated transition flushes while b is not adopted yet
	r.FlushPersistence()

	r2 := restartedStore(t, path)
	if !r2.Restore("b", "tb") {
		t.Fatal("b's pending record was lost by an earlier flush")
	}
}

func TestRemovePrunesPersistedRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "attention.json")
	s := persistingStore(t, path)
	s.RegisterToken("tok", "gone")
	s.Set("gone", AttentionFinished)
	s.FlushPersistence()
	s.Remove("gone")
	s.FlushPersistence()

	if data, _ := os.ReadFile(path); strings.Contains(string(data), "gone") {
		t.Fatalf("file still names the deleted agent:\n%s", data)
	}
	if restartedStore(t, path).Restore("gone", "tok") {
		t.Fatal("deleted agent restored")
	}
}

func TestMovedAgentRestoresUnderItsNewName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "attention.json")
	s := persistingStore(t, path)
	s.RegisterToken("tok", "old")
	s.Set("old", AttentionFinished)
	s.Move("old", "new")
	s.FlushPersistence()

	r := restartedStore(t, path)
	if r.Restore("old", "tok") || !r.Restore("new", "tok") {
		t.Fatal("renamed agent must restore under its new name only")
	}
}

func TestCorruptOrMissingFileStartsEmpty(t *testing.T) {
	dir := t.TempDir()
	corrupt := filepath.Join(dir, "attention.json")
	if err := os.WriteFile(corrupt, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{"corrupt": corrupt, "missing": filepath.Join(dir, "none.json")} {
		t.Run(name, func(t *testing.T) {
			var logged []string
			s := NewAttentionStore(nil)
			var mu sync.Mutex
			enablePersistence(t, s, path, func(f string, a ...any) { mu.Lock(); logged = append(logged, f); mu.Unlock() })
			mu.Lock()
			defer mu.Unlock()
			if s.Restore("a", "tok") {
				t.Fatal("restored from an unusable file")
			}
			if got := s.Set("a", AttentionWorking); got.Revision == 0 {
				t.Fatal("store unusable after bad file")
			}
			if name == "corrupt" && len(logged) == 0 {
				t.Fatal("corrupt file not logged")
			}
		})
	}
}

func TestRestorePublishesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "attention.json")
	s := persistingStore(t, path)
	s.RegisterToken("tok", "a")
	s.Set("a", AttentionFinished)
	s.FlushPersistence()

	bus := NewBus()
	events, unsub, _ := bus.Subscribe(4)
	defer unsub()
	r := NewAttentionStore(bus)
	enablePersistence(t, r, path, t.Logf)
	r.Restore("a", "tok")

	select {
	case ev := <-events:
		t.Fatalf("Restore published %+v; a restart must not re-announce (or re-push) a finished turn", ev)
	default:
	}
}

func TestRestoreKeepsTheFailedTurnSummaryAndTheTimeOfTheChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "attention.json")
	at := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	s := persistingStore(t, path)
	s.SetClock(func() time.Time { return at })
	s.RegisterToken("tok", "a")
	s.SetByTokenErrored("tok", "ProviderAuthError")
	s.FlushPersistence()

	r := restartedStore(t, path)
	r.SetClock(func() time.Time { return at.Add(time.Hour) })
	if !r.Restore("a", "tok") {
		t.Fatal("Restore = false")
	}
	got, _ := r.Get("a")
	if got.State != AttentionErrored || got.Preview != "ProviderAuthError" || !got.At.Equal(at) {
		t.Fatalf("restored = %+v, want errored/ProviderAuthError at the original %v", got, at)
	}
}
