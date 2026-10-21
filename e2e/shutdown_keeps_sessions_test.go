//go:build e2e

package e2e

import (
	"bufio"
	"fmt"
	"net/http"
	"strings"
	"syscall"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agent"
)

// TestDaemonSIGTERMWithOpenEventStreamKeepsAgentSession stops the daemon the
// way a takeover or post-update restart does (SIGTERM) while a client holds
// /api/v1/events open. The stream used to stall the daemon's exit long enough
// for the agent watchers to kill the session; it must now survive untouched
// and be adopted by the next daemon.
func TestDaemonSIGTERMWithOpenEventStreamKeepsAgentSession(t *testing.T) {
	s := newInteractiveE2E(t)
	name := s.spawnAgent(t, "keeper")
	session := agent.SessionName(name)
	s.waitForSession(t, session)
	pid := s.panePID(t, session)

	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/api/v1/events", s.port), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("opening the event stream: %v", err)
	}
	defer resp.Body.Close()
	stream := bufio.NewReader(resp.Body)
	if line, err := stream.ReadString('\n'); err != nil || !strings.HasPrefix(line, "event: hello") {
		t.Fatalf("first stream line = %q, %v; want the hello event", line, err)
	}

	if err := s.service.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM to the daemon: %v", err)
	}
	_ = s.service.Wait()

	if got := s.panePID(t, session); got != pid {
		t.Fatalf("shutdown disturbed the session: pane pid %s, was %s", got, pid)
	}

	s.startDaemon(t)
	s.waitFor(t, func() bool { return strings.Contains(s.serviceLog(), "adopted existing tmux session '"+session+"'") })
	if got := s.panePID(t, session); got != pid {
		t.Fatalf("restart relaunched the session: pane pid %s, was %s", got, pid)
	}
}
