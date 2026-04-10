package service

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/daemon"
)

// RestoreAgents restores ephemeral agents from a previous daemon run.
// It checks if the tmux session is still alive and re-spawns those that are.
// Dead sessions are cleaned up from agents.json.
func RestoreAgents(homePath, tmuxPath string, sv *Supervisor) int {
	path := agentstore.FilePath(homePath)
	records, err := agentstore.Load(path)
	if err != nil || len(records) == 0 {
		return 0
	}

	restored := 0

	for name, rec := range records {
		sessionName := fmt.Sprintf("leo-%s", name)
		check := exec.Command(tmuxPath, "has-session", "-t", sessionName)
		if check.Run() != nil {
			// Session is dead — clean up
			agentstore.Remove(homePath, name)
			continue
		}

		// Session is alive — register it with the supervisor
		spec := daemon.AgentSpawnSpec{
			Name:       rec.Name,
			ClaudeArgs: rec.ClaudeArgs,
			WorkDir:    rec.Workspace,
			Env:        rec.Env,
			WebPort:    rec.WebPort,
		}
		if err := sv.SpawnAgent(spec); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to restore agent %q: %v\n", name, err)
			agentstore.Remove(homePath, name)
			continue
		}
		restored++
	}

	return restored
}
