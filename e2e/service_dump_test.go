//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// healthPID reads the daemon's pid from /health on its unix socket.
func (s *interactiveE2E) healthPID(t *testing.T) int {
	t.Helper()
	sock := filepath.Join(s.ws, "state", "leo.sock")
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		}},
	}
	resp, err := client.Get("http://daemon/health")
	if err != nil {
		t.Fatalf("GET /health: %v\nservice output:\n%s", err, s.output.String())
	}
	defer resp.Body.Close()
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			PID           int  `json:"pid"`
			Ready         bool `json:"ready"`
			GoroutineDump bool `json:"goroutine_dump"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil || !env.OK {
		t.Fatalf("decoding /health: ok=%v err=%v", env.OK, err)
	}
	if !env.Data.GoroutineDump {
		t.Fatalf("/health does not advertise goroutine_dump")
	}
	return env.Data.PID
}

// TestServiceDumpWritesGoroutinesAndLeavesDaemonServing sends the real
// SIGUSR1 through `leo service dump`: the daemon must write the dump, keep
// serving under the same pid, and, once stopped, the command must refuse
// rather than signal anything.
func TestServiceDumpWritesGoroutinesAndLeavesDaemonServing(t *testing.T) {
	s := newInteractiveE2E(t)
	pid := s.healthPID(t)

	stdout, stderr, code := runLeo(t, s.ws, nil, "service", "dump", "-c", s.cfgPath)
	if code != 0 {
		t.Fatalf("leo service dump exit %d\nstdout: %s\nstderr: %s\nservice output:\n%s", code, stdout, stderr, s.output.String())
	}
	path := strings.TrimSpace(stdout)
	if filepath.Dir(path) != filepath.Join(s.ws, "state") || !strings.HasPrefix(filepath.Base(path), "goroutines-") {
		t.Fatalf("printed path %q is not a dump file under state/", path)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading printed dump: %v", err)
	}
	if !strings.Contains(string(body), "goroutine ") {
		t.Errorf("dump lacks goroutine stacks: %.200s", body)
	}
	if got := s.healthPID(t); got != pid {
		t.Errorf("daemon pid changed from %d to %d: the signal disturbed it", pid, got)
	}
	s.waitFor(t, func() bool { return strings.Contains(s.serviceLog(), "goroutine dump written to") })
	if !strings.Contains(s.serviceLog(), "startup: total took") {
		t.Errorf("service.log lacks the startup total line:\n%s", s.serviceLog())
	}

	if err := s.service.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM to the daemon: %v", err)
	}
	_ = s.service.Wait()

	_, stderr, code = runLeo(t, s.ws, nil, "service", "dump", "-c", s.cfgPath)
	if code == 0 || !strings.Contains(stderr, "not running") {
		t.Errorf("dump against a stopped daemon: exit %d, stderr %q; want non-zero and \"not running\"", code, stderr)
	}
}
