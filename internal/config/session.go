package config

import (
	"fmt"
	"strings"
)

// ResolveSession returns the session name and SessionConfig that hosts the
// named persistent task. For tasks without `session:` it synthesizes an
// implicit SessionConfig from the task itself and returns the task name as
// the session name. For `session: process:<name>` it returns the process
// name with a SessionConfig derived from the ProcessConfig. Returns an error
// for oneshot tasks or unresolved references.
func (c *Config) ResolveSession(taskName string) (string, SessionConfig, error) {
	task, ok := c.Tasks[taskName]
	if !ok {
		return "", SessionConfig{}, fmt.Errorf("task %q not found", taskName)
	}
	if task.Runtime != "persistent" {
		return "", SessionConfig{}, fmt.Errorf("task %q is not runtime: persistent", taskName)
	}

	switch {
	case task.Session == "":
		// Topology A — dedicated, inherit from task. Lazy and QueueMax remain
		// task-scoped fields; they are not threaded into SessionConfig.
		// TaskConfig has no Agent/Env/AddDirs fields, so those stay zero in
		// the synthesized session; callers needing them must use a shared
		// session or a process: reference.
		return taskName, SessionConfig{
			Workspace:          task.Workspace,
			Model:              task.Model,
			PermissionMode:     task.PermissionMode,
			AllowedTools:       task.AllowedTools,
			DisallowedTools:    task.DisallowedTools,
			AppendSystemPrompt: task.AppendSystemPrompt,
			Channels:           task.Channels,
		}, nil

	case strings.HasPrefix(task.Session, "process:"):
		// Topology C — reuse a supervised process.
		procName := strings.TrimPrefix(task.Session, "process:")
		proc, ok := c.Processes[procName]
		if !ok {
			return "", SessionConfig{}, fmt.Errorf("task %q references process:%s which is not defined", taskName, procName)
		}
		return procName, SessionConfig{
			Workspace:          proc.Workspace,
			Model:              proc.Model,
			Agent:              proc.Agent,
			PermissionMode:     proc.PermissionMode,
			AllowedTools:       proc.AllowedTools,
			DisallowedTools:    proc.DisallowedTools,
			AppendSystemPrompt: proc.AppendSystemPrompt,
			AddDirs:            proc.AddDirs,
			Channels:           proc.Channels,
			Env:                proc.Env,
		}, nil

	default:
		// Topology B — shared session from sessions: map.
		sess, ok := c.Sessions[task.Session]
		if !ok {
			return "", SessionConfig{}, fmt.Errorf("task %q references sessions.%s which is not defined", taskName, task.Session)
		}
		return task.Session, sess, nil
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
