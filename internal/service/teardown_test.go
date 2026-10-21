package service

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

// An explicit permanent stop (`leo service stop`) tears down every agent's
// session by exact name; that is the only whole-fleet kill there is.
func TestTeardownSessionsKillsEachAgentSessionByExactName(t *testing.T) {
	tmuxPath, logPath := writePrimaryPaneTmux(t)
	stubResolve(t, tmux.Resolution{Client: tmuxPath})
	home := t.TempDir()
	for _, name := range []string{"alpha", "beta"} {
		if err := agentstore.Save(home, agentstore.Record{Name: name}); err != nil {
			t.Fatal(err)
		}
	}

	var warn bytes.Buffer
	if err := TeardownSessions(home, "", &warn); err != nil {
		t.Fatalf("TeardownSessions: %v", err)
	}

	out, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, want := range []string{
		"[-L][leo][kill-session][-t][=leo-alpha]",
		"[-L][leo][kill-session][-t][=leo-beta]",
	} {
		if !containsTmuxArgv(lines, want) {
			t.Errorf("missing %s; tmux argv:\n%s", want, out)
		}
	}
	if got := strings.Count(string(out), "kill-session"); got != 2 {
		t.Errorf("kill-session calls = %d, want 2; tmux argv:\n%s", got, out)
	}
}

func TestTeardownSessionsWithNoAgentsKillsNothing(t *testing.T) {
	tmuxPath, logPath := writePrimaryPaneTmux(t)
	stubResolve(t, tmux.Resolution{Client: tmuxPath})

	if err := TeardownSessions(t.TempDir(), "", &bytes.Buffer{}); err != nil {
		t.Fatalf("TeardownSessions: %v", err)
	}
	if out, _ := os.ReadFile(logPath); strings.Contains(string(out), "kill-session") {
		t.Fatalf("killed with no agents; tmux argv:\n%s", out)
	}
}
