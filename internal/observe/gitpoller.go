package observe

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// DefaultGitPollInterval is how often the poller reads every running
	// agent's workspace.
	DefaultGitPollInterval = 10 * time.Second
	// DefaultGitPollConcurrency bounds the git calls in flight at once.
	DefaultGitPollConcurrency = 4
)

// GitAgent is one running agent and the workspace its git stats come from.
type GitAgent struct {
	Name      string
	Workspace string
}

// GitAgentLister lists the agents worth polling: running ones only
// (suspended and stopped agents change nothing).
type GitAgentLister interface {
	GitAgents() []GitAgent
}

// GitStatsSink receives each poll's complete result, by agent name.
type GitStatsSink interface {
	SetGitStats(stats map[string]GitStats)
}

// GitPollerOptions tunes a GitPoller; zero values take the defaults.
type GitPollerOptions struct {
	Interval    time.Duration
	Concurrency int
	// NewTicker replaces the wall-clock ticker (tests): it returns the tick
	// channel and its stop func.
	NewTicker func(d time.Duration) (<-chan time.Time, func())
	// Logf receives failures worth a line (nil logs nothing).
	Logf func(format string, args ...any)
}

// GitPoller reads `git diff HEAD --shortstat` for every running agent's
// workspace on an interval and hands the result to a GitStatsSink: one git
// call per unique workspace, a bounded number at a time, and no overlapping
// polls (a tick that finds the previous one still running is skipped).
type GitPoller struct {
	lister GitAgentLister
	runner CommandRunner
	sink   GitStatsSink
	opts   GitPollerOptions

	polling atomic.Bool
}

// NewGitPoller builds a poller over lister, running git through runner and
// publishing to sink.
func NewGitPoller(lister GitAgentLister, runner CommandRunner, sink GitStatsSink, opts GitPollerOptions) *GitPoller {
	if opts.Interval <= 0 {
		opts.Interval = DefaultGitPollInterval
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = DefaultGitPollConcurrency
	}
	if opts.NewTicker == nil {
		opts.NewTicker = func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(d)
			return t.C, t.Stop
		}
	}
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	return &GitPoller{lister: lister, runner: runner, sink: sink, opts: opts}
}

// Run polls on every tick until ctx ends.
func (p *GitPoller) Run(ctx context.Context) {
	ticks, stop := p.opts.NewTicker(p.opts.Interval)
	defer stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			p.Tick(ctx)
		}
	}
}

// Tick polls once, returning false without polling when the previous poll is
// still in flight.
func (p *GitPoller) Tick(ctx context.Context) bool {
	if !p.polling.CompareAndSwap(false, true) {
		return false
	}
	defer p.polling.Store(false)

	byWorkspace := map[string][]string{} // cleaned workspace → agent names
	for _, a := range p.lister.GitAgents() {
		if a.Workspace == "" {
			continue
		}
		ws := filepath.Clean(a.Workspace)
		byWorkspace[ws] = append(byWorkspace[ws], a.Name)
	}

	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		out  = map[string]GitStats{}
		errs []error
		sem  = make(chan struct{}, p.opts.Concurrency)
	)
	for ws, names := range byWorkspace {
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			stats, ok, err := GitStatsFor(ctx, p.runner, ws)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
			}
			if !ok {
				return
			}
			for _, name := range names {
				out[name] = stats
			}
		}()
	}
	wg.Wait()
	for _, err := range errs { // logged here so Logf is never called concurrently
		p.opts.Logf("%v", err)
	}
	p.sink.SetGitStats(out)
	return true
}
