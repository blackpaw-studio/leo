package config

import (
	"fmt"
)

// SessionTopology classifies how a persistent task maps to a supervised
// session. The tmux session naming differs per topology, so this is resolved
// once (here) rather than re-derived by each consumer.
type SessionTopology int

const (
	// TopologyDedicated — task with no `session:`; an implicit session named
	// after the task (Topology A). Supervised as a persistent session.
	TopologyDedicated SessionTopology = iota
	// TopologyShared — task references a `sessions:` entry (Topology B).
	// Supervised as a persistent session.
	TopologyShared
)

// ResolveSession returns the session name, its topology, and the SessionConfig
// that hosts the named persistent task. For tasks without `session:` it
// synthesizes an implicit SessionConfig from the task itself and returns the
// task name as the session name. Returns an error for oneshot tasks or
// unresolved references.
func (c *Config) ResolveSession(taskName string) (string, SessionTopology, SessionConfig, error) {
	task, ok := c.Tasks[taskName]
	if !ok {
		return "", TopologyDedicated, SessionConfig{}, fmt.Errorf("task %q not found", taskName)
	}
	if task.Runtime != "persistent" {
		return "", TopologyDedicated, SessionConfig{}, fmt.Errorf("task %q is not runtime: persistent", taskName)
	}

	switch {
	case task.Session == "":
		// Topology A — dedicated, inherit from task. Lazy and QueueMax remain
		// task-scoped fields; they are not threaded into SessionConfig.
		// TaskConfig has no Agent/Env/AddDirs fields, so those stay zero in
		// the synthesized session; callers needing them must use a shared
		// session. Claude options (permission_mode, allowed_tools, etc.) live
		// in harness_options now and are decoded by the consumer
		// (internal/service/session.go via claudeSessionOptions), not copied
		// into this synthesized config.
		return taskName, TopologyDedicated, SessionConfig{
			Workspace: task.Workspace,
			Model:     task.Model,
			Channels:  task.Channels,
		}, nil

	default:
		// Topology B — shared session from sessions: map.
		sess, ok := c.Sessions[task.Session]
		if !ok {
			return "", TopologyShared, SessionConfig{}, fmt.Errorf("task %q references sessions.%s which is not defined", taskName, task.Session)
		}
		return task.Session, TopologyShared, sess, nil
	}
}

// channelSubset reports whether every element of want appears in have.
// Returns (missing, false) when an element of want is absent from have.
// Returns ("", true) when want is empty or fully covered.
func channelSubset(want, have []string) (string, bool) {
	set := make(map[string]struct{}, len(have))
	for _, c := range have {
		set[c] = struct{}{}
	}
	for _, c := range want {
		if _, ok := set[c]; !ok {
			return c, false
		}
	}
	return "", true
}
