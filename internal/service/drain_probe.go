package service

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/drain"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

// drainProbeTimeout bounds each tmux call a probe makes, matching
// tmux.InjectIntoWith's per-command timeout.
const drainProbeTimeout = 5 * time.Second

// drainProber implements drain.Prober over the supervisor's ephemeral
// agents: a connected bridge reports its busy flag, any other agent is
// read from its pane the way tmux.InjectInto reads the composer.
type drainProber struct {
	sup      *Supervisor
	tmuxPath string
	command  tmux.CommandFunc
	logf     func(format string, args ...any)
}

func newDrainProber(sup *Supervisor, tmuxPath string, logf func(string, ...any)) *drainProber {
	return &drainProber{sup: sup, tmuxPath: tmuxPath, command: exec.CommandContext, logf: logf}
}

// BusyAgents probes every ephemeral agent, in name order, and returns the
// busy ones. A probe error is logged and the agent counts as not busy.
func (p *drainProber) BusyAgents(ctx context.Context) ([]drain.Probe, error) {
	agents := p.sup.EphemeralAgents()
	names := make([]string, 0, len(agents))
	for name := range agents {
		names = append(names, name)
	}
	sort.Strings(names)
	var busy []drain.Probe
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		probe, err := p.Probe(ctx, name)
		if err != nil {
			p.log("drain: probing %s: %v", name, err)
		}
		if probe.Busy {
			busy = append(busy, probe)
		}
	}
	return busy, nil
}

// Probe reports one agent; Exists is false once it is no longer an
// ephemeral agent of this supervisor.
func (p *drainProber) Probe(ctx context.Context, name string) (drain.Probe, error) {
	if _, ok := p.sup.EphemeralAgents()[name]; !ok {
		return drain.Probe{Name: name}, nil
	}
	st, connected := p.sup.BridgeState(name)
	return drain.Classify(drain.ProbeInput{
		Name:            name,
		BridgeConnected: connected,
		BridgeBusy:      st.Busy,
		Harness:         p.sup.HarnessOf(name),
		Capture:         func() (string, error) { return p.capture(ctx, name) },
	})
}

func (p *drainProber) capture(ctx context.Context, name string) (string, error) {
	pane, err := p.resolvePane(ctx, agent.SessionName(name))
	if err != nil {
		return "", err
	}
	out, err := p.run(ctx, "capture-pane", "-p", "-t", pane)
	if err != nil {
		return "", fmt.Errorf("tmux capture-pane -t %s: %w", pane, err)
	}
	return out, nil
}

// resolvePane is tmux.ResolvePane through this prober's command seam.
func (p *drainProber) resolvePane(ctx context.Context, session string) (string, error) {
	target := tmux.PaneTarget(session)
	out, err := p.run(ctx, "list-panes", "-t", target, "-F", "#{pane_id}")
	if err != nil {
		return "", fmt.Errorf("tmux list-panes -t %q: %w", target, err)
	}
	return tmux.LowestPaneID(out)
}

func (p *drainProber) run(ctx context.Context, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, drainProbeTimeout)
	defer cancel()
	c := p.command(cctx, p.tmuxPath, tmux.Args(args...)...)
	c.WaitDelay = 100 * time.Millisecond
	out, err := c.Output()
	return string(out), err
}

func (p *drainProber) log(format string, args ...any) {
	if p.logf != nil {
		p.logf(format, args...)
	}
}
