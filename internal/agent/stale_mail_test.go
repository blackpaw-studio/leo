package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/git"
	"github.com/blackpaw-studio/leo/internal/outbox"
)

// staleMailSupervisor is a capturingSupervisor whose outbox holds messages
// under the names in stale, as a deleted agent's whose drop failed.
type staleMailSupervisor struct {
	capturingSupervisor
	stale   func(name string) bool
	checked []string
}

func (s *staleMailSupervisor) CheckNoAgentMail(name string) error {
	s.checked = append(s.checked, name)
	if s.stale(name) {
		return fmt.Errorf("%s: %w", name, outbox.ErrNameHasMessages)
	}
	return nil
}

// A brand-new agent is never spawned under a name whose outbox still holds
// a former holder's messages: its first launch would carry them to it. As
// for a rename onto such a name, the spawn is refused, before anything is
// created, and the name's reservation is given back.
func TestSpawningOverAStaleOutboxIsRefused(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{
		HomePath:  home,
		Defaults:  config.DefaultsConfig{Model: "sonnet"},
		Templates: map[string]config.TemplateConfig{"assistant": {Workspace: home}},
	}
	sup := &staleMailSupervisor{stale: func(name string) bool { return name == "assistant" }}
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")

	_, err := m.Spawn(context.Background(), SpawnSpec{Template: "assistant"})

	if !errors.Is(err, outbox.ErrNameHasMessages) {
		t.Fatalf("Spawn = %v, want outbox.ErrNameHasMessages", err)
	}
	if sup.spawnCall != nil {
		t.Fatalf("spawned %+v over a stale outbox", *sup.spawnCall)
	}
	if recs, _ := agentstore.Load(agentstore.FilePath(home)); len(recs) != 0 {
		t.Fatalf("saved %d records for a refused spawn", len(recs))
	}
	if len(sup.releaseCalls) != 1 || sup.releaseCalls[0] != "assistant" {
		t.Fatalf("released %v, want the reservation of assistant back", sup.releaseCalls)
	}
}

// So is a worktree agent, before its worktree is made.
func TestSpawningAWorktreeAgentOverAStaleOutboxIsRefused(t *testing.T) {
	mgr, _, _ := newWorktreeTestManager(t, "leo")
	sup := &staleMailSupervisor{stale: func(string) bool { return true }}
	mgr.sup = sup
	fake := installFakeGit(t, map[string]git.BranchStatus{})

	_, err := mgr.Spawn(context.Background(), SpawnSpec{Template: "coding", Repo: "blackpaw-studio/leo", Branch: "feat/stale"})

	if !errors.Is(err, outbox.ErrNameHasMessages) {
		t.Fatalf("Spawn = %v, want outbox.ErrNameHasMessages", err)
	}
	if sup.spawnCall != nil {
		t.Fatalf("spawned %+v over a stale outbox", *sup.spawnCall)
	}
	for _, c := range fake.calls {
		if len(c.args) >= 2 && c.args[0] == "worktree" && c.args[1] == "add" {
			t.Fatalf("made a worktree for a refused spawn: %v", c.args)
		}
	}
	if len(sup.checked) != 1 || len(sup.releaseCalls) != 1 || sup.releaseCalls[0] != sup.checked[0] {
		t.Fatalf("checked %v and released %v, want the refused name's reservation given back", sup.checked, sup.releaseCalls)
	}
	if len(sup.reservations) != 0 {
		t.Fatalf("reservations %v outlive a refused spawn", sup.reservations)
	}
}
