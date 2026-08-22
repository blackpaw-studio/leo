// Package picker renders a full-screen Bubble Tea picker over all leo agents
// (local and remote), with fuzzy search and in-place lifecycle actions. It is
// decoupled from the daemon and SSH transport via the Backend interface so the
// tea model can be unit-tested against fakes with no I/O.
package picker

import (
	"context"
	"fmt"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
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
	// Stop makes an agent dormant with WakeOnMessage=false — a picker-initiated
	// stop is always operator intent, never auto-wakeable. Reversible via Start.
	Stop(ctx context.Context, name string) error
	// Start clears a dormant agent's flags and respawns it, resuming its prior
	// conversation.
	Start(ctx context.Context, name string) error
	// DeletePlan reports what Delete would remove, for the confirm dialog —
	// the single source of truth for "does this agent have a worktree" (see
	// agent.Manager.DeletePlan). It performs no mutation.
	DeletePlan(ctx context.Context, name string) (agent.DeletePlan, error)
	// Delete permanently removes the agent's record — plus its worktree and,
	// when deleteBranch is true, its branch. Refuses a live agent.
	Delete(ctx context.Context, name string, deleteBranch bool) error
	// Templates lists the template names configured on this host — the menu
	// the picker offers when re-pointing an agent at another template.
	Templates(ctx context.Context) ([]string, error)
	// SwitchTemplate re-points an agent at another template, keeping its
	// workspace and swapping which per-template conversation is live.
	// Permission to do so is checked by the model before dispatch, not here.
	SwitchTemplate(ctx context.Context, name, template string) error
}

// Result carries the picker outcome. Agent is nil when the user quit without
// choosing anything.
type Result struct {
	Agent *Agent
}

// Run starts the picker over the given backends (keyed by host name) and blocks
// until the user attaches or quits. Attach happens in the caller AFTER Run
// returns, so tmux inherits a clean terminal.
//
// canSwitchTemplate refuses a template this process may not switch an agent
// onto, mirroring `leo agent set-template`'s permission gate. It is checked in
// the model rather than in a Backend because permissions belong to THIS
// process: a remote backend shells out to a leo that cannot see this agent's
// LEO_PERMISSIONS, so a per-backend check would leave remote rows open. Pass
// nil for no restriction.
func Run(ctx context.Context, backends map[string]Backend, canSwitchTemplate func(template string) error) (Result, error) {
	m := newModel(ctx, backends)
	m.canSwitch = canSwitchTemplate
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
