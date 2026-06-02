package service

import (
	"context"
	"testing"
)

func newTestSupervisor(t *testing.T, name string) *Supervisor {
	t.Helper()
	s := NewSupervisor(context.Background())
	s.states[name] = &ProcessState{Name: name, Status: "running", Ephemeral: true}
	s.cancels[name] = func() {}
	s.identities[name] = newProcIdentity(name, []string{"--name", name})
	return s
}

func TestRenameAgent_ReKeysMaps(t *testing.T) {
	origRename := tmuxRenameSession
	tmuxRenameSession = func(tmuxPath, old, new string) error { return nil }
	defer func() { tmuxRenameSession = origRename }()

	s := newTestSupervisor(t, "leo-old")
	if err := s.RenameAgent("leo-old", "leo-new"); err != nil {
		t.Fatalf("RenameAgent: %v", err)
	}

	if _, ok := s.states["leo-old"]; ok {
		t.Fatal("old state key still present")
	}
	st, ok := s.states["leo-new"]
	if !ok || st.Name != "leo-new" {
		t.Fatalf("new state missing/mislabeled: %+v", st)
	}
	if _, ok := s.cancels["leo-new"]; !ok {
		t.Fatal("cancel not re-keyed")
	}
	id, ok := s.identities["leo-new"]
	if !ok || id.Name() != "leo-new" || id.Args()[1] != "leo-new" {
		t.Fatalf("identity not re-keyed/rewritten: %+v", id)
	}
}

func TestRenameAgent_Rejections(t *testing.T) {
	origRename := tmuxRenameSession
	tmuxRenameSession = func(tmuxPath, old, new string) error { return nil }
	defer func() { tmuxRenameSession = origRename }()

	s := newTestSupervisor(t, "leo-old")
	s.states["leo-taken"] = &ProcessState{Name: "leo-taken", Status: "running", Ephemeral: true}
	if err := s.RenameAgent("leo-old", "leo-taken"); err == nil {
		t.Fatal("expected collision error")
	}

	s2 := newTestSupervisor(t, "leo-x")
	s2.states["leo-x"].Status = "restarting"
	if err := s2.RenameAgent("leo-x", "leo-y"); err == nil {
		t.Fatal("expected non-running rejection")
	}

	s3 := NewSupervisor(context.Background())
	s3.states["proc"] = &ProcessState{Name: "proc", Status: "running"}
	if err := s3.RenameAgent("proc", "leo-z"); err == nil {
		t.Fatal("expected non-ephemeral rejection")
	}

	s4 := NewSupervisor(context.Background())
	if err := s4.RenameAgent("leo-missing", "leo-q"); err == nil {
		t.Fatal("expected missing-source rejection")
	}

	s5 := newTestSupervisor(t, "leo-r")
	s5.reservations["leo-taken2"] = struct{}{}
	if err := s5.RenameAgent("leo-r", "leo-taken2"); err == nil {
		t.Fatal("expected reserved-name rejection")
	}

	s6 := NewSupervisor(context.Background())
	s6.states["leo-noid"] = &ProcessState{Name: "leo-noid", Status: "running", Ephemeral: true}
	// no entry in s6.identities
	if err := s6.RenameAgent("leo-noid", "leo-ok"); err == nil {
		t.Fatal("expected missing-identity rejection")
	}
}

func TestRenameAgent_TmuxFailureLeavesStateUntouched(t *testing.T) {
	origRename := tmuxRenameSession
	tmuxRenameSession = func(tmuxPath, old, new string) error { return context.DeadlineExceeded }
	defer func() { tmuxRenameSession = origRename }()

	s := newTestSupervisor(t, "leo-old")
	if err := s.RenameAgent("leo-old", "leo-new"); err == nil {
		t.Fatal("expected tmux rename failure to propagate")
	}
	if _, ok := s.states["leo-old"]; !ok {
		t.Fatal("old state was removed despite tmux failure")
	}
	if _, ok := s.states["leo-new"]; ok {
		t.Fatal("new state created despite tmux failure")
	}
}
