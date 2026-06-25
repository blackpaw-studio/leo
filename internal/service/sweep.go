package service

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

// idleSweepInterval is how often the daemon checks live agents for idleness.
// A package var so tests can shorten it.
var idleSweepInterval = 60 * time.Second

// runIdleSweep periodically suspends ephemeral agents that have been idle past
// their configured interval. It runs for the daemon's lifetime; ctx cancellation
// (shutdown) stops it.
func runIdleSweep(ctx context.Context, sup *Supervisor, mgr *agent.Manager, tmuxPath, homePath string) {
	t := time.NewTicker(idleSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		sweepIdleAgents(ctx, sup, mgr, tmuxPath, homePath)
	}
}

// sweepIdleAgents runs a single sweep pass: for each live ephemeral agent with a
// configured idle interval, suspend it if its tmux session has been inactive
// long enough and no client is attached.
func sweepIdleAgents(ctx context.Context, sup *Supervisor, mgr *agent.Manager, tmuxPath, homePath string) {
	records, err := agentstore.Load(agentstore.FilePath(homePath))
	if err != nil || len(records) == 0 {
		return
	}
	activity, err := tmux.ListSessionActivity(ctx, tmuxPath)
	if err != nil {
		return
	}
	now := time.Now()
	for name := range sup.EphemeralAgents() {
		rec, ok := records[name]
		if !ok {
			continue
		}
		idle := parseIdle(rec.IdleSuspendAfter)
		if idle <= 0 {
			continue
		}
		act, ok := activity[agent.SessionName(name)]
		if !ok {
			continue // no tmux session metadata — leave it alone
		}
		if shouldSuspend(now, act, idle) {
			if err := mgr.Suspend(name); err != nil {
				fmt.Fprintf(os.Stderr, "idle-sweep: suspend %q failed: %v\n", name, err)
			} else {
				fmt.Fprintf(os.Stdout, "idle-sweep: suspended %q (idle >= %s)\n", name, idle)
			}
		}
	}
}

// shouldSuspend reports whether an agent should be suspended now: idle-suspend
// must be enabled, no client attached, and the session inactive for at least the
// idle interval.
func shouldSuspend(now time.Time, act tmux.SessionActivity, idle time.Duration) bool {
	if idle <= 0 || act.Attached > 0 {
		return false
	}
	return now.Sub(act.LastActivity) >= idle
}

// parseIdle parses a stored idle-interval string. Empty, invalid, or
// non-positive values mean "disabled" (0).
func parseIdle(s string) time.Duration {
	if s == "" {
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0
	}
	return d
}
