package chat

import (
	"path/filepath"

	"github.com/blackpaw-studio/leo/internal/config"
)

// AgentRef is what owner resolution needs to know about an agent.
type AgentRef struct {
	Name      string
	Workspace string
}

// Owner is a task's resolved thread. Missing names a configured owner that
// is not an agent, for a system note in the fallback thread.
type Owner struct {
	Agent   string
	Missing string
}

// ResolveOwner picks the thread a task's output posts to: its configured
// owner; else, for a persistent task, the agent it delivers into; else the
// agent whose workspace is the task's; else DefaultAgent. A configured or
// persistent owner that is not among agents falls back to DefaultAgent with
// Missing set.
func ResolveOwner(cfg *config.Config, task string, agents []AgentRef) Owner {
	known := func(name string) bool {
		for _, a := range agents {
			if a.Name == name {
				return true
			}
		}
		return false
	}
	tc, ok := cfg.Tasks[task]
	if !ok {
		return Owner{Agent: DefaultAgent}
	}
	want := tc.Owner
	if want == "" && tc.Runtime == "persistent" {
		if name, _, _, err := cfg.ResolveTaskTarget(task); err == nil {
			want = name
		}
	}
	if want != "" {
		if known(want) {
			return Owner{Agent: want}
		}
		return Owner{Agent: DefaultAgent, Missing: want}
	}
	// Several agents can share a workspace (the default one, typically):
	// DefaultAgent wins among them, else the first by name.
	ws := filepath.Clean(cfg.TaskWorkspace(tc))
	match := ""
	for _, a := range agents {
		if a.Workspace == "" || filepath.Clean(a.Workspace) != ws {
			continue
		}
		if a.Name == DefaultAgent {
			return Owner{Agent: DefaultAgent}
		}
		if match == "" || a.Name < match {
			match = a.Name
		}
	}
	if match != "" {
		return Owner{Agent: match}
	}
	return Owner{Agent: DefaultAgent}
}
