package agent

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
)

func movedTestManager(t *testing.T) (*Manager, *capturingSupervisor, string) {
	t.Helper()
	home := t.TempDir()
	cfg := &config.Config{HomePath: home, Templates: map[string]config.TemplateConfig{"coding": {}, "other": {}}}
	sup := &capturingSupervisor{}
	at := time.Date(2026, 10, 27, 9, 30, 0, 0, time.UTC)
	_ = agentstore.Save(home, agentstore.Record{
		Name: "leo-x", Template: "coding", Workspace: t.TempDir(), SessionID: "sid",
		Stopped: true, MovedTo: "laptop", MovedAt: &at,
	})
	return New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok"), sup, home
}

// TestMovedAgentRefusesLifecycle: every verb that could launch, rewire or
// re-key the agent refuses a record moved to another host, and none of them
// reaches the supervisor.
func TestMovedAgentRefusesLifecycle(t *testing.T) {
	verbs := map[string]func(m *Manager) error{
		"Start":   func(m *Manager) error { return m.Start("leo-x") },
		"Restart": func(m *Manager) error { return m.Restart("leo-x") },
		"Reset":   func(m *Manager) error { return m.Reset("leo-x") },
		"SwitchTemplate": func(m *Manager) error {
			_, err := m.SwitchTemplate("leo-x", "other")
			return err
		},
		"SetEnvironments": func(m *Manager) error {
			_, err := m.SetEnvironments("leo-x", []string{"work"})
			return err
		},
		"Rename": func(m *Manager) error {
			_, err := m.Rename("leo-x", "y")
			return err
		},
	}
	for verb, call := range verbs {
		t.Run(verb, func(t *testing.T) {
			m, sup, home := movedTestManager(t)
			err := call(m)
			if !errors.Is(err, ErrAgentMoved) {
				t.Fatalf("%s on a moved agent = %v, want ErrAgentMoved", verb, err)
			}
			var moved *MovedError
			if !errors.As(err, &moved) || moved.To != "laptop" {
				t.Fatalf("%s error = %#v, want *MovedError naming laptop", verb, err)
			}
			if !strings.Contains(err.Error(), "leo --host laptop agent attach leo-x") {
				t.Errorf("%s error %q does not point at the destination", verb, err)
			}
			if sup.spawnCall != nil || len(sup.stopCalls) != 0 {
				t.Errorf("%s reached the supervisor: spawn=%v stops=%v", verb, sup.spawnCall, sup.stopCalls)
			}
			recs, _ := agentstore.Load(agentstore.FilePath(home))
			if got := recs["leo-x"]; got.MovedTo != "laptop" || !got.Stopped || got.Template != "coding" {
				t.Errorf("%s changed the moved record: %+v", verb, got)
			}
		})
	}
}

func TestStartRefusedWhileMoveLockHeld(t *testing.T) {
	m, sup, home := movedTestManager(t)
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-y", Workspace: "/w", Stopped: true})

	release, err := m.BeginMove("leo-y", MoveModeMove)
	if err != nil {
		t.Fatalf("BeginMove: %v", err)
	}
	if err := m.Start("leo-y"); !errors.Is(err, ErrAgentMoving) {
		t.Fatalf("Start while moving = %v, want ErrAgentMoving", err)
	}
	if err := m.Reset("leo-y"); !errors.Is(err, ErrAgentMoving) {
		t.Fatalf("Reset while moving = %v, want ErrAgentMoving", err)
	}
	if _, err := m.BeginMove("leo-y", MoveModeCopy); !errors.Is(err, ErrAgentMoving) {
		t.Fatalf("second BeginMove = %v, want ErrAgentMoving", err)
	}
	if sup.spawnCall != nil {
		t.Fatal("a launch reached the supervisor while the move lock was held")
	}
	release()
	release() // idempotent
	if err := m.Start("leo-y"); err != nil {
		t.Fatalf("Start after release: %v", err)
	}
}

func TestCopyLockDoesNotBlockStart(t *testing.T) {
	m, sup, home := movedTestManager(t)
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-y", Workspace: "/w", Stopped: true})

	release, err := m.BeginMove("leo-y", MoveModeCopy)
	if err != nil {
		t.Fatalf("BeginMove: %v", err)
	}
	defer release()
	if err := m.Start("leo-y"); err != nil {
		t.Fatalf("Start under a copy lock: %v", err)
	}
	if sup.spawnCall == nil {
		t.Fatal("Start under a copy lock did not spawn")
	}
	if _, err := m.BeginMove("leo-y", MoveModeMove); !errors.Is(err, ErrAgentMoving) {
		t.Fatalf("a move during a copy = %v, want ErrAgentMoving", err)
	}
}

func TestWakeableFalseForMoved(t *testing.T) {
	m, _, home := movedTestManager(t)
	// Even a hand-edited record that kept WakeOnMessage must not wake.
	_ = agentstore.Update(home, "leo-x", func(r agentstore.Record) agentstore.Record {
		r.WakeOnMessage = true
		return r
	})
	if m.Wakeable("leo-x") {
		t.Fatal("Wakeable = true for a moved agent")
	}
	if _, ok := m.Moved("leo-x"); !ok {
		t.Fatal("Moved = false for a moved agent")
	}
}

func TestMarkMovedAndListShowsMovedTo(t *testing.T) {
	m, _, home := movedTestManager(t)
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-y", Workspace: "/w", Stopped: true, WakeOnMessage: true})
	at := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)

	if err := m.MarkMoved("leo-y", "laptop", "laptop.local", at); err != nil {
		t.Fatalf("MarkMoved: %v", err)
	}
	recs, _ := agentstore.Load(agentstore.FilePath(home))
	got := recs["leo-y"]
	if got.MovedTo != "laptop" || !got.Stopped || got.WakeOnMessage || got.MovedAt == nil || !got.MovedAt.Equal(at) {
		t.Fatalf("marked record = %+v", got)
	}
	if !got.MoveLog["laptop.local"].Equal(at) {
		t.Fatalf("MoveLog = %v, want laptop.local=%v", got.MoveLog, at)
	}
	for _, r := range m.List() {
		if r.Name == "leo-y" && (r.MovedTo != "laptop" || r.Status != "stopped") {
			t.Fatalf("List row = %+v, want stopped moved_to laptop", r)
		}
	}
}
