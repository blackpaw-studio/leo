// Package picker renders a full-screen Bubble Tea picker over all leo agents
// (local and remote), with fuzzy search and in-place lifecycle actions. It is
// decoupled from the daemon and SSH transport via the Backend interface so the
// tea model can be unit-tested against fakes with no I/O.
package picker

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// LocalHost is the reserved backend key (and Agent.Host value) for agents
// served by the local daemon.
const LocalHost = "local"

// Agent is one row's worth of agent metadata, harness-agnostic.
type Agent struct {
	Name       string
	Template   string
	Host       string
	Status     string
	StartedAt  time.Time
	AttachOnly bool // remote tmux-fallback rows: attach works, lifecycle does not
}

// Backend is one host's control surface. "local" wraps the daemon client; each
// configured client.hosts entry is an SSH backend. Names passed to the action
// methods are canonical agent names as returned by List.
type Backend interface {
	List(ctx context.Context) ([]Agent, error)
	Rename(ctx context.Context, oldName, newName string) error
	Stop(ctx context.Context, name string) error
	Suspend(ctx context.Context, name string) error
	Resume(ctx context.Context, name string) error
}

// Result carries the picker outcome. Agent is nil when the user quit without
// choosing anything.
type Result struct {
	Agent *Agent
}

// Run starts the picker over the given backends (keyed by host name) and blocks
// until the user attaches or quits. Attach happens in the caller AFTER Run
// returns, so tmux inherits a clean terminal.
func Run(ctx context.Context, backends map[string]Backend) (Result, error) {
	m := newModel(ctx, backends)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	final, err := p.Run()
	if err != nil {
		return Result{}, err
	}
	fm, ok := final.(model)
	if !ok {
		return Result{}, fmt.Errorf("picker: unexpected final model type %T", final)
	}
	return fm.result, nil
}
