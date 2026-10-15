package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
)

// goneRecorder is a gone hook that remembers who it was told about.
type goneRecorder struct{ names []string }

func (g *goneRecorder) hook(name string) { g.names = append(g.names, name) }

func goneManager(t *testing.T, sup *capturingSupervisor, names ...string) (*Manager, *goneRecorder) {
	t.Helper()
	home := t.TempDir()
	cfg := &config.Config{HomePath: home}
	for _, n := range names {
		_ = agentstore.Save(home, agentstore.Record{Name: n, Workspace: "/w", SessionID: "sid"})
	}
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	rec := &goneRecorder{}
	m.SetGoneHook(rec.hook)
	return m, rec
}

func TestOperatorStopReportsTheAgentGone(t *testing.T) {
	sup := &capturingSupervisor{agents: map[string]ProcessState{"leo-x": {Name: "leo-x", Status: "running"}}}
	m, rec := goneManager(t, sup, "leo-x")
	if err := m.Stop("leo-x", StopOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(rec.names) != 1 || rec.names[0] != "leo-x" {
		t.Fatalf("gone hook calls = %v, want [leo-x]", rec.names)
	}
}

// An idle sweep suspends an agent that a message wakes again: its asks stay.
func TestIdleStopDoesNotReportTheAgentGone(t *testing.T) {
	sup := &capturingSupervisor{agents: map[string]ProcessState{"leo-x": {Name: "leo-x", Status: "running"}}}
	m, rec := goneManager(t, sup, "leo-x")
	if err := m.Stop("leo-x", StopOptions{WakeOnMessage: true}); err != nil {
		t.Fatal(err)
	}
	if len(rec.names) != 0 {
		t.Fatalf("gone hook calls = %v, want none", rec.names)
	}
}

func TestFailedStopDoesNotReportTheAgentGone(t *testing.T) {
	sup := &capturingSupervisor{
		agents:  map[string]ProcessState{"leo-x": {Name: "leo-x", Status: "running"}},
		stopErr: errors.New("tmux kill failed"),
	}
	m, rec := goneManager(t, sup, "leo-x")
	if err := m.Stop("leo-x", StopOptions{}); err == nil {
		t.Fatal("expected the stop to fail")
	}
	if len(rec.names) != 0 {
		t.Fatalf("gone hook calls = %v, want none", rec.names)
	}
}

func TestDeleteReportsTheAgentGone(t *testing.T) {
	m, rec := goneManager(t, &capturingSupervisor{}, "leo-gone")
	if err := m.Delete(context.Background(), "leo-gone", DeleteOptions{}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(rec.names) != 1 || rec.names[0] != "leo-gone" {
		t.Fatalf("gone hook calls = %v, want [leo-gone]", rec.names)
	}
}
