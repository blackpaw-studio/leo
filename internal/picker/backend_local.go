package picker

import (
	"context"
	"fmt"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/daemon"
)

// LocalBackend wraps the local daemon client. The daemon funcs are stored as
// fields so a test can inject fakes without a live socket. Agent names returned
// by List are canonical, so lifecycle calls pass them straight through (no
// shorthand resolution needed).
type LocalBackend struct {
	homePath string
	list     func(ctx context.Context, workDir string) ([]agent.Record, error)
	stop     func(ctx context.Context, workDir, name string) error
	suspend  func(ctx context.Context, workDir, name string) error
	resume   func(ctx context.Context, workDir, name string) (agent.Record, error)
	rename   func(ctx context.Context, workDir, query, newName string) (agent.Record, error)
	switchTo func(ctx context.Context, workDir, name, template string) (agent.SwitchResult, error)
	policy   LocalPolicy
}

// LocalPolicy carries the host-side policy the picker cannot resolve on its
// own: which templates exist, and whether this process may switch an agent onto
// one. Both are supplied by the CLI layer, which holds the loaded config and
// this process's permissions — the picker has only a leo home path.
type LocalPolicy struct {
	// Templates lists the configured template names for the template menu.
	Templates func() ([]string, error)
	// CanSwitchTo refuses a switch this process is not permitted to make,
	// mirroring `leo agent set-template`'s gate so pressing t in the picker
	// cannot reach a template the CLI verb would refuse. A nil func means no
	// restriction, matching how an unset LEO_PERMISSIONS reads elsewhere.
	CanSwitchTo func(template string) error
}

// NewLocalBackend builds a local backend bound to the given leo home. A zero
// LocalPolicy leaves the template menu unavailable and unrestricted; the CLI
// always supplies a real one.
func NewLocalBackend(homePath string, policy LocalPolicy) *LocalBackend {
	return &LocalBackend{
		homePath: homePath,
		list:     daemon.AgentList,
		stop:     daemon.AgentStop,
		suspend:  daemon.AgentSuspend,
		resume:   daemon.AgentResume,
		rename:   daemon.AgentRename,
		switchTo: daemon.AgentSwitchTemplate,
		policy:   policy,
	}
}

func (b *LocalBackend) List(ctx context.Context) ([]Agent, error) {
	records, err := b.list(ctx, b.homePath)
	if err != nil {
		return nil, err
	}
	agents := make([]Agent, 0, len(records))
	for _, r := range records {
		agents = append(agents, Agent{
			Name:      r.Name,
			Template:  r.Template,
			Host:      LocalHost,
			Status:    r.Status,
			StartedAt: r.StartedAt,
		})
	}
	return agents, nil
}

func (b *LocalBackend) Rename(ctx context.Context, oldName, newName string) error {
	_, err := b.rename(ctx, b.homePath, oldName, newName)
	return err
}

func (b *LocalBackend) Stop(ctx context.Context, name string) error {
	return b.stop(ctx, b.homePath, name)
}

func (b *LocalBackend) Suspend(ctx context.Context, name string) error {
	return b.suspend(ctx, b.homePath, name)
}

func (b *LocalBackend) Resume(ctx context.Context, name string) error {
	_, err := b.resume(ctx, b.homePath, name)
	return err
}

func (b *LocalBackend) Templates(context.Context) ([]string, error) {
	if b.policy.Templates == nil {
		return nil, fmt.Errorf("templates are unavailable for this host")
	}
	return b.policy.Templates()
}

func (b *LocalBackend) SwitchTemplate(ctx context.Context, name, template string) error {
	if b.policy.CanSwitchTo != nil {
		if err := b.policy.CanSwitchTo(template); err != nil {
			return err
		}
	}
	_, err := b.switchTo(ctx, b.homePath, name, template)
	return err
}
