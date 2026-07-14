package picker

import (
	"context"

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
}

// NewLocalBackend builds a local backend bound to the given leo home.
func NewLocalBackend(homePath string) *LocalBackend {
	return &LocalBackend{
		homePath: homePath,
		list:     daemon.AgentList,
		stop:     daemon.AgentStop,
		suspend:  daemon.AgentSuspend,
		resume:   daemon.AgentResume,
		rename:   daemon.AgentRename,
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
