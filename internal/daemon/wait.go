package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// healthPollInterval paces WaitReady's probes; each probe is itself bounded
// to one second by probeTimeout.
const healthPollInterval = 200 * time.Millisecond

// probeTimeout bounds a single /health round-trip during WaitReady.
const probeTimeout = time.Second

// WaitReady blocks until the daemon for workDir answers /health with
// ready=true, or until timeout or ctx ends. A service-manager restart
// (launchctl kickstart -k, systemctl restart) returns before the new daemon
// has bound its socket, and the socket answers before agents are restored,
// so callers that query agents right after a restart must wait here first.
func WaitReady(ctx context.Context, workDir string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	sockPath := SockPath(workDir)
	ticker := time.NewTicker(healthPollInterval)
	defer ticker.Stop()
	answered := false
	for {
		health, ok := probeHealth(ctx, sockPath)
		if ok && health.Ready {
			return nil
		}
		answered = answered || ok
		select {
		case <-ctx.Done():
			if answered {
				return fmt.Errorf("daemon on %s answered but was not ready (still restoring agents) within %s: %w", sockPath, timeout, ctx.Err())
			}
			return fmt.Errorf("daemon did not answer on %s within %s: %w", sockPath, timeout, ctx.Err())
		case <-ticker.C:
		}
	}
}

// probeHealth performs one GET /health and decodes its payload. ok is false
// when the daemon is unreachable (missing or stale socket) or answers with
// anything other than a well-formed 200.
func probeHealth(ctx context.Context, sockPath string) (healthData, bool) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://daemon/health", nil)
	if err != nil {
		return healthData{}, false
	}
	resp, err := newUnixClient(sockPath).Do(req)
	if err != nil {
		return healthData{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return healthData{}, false
	}

	var envelope Response
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil || !envelope.OK {
		return healthData{}, false
	}
	var health healthData
	if err := json.Unmarshal(envelope.Data, &health); err != nil {
		return healthData{}, false
	}
	return health, true
}
