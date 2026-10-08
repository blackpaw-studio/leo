package daemon

import (
	"context"
	"strings"
	"testing"
)

func TestRegisterAttachPlacementReachesTheDaemonRegistry(t *testing.T) {
	workDir := tmpWorkDir(t)
	s := startServerAt(t, workDir)

	if err := RegisterAttachPlacement(context.Background(), workDir, "leo-agent-foo", 4242, "background"); err != nil {
		t.Fatalf("RegisterAttachPlacement: %v", err)
	}
	if got := s.AttachPlacements().Len(); got != 1 {
		t.Fatalf("registry len=%d, want 1", got)
	}
}

func TestRegisterAttachPlacementRejectsInvalidInput(t *testing.T) {
	workDir := tmpWorkDir(t)
	s := startServerAt(t, workDir)

	for name, call := range map[string]func() error{
		"placement": func() error { return RegisterAttachPlacement(context.Background(), workDir, "leo-a", 1, "sideways") },
		"session":   func() error { return RegisterAttachPlacement(context.Background(), workDir, "", 1, "pane") },
		"pid":       func() error { return RegisterAttachPlacement(context.Background(), workDir, "leo-a", 0, "pane") },
	} {
		err := call()
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: err=%v, want a rejection naming the field", name, err)
		}
	}
	if got := s.AttachPlacements().Len(); got != 0 {
		t.Fatalf("registry len=%d, want 0", got)
	}
}

func TestRegisterAttachPlacementFailsWithoutADaemon(t *testing.T) {
	if err := RegisterAttachPlacement(context.Background(), tmpWorkDir(t), "leo-a", 1, "pane"); err == nil {
		t.Fatal("want a connection error")
	}
}
