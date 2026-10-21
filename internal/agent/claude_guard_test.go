package agent

import (
	"errors"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
)

// claudeGuardSupervisor is a supervisor that knows claude is not installed.
type claudeGuardSupervisor struct {
	capturingSupervisor
	missing bool
}

func (s *claudeGuardSupervisor) ClaudeAvailable(harnessName string) error {
	if s.missing && (harnessName == "" || harnessName == "claude") {
		return ErrClaudeNotFound
	}
	return nil
}

// Replacing a live claude agent must check that claude resolves first:
// stopping it and then failing to relaunch would turn a recoverable
// "install claude" error into a lost session.
func TestRelaunchOfLiveClaudeAgentChecksClaudeBeforeStopping(t *testing.T) {
	relaunches := map[string]func(*Manager, string) error{
		"Restart": func(m *Manager, name string) error { return m.Restart(name) },
		"Reset":   func(m *Manager, name string) error { return m.Reset(name) },
	}
	for label, relaunch := range relaunches {
		t.Run(label, func(t *testing.T) {
			home := t.TempDir()
			cfg := &config.Config{HomePath: home}
			sup := &claudeGuardSupervisor{missing: true}
			sup.agents = map[string]ProcessState{"leo-x": {Name: "leo-x", Status: "running"}}
			_ = agentstore.Save(home, agentstore.Record{
				Name: "leo-x", Workspace: "/w", SessionID: "sid",
				ClaudeArgs: []string{"--model", "sonnet", "--session-id", "sid"},
			})
			m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")

			err := relaunch(m, "leo-x")

			if !errors.Is(err, ErrClaudeNotFound) {
				t.Fatalf("%s error = %v, want ErrClaudeNotFound", label, err)
			}
			if len(sup.callOrder) != 0 {
				t.Errorf("the live agent was touched before the claude check: %v", sup.callOrder)
			}
			if recs, _ := agentstore.Load(agentstore.FilePath(home)); recs["leo-x"].SessionID != "sid" {
				t.Errorf("record changed: %+v", recs["leo-x"])
			}
		})
	}
}
