package service

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/observe"
	"github.com/blackpaw-studio/leo/internal/run"
	"github.com/blackpaw-studio/leo/internal/web"
)

// freeTCPPort asks the OS for an unused loopback port, releasing it
// immediately so the caller can bind it. A hardcoded test port risks
// colliding with another test or a real process on the machine running the
// suite; this trades that for a (much smaller) bind-time TOCTOU window,
// which is the standard tradeoff for tests that need a real listener.
func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a free port: %v", err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}

// TestObservabilityWiringEndToEnd builds the same bus + run log + activity
// tracker + web.New wiring RunSupervised assembles in production (see
// defaultSupervisedExec and daemon.Server.SetObservability), then drives
// events through the real producer seams (Supervisor.SetPublisher,
// run.SetPublisher) and asserts they reach both HTTP surfaces. This is the
// class of bug a unit test behind a fake Publisher/EventSource can't catch:
// wiring that compiles but was never actually connected (an Option never
// passed to web.New, or a goroutine never started).
func TestObservabilityWiringEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fakeTmux := writeFakeTmuxScript(t)

	sv := NewSupervisor(ctx)
	sv.tmuxPath = fakeTmux
	sv.homePath = t.TempDir()
	t.Cleanup(func() { run.SetPublisher(nil) })

	// wireObservability is the exact function defaultSupervisedExec calls in
	// production — exercising it here (rather than re-deriving the same
	// bus/runLog/tracker/SetPublisher calls inline) is what makes this test
	// catch a regression in the wiring itself, not just in a hand-copied
	// reproduction of it.
	bus, runLog, tracker := wireObservability(ctx, sv, fakeTmux)

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "leo.yaml")
	if err := os.WriteFile(cfgPath, []byte("defaults:\n  model: sonnet\n"), 0600); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	testPort := freeTCPPort(t)
	const apiToken = "test-token-0123456789abcdef0123456789abcdef"
	webSrv := web.New(cfgPath, nil, nil, nil, nil, web.Options{
		Port:     testPort,
		APIToken: apiToken,
	}, web.WithEventSource(bus), web.WithActivityProvider(tracker), web.WithRunLog(runLog), web.WithVersion("v-wiring-test"))

	addr := fmt.Sprintf("127.0.0.1:%d", testPort)
	if err := webSrv.ListenAndServe(addr); err != nil {
		t.Fatalf("ListenAndServe: %v", err)
	}
	t.Cleanup(func() { _ = webSrv.Shutdown() })
	baseURL := "http://" + addr

	// --- Open the SSE stream before publishing anything: the bus only fans
	// out to subscribers current at Publish time (see observe.Bus.Publish),
	// so a subscriber opened afterward would miss the very events this test
	// exists to catch a missing wire-up of. ---
	sseReq, err := http.NewRequestWithContext(ctx, "GET", baseURL+"/api/v1/events", nil)
	if err != nil {
		t.Fatalf("building SSE request: %v", err)
	}
	sseReq.Header.Set("Authorization", "Bearer "+apiToken)
	sseReq.Host = addr
	sseResp, err := http.DefaultClient.Do(sseReq) //nolint:bodyclose // closed below
	if err != nil {
		t.Fatalf("GET /api/v1/events: %v", err)
	}
	defer sseResp.Body.Close()
	if sseResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", sseResp.StatusCode)
	}
	r := bufio.NewReader(sseResp.Body)

	// --- Register the ephemeral agent the tracker should sweep, via the
	// same publish path production uses (Supervisor.SpawnAgent), so the
	// agent_spawned event and the tracker's session lookup both come from
	// real Supervisor state rather than being injected directly. ---
	err = sv.SpawnAgent(daemon.AgentSpawnSpec{
		Name:       "wiring-agent",
		ClaudeArgs: []string{"--model", "sonnet"},
		WorkDir:    t.TempDir(),
	})
	if err != nil {
		t.Fatalf("SpawnAgent: %v", err)
	}

	// --- A task-run event, injected directly into the run log to exercise
	// the daemon-side HTTP/SSE wiring (buildSnapshot, the bus) in isolation
	// from the producer side. The producer side — a `leo run` subprocess
	// reporting over IPC via daemon.ObservePublisher — is NOT exercised by
	// calling runLog.Publish directly here; that path is covered end-to-end
	// by TestTaskRunObservabilityReachesDaemonOverIPC below, which drives a
	// real daemon.Server + PublishTaskRun round-trip the way run.Run's
	// subprocess-side publisher actually does. ---
	startedAt := time.Now()
	runLog.Publish(observe.Event{
		Type: observe.EventTaskRunStarted,
		Payload: &observe.TaskRunPayload{
			Run: observe.TaskRun{
				ID:        "wiring-task-1",
				Task:      "wiring-task",
				Status:    observe.RunRunning,
				StartedAt: startedAt,
			},
		},
	})

	// --- GET /api/v1/state must reflect the run log entry and the version. ---
	req, err := http.NewRequest("GET", baseURL+"/api/v1/state", nil)
	if err != nil {
		t.Fatalf("building state request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiToken)
	req.Host = addr
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/v1/state: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var stateResp struct {
		OK   bool `json:"ok"`
		Data struct {
			LeoVersion string            `json:"leo_version"`
			RecentRuns []observe.TaskRun `json:"recent_runs"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&stateResp); err != nil {
		t.Fatalf("decoding state response: %v", err)
	}
	if !stateResp.OK {
		t.Fatal("expected ok=true")
	}
	if stateResp.Data.LeoVersion != "v-wiring-test" {
		t.Errorf("leo_version = %q, want v-wiring-test — WithVersion wasn't wired through", stateResp.Data.LeoVersion)
	}
	foundRun := false
	for _, r := range stateResp.Data.RecentRuns {
		if r.ID == "wiring-task-1" {
			foundRun = true
		}
	}
	if !foundRun {
		t.Errorf("recent_runs %+v missing wiring-task-1 — RunLog wasn't wired through (WithRunLog or SetPublisher)", stateResp.Data.RecentRuns)
	}

	// --- The SSE stream (opened before SpawnAgent, above) must carry the
	// agent_spawned event it published, plus an agent_activity event once
	// the tracker sweeps — proving the tracker was actually started and its
	// publisher reaches the bus. ---
	seenAgentSpawned := false
	seenAgentActivity := false
	deadline := time.After(10 * time.Second)
	for !seenAgentSpawned || !seenAgentActivity {
		type frame struct{ event, data string }
		frameCh := make(chan frame, 1)
		go func() {
			ev, data := readOneSSEFrame(r)
			frameCh <- frame{event: ev, data: data}
		}()
		select {
		case f := <-frameCh:
			switch f.event {
			case string(observe.EventAgentSpawned):
				if strings.Contains(f.data, `"wiring-agent"`) {
					seenAgentSpawned = true
				}
			case string(observe.EventAgentActivity):
				seenAgentActivity = true
			}
		case <-deadline:
			t.Fatalf("timed out waiting for events (agent_spawned=%v activity=%v)", seenAgentSpawned, seenAgentActivity)
		}
	}
}

// readOneSSEFrame reads one "event: X\ndata: Y\n\n" frame, skipping hello and
// heartbeat comment frames.
func readOneSSEFrame(r *bufio.Reader) (event, data string) {
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return "", ""
		}
		line = strings.TrimRight(line, "\n")
		if strings.HasPrefix(line, ": ") {
			continue // heartbeat comment
		}
		if strings.HasPrefix(line, "event: ") {
			event = strings.TrimPrefix(line, "event: ")
			continue
		}
		if strings.HasPrefix(line, "data: ") {
			data = strings.TrimPrefix(line, "data: ")
			continue
		}
		if line == "" && event != "" {
			if event == string(observe.EventHello) {
				event, data = "", ""
				continue
			}
			return event, data
		}
	}
}

// writeFakeTmuxScript writes an executable shell script standing in for the
// tmux binary: it answers `list-sessions` and `capture-pane` with fixture
// output so the activity tracker (and Supervisor.SpawnAgent's session
// create/kill calls) never shell out to a real tmux server.
func writeFakeTmuxScript(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-tmux")
	script := `#!/bin/sh
for arg in "$@"; do
  case "$arg" in
    list-sessions)
      echo "leo-wiring-agent|0|$(date +%s)"
      exit 0
      ;;
    capture-pane)
      echo "wiring test pane output"
      exit 0
      ;;
  esac
done
exit 0
`
	if err := os.WriteFile(path, []byte(script), 0755); err != nil { //nolint:gosec // test fixture, needs +x
		t.Fatalf("writing fake tmux script: %v", err)
	}
	return path
}
