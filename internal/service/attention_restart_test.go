package service

import (
	"context"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/observe"
)

// daemonLife is one daemon lifetime over a shared leo home: its own
// supervisor and attention store, persisting under the home's state dir.
type daemonLife struct {
	launchFixture
}

// startDaemon boots a supervisor on home (the agents.json and attention file
// a previous life left behind) and spawns agent name, adopting its session.
func startDaemon(t *testing.T, home, name string) daemonLife {
	t.Helper()
	f := newLaunchFixture(t, &fakeAttentionDriver{supported: true}, agentstore.Record{})
	f.sv.homePath = home
	ctx, cancel := context.WithCancel(context.Background())
	store := observe.NewAttentionStore(nil)
	store.EnablePersistence(ctx, attentionFilePath(home), t.Logf)
	t.Cleanup(func() {
		cancel()
		store.WaitPersistence()
	})
	f.sv.SetAttention(store)
	f.store = store
	f.spawn(t, daemon.AgentSpawnSpec{Name: name, Adopt: true})
	waitTokenRouted(t, home, store, name)
	return daemonLife{f}
}

// restartDaemon ends the running daemon life (its session survives, so the
// stored token stays) after a final flush, as shutdown does.
func (d daemonLife) restartDaemon(t *testing.T, name string) {
	t.Helper()
	d.store.FlushPersistence()
	d.cancel()
	d.sv.Wait()
}

func seedAdoptable(t *testing.T, name, token string) string {
	t.Helper()
	home := t.TempDir()
	if err := agentstore.Save(home, agentstore.Record{Name: name}); err != nil {
		t.Fatal(err)
	}
	if err := agentstore.SetAttentionToken(home, name, token); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestAdoptAfterRestartRestoresAtRestAttention(t *testing.T) {
	home := seedAdoptable(t, "a", "tok-1")
	first := startDaemon(t, home, "a")
	waitAttention(t, first.store, "a", observe.AttentionUnknown)
	first.store.SetByToken("tok-1", observe.AttentionWorking)
	fin, ok := first.store.SetByTokenFinished("tok-1", "shipped it")
	if !ok {
		t.Fatal("finished hook not recorded")
	}
	first.restartDaemon(t, "a")

	second := startDaemon(t, home, "a")

	got := waitAttention(t, second.store, "a", observe.AttentionFinished)
	if got.Preview != "shipped it" || got.Revision <= fin.Revision {
		t.Fatalf("restored = %+v, want preview %q at revision > %d", got, "shipped it", fin.Revision)
	}
	if att, ok := second.store.SetByToken("tok-1", observe.AttentionWorking); !ok || att.State != observe.AttentionWorking || att.Revision <= got.Revision {
		t.Fatalf("UserPromptSubmit after restore = %+v, %v; want working above %d", att, ok, got.Revision)
	}
}

func TestRestartDoesNotRestoreWorking(t *testing.T) {
	home := seedAdoptable(t, "a", "tok-1")
	first := startDaemon(t, home, "a")
	waitAttention(t, first.store, "a", observe.AttentionUnknown)
	first.store.SetByToken("tok-1", observe.AttentionWorking)
	first.restartDaemon(t, "a")

	second := startDaemon(t, home, "a")

	waitAttention(t, second.store, "a", observe.AttentionUnknown)
}

func TestFreshLaunchDoesNotInheritAttention(t *testing.T) {
	for name, relaunch := range map[string]func(t *testing.T, home string){
		"new token": func(t *testing.T, home string) {
			if err := agentstore.SetAttentionToken(home, "a", "tok-2"); err != nil {
				t.Fatal(err)
			}
		},
		"recreated agent": func(t *testing.T, home string) {
			agentstore.Remove(home, "a")
			if err := agentstore.Save(home, agentstore.Record{Name: "a"}); err != nil {
				t.Fatal(err)
			}
			if err := agentstore.SetAttentionToken(home, "a", "tok-3"); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			home := seedAdoptable(t, "a", "tok-1")
			first := startDaemon(t, home, "a")
			waitAttention(t, first.store, "a", observe.AttentionUnknown)
			first.store.SetByTokenFinished("tok-1", "old turn")
			first.restartDaemon(t, "a")
			relaunch(t, home)

			second := startDaemon(t, home, "a")

			waitAttention(t, second.store, "a", observe.AttentionUnknown)
		})
	}
}

func TestRestoredAttentionSurvivesForNeedsInputAndErrored(t *testing.T) {
	for _, tc := range []struct {
		name  string
		apply func(*observe.AttentionStore)
		want  observe.AttentionState
	}{
		{"needs_input", func(s *observe.AttentionStore) {
			s.SetByTokenNeedsInput("tok-1", observe.AttentionReason{Kind: observe.AttentionReasonQuestion, Detail: "which db?"})
		}, observe.AttentionNeedsInput},
		{"errored", func(s *observe.AttentionStore) { s.Set("a", observe.AttentionErrored) }, observe.AttentionErrored},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := seedAdoptable(t, "a", "tok-1")
			first := startDaemon(t, home, "a")
			waitAttention(t, first.store, "a", observe.AttentionUnknown)
			tc.apply(first.store)
			before, _ := first.store.Get("a")
			first.restartDaemon(t, "a")

			second := startDaemon(t, home, "a")

			got := waitAttention(t, second.store, "a", tc.want)
			if got.Revision <= before.Revision || (got.Reason == nil) != (before.Reason == nil) {
				t.Fatalf("restored = %+v, before = %+v", got, before)
			}
		})
	}
}

// waitTokenRouted waits for the adopted launch's token to route to name, the
// point from which its hooks land.
func waitTokenRouted(t *testing.T, home string, store *observe.AttentionStore, name string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got, ok := store.AgentForToken(storedToken(t, home, name)); ok && got == name {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no token routes to %q", name)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
