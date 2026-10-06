package daemon

import (
	"context"
	"fmt"
	"time"
)

// healthPollInterval paces WaitHealthy's probes; each probe is itself bounded
// to one second by SocketHealthy.
const healthPollInterval = 200 * time.Millisecond

// WaitHealthy blocks until the daemon for workDir answers /health, or until
// timeout or ctx ends. A service-manager restart (launchctl kickstart -k,
// systemctl restart) returns before the new daemon has bound its socket, so
// callers that talk to the daemon right after a restart must wait here first.
func WaitHealthy(ctx context.Context, workDir string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(healthPollInterval)
	defer ticker.Stop()
	for {
		if SocketHealthy(ctx, SockPath(workDir)) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("daemon did not answer on %s within %s: %w", SockPath(workDir), timeout, ctx.Err())
		case <-ticker.C:
		}
	}
}
