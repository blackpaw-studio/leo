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

// WaitReady blocks until a daemon for workDir answers /health with
// ready=true, or until timeout or ctx ends. A service-manager restart
// (launchctl kickstart -k, systemctl restart) returns before the new daemon
// has bound its socket — possibly while the old one is still answering — and
// the socket answers before agents are restored, so callers that query agents
// right after a restart must wait here first.
//
// previousPID is the pre-restart daemon's PID (see DaemonPID); a daemon
// reporting it is never accepted. Pass 0 when there is no previous daemon to
// exclude.
func WaitReady(ctx context.Context, workDir string, timeout time.Duration, previousPID int) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	sockPath := SockPath(workDir)
	ticker := time.NewTicker(healthPollInterval)
	defer ticker.Stop()
	seen := seenNothing
	for {
		health, ok := probeHealth(ctx, sockPath)
		state := classifyProbe(health, ok, previousPID)
		if state == seenReady {
			return nil
		}
		seen = max(seen, state)
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s on %s within %s: %w", seen, sockPath, timeout, ctx.Err())
		case <-ticker.C:
		}
	}
}

// probeState is what one /health probe saw, ordered by how far the restart
// had progressed, so WaitReady reports the furthest stage it observed.
type probeState int

const (
	seenNothing probeState = iota
	seenPrevious
	seenStarting
	seenReady
)

func (p probeState) String() string {
	switch p {
	case seenPrevious:
		return "only the previous daemon answered"
	case seenStarting:
		return "daemon answered but was not ready (still restoring agents)"
	case seenReady:
		return "daemon ready"
	default:
		return "daemon did not answer"
	}
}

func classifyProbe(health healthData, ok bool, previousPID int) probeState {
	switch {
	case !ok:
		return seenNothing
	case previousPID != 0 && health.PID == previousPID:
		return seenPrevious
	case !health.Ready:
		return seenStarting
	default:
		return seenReady
	}
}

// DaemonPID reports the PID of the daemon answering on workDir's socket. ok
// is false when no daemon answers (or it predates the pid field); restart
// callers then have no previous daemon to exclude.
func DaemonPID(ctx context.Context, workDir string) (int, bool) {
	health, ok := probeHealth(ctx, SockPath(workDir))
	if !ok || health.PID == 0 {
		return 0, false
	}
	return health.PID, true
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
