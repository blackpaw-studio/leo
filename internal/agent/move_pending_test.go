package agent

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
)

var fenceSince = time.Date(2026, 10, 27, 11, 0, 0, 0, time.UTC)

// fencedTestManager has leo-x stopped with a move to laptop pending, as a
// source holds it after a commit whose outcome is unknown.
func fencedTestManager(t *testing.T) (*Manager, *capturingSupervisor, string) {
	t.Helper()
	home := t.TempDir()
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-x", Template: "coding", Workspace: t.TempDir(), SessionID: "sid", Stopped: true})
	m, sup := fencedManagerAt(home)
	if err := m.FenceMove("leo-x", agentstore.MovePending{To: "laptop", Peer: "laptop.local", RunID: "run-1", Since: fenceSince}); err != nil {
		t.Fatalf("FenceMove: %v", err)
	}
	return m, sup, home
}

func fencedManagerAt(home string) (*Manager, *capturingSupervisor) {
	cfg := &config.Config{HomePath: home, Templates: map[string]config.TemplateConfig{"coding": {}, "other": {}}}
	sup := &capturingSupervisor{}
	return New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok"), sup
}

// Every verb that could launch, rewire or re-key the agent refuses while a
// move of it is pending, naming the host and how to settle it.
func TestMovePendingRefusesLifecycle(t *testing.T) {
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
		"MovedErr": func(m *Manager) error { return m.MovedErr("leo-x") },
	}
	for verb, call := range verbs {
		t.Run(verb, func(t *testing.T) {
			m, sup, home := fencedTestManager(t)
			err := call(m)
			if !errors.Is(err, ErrMovePending) {
				t.Fatalf("%s on a fenced agent = %v, want ErrMovePending", verb, err)
			}
			for _, want := range []string{"laptop", "leo --host laptop agent list", "leo agent move leo-x --reconcile", "--abandon-remote"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("%s error %q does not mention %q", verb, err, want)
				}
			}
			if sup.spawnCall != nil || len(sup.stopCalls) != 0 {
				t.Errorf("%s reached the supervisor: spawn=%v stops=%v", verb, sup.spawnCall, sup.stopCalls)
			}
			recs, _ := agentstore.Load(agentstore.FilePath(home))
			if got := recs["leo-x"]; got.MovePending == nil || got.MovePending.RunID != "run-1" {
				t.Errorf("%s changed the fence: %+v", verb, got.MovePending)
			}
		})
	}
}

// The fence is on disk: a restarted daemon's manager still refuses, and an
// inbound message never wakes the agent.
func TestMovePendingSurvivesRestart(t *testing.T) {
	_, _, home := fencedTestManager(t)
	_ = agentstore.Update(home, "leo-x", func(r agentstore.Record) agentstore.Record {
		r.WakeOnMessage = true
		return r
	})
	m, sup := fencedManagerAt(home)
	if err := m.Start("leo-x"); !errors.Is(err, ErrMovePending) {
		t.Fatalf("Start after a restart = %v, want ErrMovePending", err)
	}
	if m.Wakeable("leo-x") {
		t.Fatal("Wakeable = true for a fenced agent")
	}
	if sup.spawnCall != nil {
		t.Fatal("a launch reached the supervisor")
	}
}

// Only the fence's own run lifts it; MarkMoved settles it as moved.
func TestMoveFenceLifts(t *testing.T) {
	m, _, _ := fencedTestManager(t)
	if err := m.ClearMoveFence("leo-x", "run-0"); err != nil {
		t.Fatalf("ClearMoveFence(another run): %v", err)
	}
	if err := m.Start("leo-x"); !errors.Is(err, ErrMovePending) {
		t.Fatalf("another run lifted the fence: Start = %v", err)
	}
	if err := m.ClearMoveFence("leo-x", "run-1"); err != nil {
		t.Fatalf("ClearMoveFence: %v", err)
	}
	if err := m.Start("leo-x"); err != nil {
		t.Fatalf("Start after the fence lifted: %v", err)
	}

	m, _, home := fencedTestManager(t)
	if err := m.MarkMoved("leo-x", "laptop", "laptop.local", fenceSince); err != nil {
		t.Fatalf("MarkMoved: %v", err)
	}
	recs, _ := agentstore.Load(agentstore.FilePath(home))
	if got := recs["leo-x"]; got.MovePending != nil || got.MovedTo != "laptop" {
		t.Fatalf("marked record = %+v, want moved with no fence", got)
	}
}
