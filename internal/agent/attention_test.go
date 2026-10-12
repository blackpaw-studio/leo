package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/observe"
)

func TestDeleteRemovesAttention(t *testing.T) {
	home := t.TempDir()
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-gone", Workspace: "/w", Stopped: true})
	m := newTestManager(t, home, &fakeSupervisor{ephemeral: map[string]ProcessState{}})
	store := observe.NewAttentionStore(nil)
	store.Set("leo-gone", observe.AttentionUnknown)
	m.SetAttention(store)

	if err := m.Delete(context.Background(), "leo-gone", DeleteOptions{}); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if att, ok := store.Get("leo-gone"); ok {
		t.Fatalf("attention after Delete = %+v, want absent", att)
	}
}

func TestRenameMovesAttention(t *testing.T) {
	for _, tc := range []struct {
		name string
		live bool
	}{{"stopped", false}, {"running", true}} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			_ = agentstore.Save(home, agentstore.Record{Name: "leo-old", Workspace: "/w", Stopped: !tc.live, ClaudeArgs: []string{"--name", "leo-old"}})
			sup := &fakeSupervisor{ephemeral: map[string]ProcessState{}}
			if tc.live {
				sup.ephemeral["leo-old"] = ProcessState{Name: "leo-old", Status: "running"}
			}
			m := newTestManager(t, home, sup)
			store := observe.NewAttentionStore(nil)
			store.Set("leo-old", observe.AttentionFinished)
			m.SetAttention(store)

			if _, err := m.Rename("leo-old", "leo-new"); err != nil {
				t.Fatalf("Rename: %v", err)
			}

			if _, ok := store.Get("leo-old"); ok {
				t.Error("old name still has attention")
			}
			if att, ok := store.Get("leo-new"); !ok || att.State != observe.AttentionFinished || att.Revision != 2 {
				t.Errorf("new name attention = %+v, %v", att, ok)
			}
		})
	}
}

func TestDeleteRemovesSettingsSpillFile(t *testing.T) {
	home := t.TempDir()
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-gone", Workspace: "/w", Stopped: true})
	spill := filepath.Join(home, "state", "settings", "leo-gone.json")
	other := filepath.Join(home, "state", "settings", "leo-other.json")
	for _, p := range []string{spill, other} {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(`{"env":{"K":"secret"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m := newTestManager(t, home, &fakeSupervisor{ephemeral: map[string]ProcessState{}})

	if err := m.Delete(context.Background(), "leo-gone", DeleteOptions{}); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := os.Stat(spill); !os.IsNotExist(err) {
		t.Fatalf("spill file after Delete: err=%v, want removed", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("another agent's spill file removed: %v", err)
	}
}

// A live agent's claude was launched with --settings <old>.json and may
// re-read it, so only a non-live rename deletes the old spill; the startup
// sweep (SweepSettingsSpills) cleans the live leftover later.
func TestRenameRemovesOldSettingsSpillOnlyWhenNotLive(t *testing.T) {
	for _, live := range []bool{false, true} {
		home := t.TempDir()
		_ = agentstore.Save(home, agentstore.Record{Name: "leo-old", Workspace: "/w", Stopped: !live, ClaudeArgs: []string{"--name", "leo-old"}})
		old := writeAgentSpill(t, home, "leo-old")
		sup := &fakeSupervisor{ephemeral: map[string]ProcessState{}}
		if live {
			sup.ephemeral["leo-old"] = ProcessState{Name: "leo-old", Status: "running"}
		}
		m := newTestManager(t, home, sup)

		if _, err := m.Rename("leo-old", "leo-new"); err != nil {
			t.Fatalf("Rename (live=%v): %v", live, err)
		}

		_, err := os.Stat(old)
		if live && err != nil {
			t.Fatalf("live rename removed the running agent's settings file: %v", err)
		}
		if !live && !os.IsNotExist(err) {
			t.Fatalf("non-live rename kept the old spill file: err=%v", err)
		}
	}
}

func TestSweepSettingsSpillsRemovesOrphanAgentFiles(t *testing.T) {
	home := t.TempDir()
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-known"})
	known := writeAgentSpill(t, home, "leo-known")
	orphan := writeAgentSpill(t, home, "leo-renamed-away")
	dispatch := writeAgentSpill(t, home, "dispatch-d-123") // the dispatcher's own sweep owns these

	SweepSettingsSpills(home)

	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphan agent spill kept: err=%v", err)
	}
	for _, p := range []string{known, dispatch} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s removed: %v", p, err)
		}
	}
}

func writeAgentSpill(t *testing.T, home, name string) string {
	t.Helper()
	path := filepath.Join(home, "state", "settings", name+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"env":{"K":"secret"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
