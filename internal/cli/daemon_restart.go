package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/service"
)

// daemonReadyTimeout bounds the wait for a restarted daemon to bind its
// socket and finish restoring agents. Restore re-adopts or respawns every
// agent record, which with a few dozen agents takes well past a few seconds.
const daemonReadyTimeout = 60 * time.Second

// errDaemonNotReady marks a restart that succeeded but whose daemon did not
// report ready in time. Callers treat it as a warning, not a failure: the
// daemon is up, it just cannot yet answer agent queries reliably.
var errDaemonNotReady = errors.New("daemon restarted but did not become ready")

// daemonRestartDeps are the side effects restartDaemonAndWait drives.
type daemonRestartDeps struct {
	restart   func(home string) error
	waitReady func(ctx context.Context, home string, timeout time.Duration) error
}

var defaultDaemonRestartDeps = daemonRestartDeps{
	restart:   service.RestartDaemon,
	waitReady: daemon.WaitReady,
}

// restartDaemonAndWait restarts the daemon via the service manager and waits
// until the new daemon has restored its agents. launchctl kickstart -k and
// systemctl restart return before the new daemon binds its socket, and the
// socket answers before restore finishes, so anything that queries agents
// right after a restart must go through here. A wait failure wraps
// errDaemonNotReady; a restart failure does not.
func restartDaemonAndWait(ctx context.Context, home string, deps daemonRestartDeps) error {
	if err := deps.restart(home); err != nil {
		return fmt.Errorf("restarting daemon: %w", err)
	}
	if err := deps.waitReady(ctx, home, daemonReadyTimeout); err != nil {
		return fmt.Errorf("%w: %w", errDaemonNotReady, err)
	}
	return nil
}
