package daemon

import (
	"context"
	"fmt"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/config"
)

// EnsureSpec describes the agent target an invocation must be delivered to.
// Name is both the router's FIFO queue key and the agent name; Template is
// the effective TemplateConfig to spawn from if the agent does not exist yet
// — resolved once by config.ResolveTaskTarget for both explicit (`template:`)
// and implicit (synthesized from the task's own fields) tasks, so Ensure
// itself does not need to re-resolve config. TemplateName and Implicit are
// carried through for diagnostics/logging only; Ensure's own logic does not
// branch on them because Template already holds the right config either way.
type EnsureSpec struct {
	Name         string
	TemplateName string
	Template     config.TemplateConfig
	Implicit     bool
}

// EnsureAgentManager is the narrow view of agent.Manager the ensurer needs.
// Defined here (rather than depending on the broader daemon.AgentManager
// socket-handler interface) so tests exercise Ensure against a tiny fake
// instead of standing up a full agent.Manager.
type EnsureAgentManager interface {
	// Live reports whether name is a currently running (supervised) agent.
	Live(name string) bool
	// Suspended reports whether name has a persisted-but-stopped record that
	// can be resumed.
	Suspended(name string) bool
	// Resume restarts a suspended agent, rejoining its prior session.
	Resume(name string) (agent.Record, error)
	// SpawnFromTemplate spawns a repo-less agent named `name` directly from
	// an already-resolved TemplateConfig.
	SpawnFromTemplate(ctx context.Context, name string, tmpl config.TemplateConfig) (agent.Record, error)
}

// AgentEnsurer makes sure an agent target is injectable — running, or made
// so — before the session router injects a prompt into it.
type AgentEnsurer interface {
	Ensure(ctx context.Context, spec EnsureSpec) error
}

// managerEnsurer implements AgentEnsurer over the narrow EnsureAgentManager
// view of agent.Manager.
type managerEnsurer struct {
	mgr EnsureAgentManager
}

// NewAgentEnsurer constructs an AgentEnsurer backed by mgr (in production,
// *agent.Manager; tests pass a fake satisfying EnsureAgentManager).
func NewAgentEnsurer(mgr EnsureAgentManager) AgentEnsurer {
	return &managerEnsurer{mgr: mgr}
}

// Ensure guarantees spec.Name is injectable: a no-op when already live,
// Resume when suspended, and a spawn from spec.Template when there is no
// record at all.
func (e *managerEnsurer) Ensure(ctx context.Context, spec EnsureSpec) error {
	if e.mgr.Live(spec.Name) {
		return nil
	}
	if e.mgr.Suspended(spec.Name) {
		if _, err := e.mgr.Resume(spec.Name); err != nil {
			return fmt.Errorf("resuming agent %q: %w", spec.Name, err)
		}
		return nil
	}
	if _, err := e.mgr.SpawnFromTemplate(ctx, spec.Name, spec.Template); err != nil {
		return fmt.Errorf("spawning agent %q: %w", spec.Name, err)
	}
	return nil
}
