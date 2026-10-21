package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/drain"
	"github.com/blackpaw-studio/leo/internal/managed"
)

// restartResult is `leo service restart --json`'s success object.
type restartResult struct {
	OK        bool     `json:"ok"`
	Drained   bool     `json:"drained"`
	Restarted bool     `json:"restarted"`
	Warnings  []string `json:"warnings,omitempty"`
}

// beginTracker notes whether a drain was ever started.
type beginTracker struct {
	drainClient
	begun bool
}

func (b *beginTracker) Begin(ctx context.Context, timeout time.Duration) (drain.Status, error) {
	st, err := b.drainClient.Begin(ctx, timeout)
	if err == nil {
		b.begun = true
	}
	return st, err
}

// restartJSON is the machine-readable restart: a job that is not loaded fails
// at once (nothing to drain or kickstart), a daemon that is down but whose job
// is loaded restarts without a drain, and a drain timeout without --force
// fails with the agents told to resume. Progress goes to deps.out (stderr).
func restartJSON(ctx context.Context, cfg *config.Config, st managed.State, opts gracefulOptions, deps gracefulDeps) (any, error) {
	if !daemonJobLoaded(cfg, st) {
		return nil, failJSON("not_registered", fmt.Errorf("no launchd job %s is registered, so there is nothing to restart", jobLabel(cfg, st)))
	}
	tracker := &beginTracker{drainClient: deps.drain}
	deps.drain = tracker
	restart := deps.restart
	restartAttempted := false
	deps.restart = func(ctx context.Context) error {
		restartAttempted = true
		return restart(ctx)
	}

	res := restartResult{OK: true, Restarted: true}
	err := gracefulRestart(ctx, opts, deps)
	switch {
	case err == nil:
	case errors.Is(err, errDaemonNotReady):
		res.Warnings = append(res.Warnings, err.Error())
	case errors.Is(err, errDrainTimeout):
		return nil, failJSON("drain_timeout", err)
	case restartAttempted:
		return nil, failJSON("restart_failed", err)
	default:
		return nil, failJSON("drain_failed", err)
	}
	res.Drained = tracker.begun
	return res, nil
}

// jobLabel names the launchd job that owns the daemon.
func jobLabel(cfg *config.Config, st managed.State) string {
	if st.Managed {
		return st.LaunchdLabel
	}
	return daemonLabelFn(cfg.HomePath)
}
