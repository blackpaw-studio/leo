package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agentstore"
)

// mailSupervisor is a fakeSupervisor that also keeps agents' undelivered
// messages, as service.Supervisor does, recording what it is asked.
type mailSupervisor struct {
	fakeSupervisor
	dropped []string
	moved   []string
	moveErr error
}

func (m *mailSupervisor) DropAgentMail(name string) { m.dropped = append(m.dropped, name) }

func (m *mailSupervisor) RenameAgentMail(oldName, newName string) error {
	m.moved = append(m.moved, oldName+">"+newName)
	return m.moveErr
}

// Deleting an agent drops what it never took (its senders are told).
func TestDeleteDropsTheAgentsUndeliveredMessages(t *testing.T) {
	home := t.TempDir()
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-x", Stopped: true, Workspace: home})
	sup := &mailSupervisor{fakeSupervisor: fakeSupervisor{ephemeral: map[string]ProcessState{}}}
	m := newTestManager(t, home, sup)
	if err := m.Delete(context.Background(), "leo-x", DeleteOptions{}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if strings.Join(sup.dropped, ",") != "leo-x" {
		t.Fatalf("dropped %q, want leo-x's messages", sup.dropped)
	}
}

// A stopped agent's undelivered messages follow its rename (a live one's
// move with the supervisor's rename); a move that fails leaves the agent
// unrenamed rather than parted from its messages.
func TestRenamingAStoppedAgentMovesItsUndeliveredMessages(t *testing.T) {
	home := t.TempDir()
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-a", Stopped: true, ClaudeArgs: []string{"--name", "leo-a"}})
	sup := &mailSupervisor{fakeSupervisor: fakeSupervisor{ephemeral: map[string]ProcessState{}}}
	m := newTestManager(t, home, sup)
	if _, err := m.Rename("leo-a", "leo-b"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if strings.Join(sup.moved, ",") != "leo-a>leo-b" {
		t.Fatalf("moved %q, want leo-a's messages to leo-b", sup.moved)
	}

	sup.moveErr = errors.New("disk full")
	if _, err := m.Rename("leo-b", "leo-c"); err == nil {
		t.Fatal("a rename whose messages could not move went through")
	}
	recs, _ := agentstore.Load(agentstore.FilePath(home))
	if _, ok := recs["leo-b"]; !ok {
		t.Fatal("the failed rename re-keyed the record")
	}
}

func TestRenamingALiveAgentLeavesItsMessagesToTheSupervisor(t *testing.T) {
	home := t.TempDir()
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-a", ClaudeArgs: []string{"--name", "leo-a"}})
	sup := &mailSupervisor{fakeSupervisor: fakeSupervisor{ephemeral: map[string]ProcessState{"leo-a": {Name: "leo-a", Status: "running"}}}}
	m := newTestManager(t, home, sup)
	if _, err := m.Rename("leo-a", "leo-b"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if len(sup.moved) != 0 {
		t.Fatalf("moved %q; a live agent's messages move with RenameAgent", sup.moved)
	}
}
