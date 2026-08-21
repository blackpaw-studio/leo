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
	// templates lists the local host's configured template names for the
	// template menu. Injected by the CLI layer, which already holds the loaded
	// config — the picker has only a leo home path, and re-reading leo.yaml
	// here would duplicate config-resolution rules the CLI has applied. Nil
	// makes the menu report that templates are unavailable.
	templates func() ([]string, error)
}

// localStop calls daemon.AgentStop with WakeOnMessage=false — an operator
// (or picker) initiated stop is never auto-wakeable, matching the daemon's
// one-dormant-state contract.
func localStop(ctx context.Context, workDir, name string) error {
	return daemon.AgentStop(ctx, workDir, name, false)
}

// localSuspend calls daemon.AgentStop with WakeOnMessage=true, preserving the
// picker's "suspend" affordance (dormant, but auto-wakeable on the next
// message) now that Suspend and Stop share one dormant state.
func localSuspend(ctx context.Context, workDir, name string) error {
	return daemon.AgentStop(ctx, workDir, name, true)
}

// localResume calls daemon.AgentStart, adapting its error-only signature to
// the (agent.Record, error) shape the picker's Backend interface still
// expects.
func localResume(ctx context.Context, workDir, name string) (agent.Record, error) {
	if err := daemon.AgentStart(ctx, workDir, name); err != nil {
		return agent.Record{}, err
	}
	return agent.Record{Name: name, Status: "starting"}, nil
}

// NewLocalBackend builds a local backend bound to the given leo home.
func NewLocalBackend(homePath string, templates func() ([]string, error)) *LocalBackend {
	return &LocalBackend{
		homePath:  homePath,
		list:      daemon.AgentList,
		stop:      localStop,
		suspend:   localSuspend,
		resume:    localResume,
		rename:    daemon.AgentRename,
		switchTo:  daemon.AgentSwitchTemplate,
		templates: templates,
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
	if b.templates == nil {
		return nil, fmt.Errorf("templates are unavailable for this host")
	}
	return b.templates()
}

func (b *LocalBackend) SwitchTemplate(ctx context.Context, name, template string) error {
	_, err := b.switchTo(ctx, b.homePath, name, template)
	return err
}
