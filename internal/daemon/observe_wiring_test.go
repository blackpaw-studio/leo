package daemon

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/observe"
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

// TestStartWebForwardsObservabilityOptions verifies that SetObservability's
// dependencies actually reach web.New via StartWeb's extra Options — the
// specific bug class this test exists to catch is "SetObservability was
// called but StartWeb forgot to forward one of the four fields", which a
// unit test against internal/web alone (behind its own fakes) can't see.
func TestStartWebForwardsObservabilityOptions(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "leo.yaml")
	testPort := freeTCPPort(t)
	cfgYAML := fmt.Sprintf("defaults:\n  model: sonnet\nweb:\n  enabled: true\n  port: %d\n", testPort)
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0600); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "state"), 0750); err != nil {
		t.Fatalf("creating state dir: %v", err)
	}

	s := New(tmpSockPath(t, "d.sock"), cfgPath, nil)

	bus := observe.NewBus()
	runLog := observe.NewRunLog(bus, 0)
	s.SetObservability(bus, runLog, nil, nil, "v-daemon-wiring-test")

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}
	if err := s.StartWeb(cfg, nil); err != nil {
		t.Fatalf("StartWeb: %v", err)
	}
	t.Cleanup(func() { _ = s.webServer.Shutdown() })

	apiToken, err := os.ReadFile(filepath.Join(dir, "state", "api.token"))
	if err != nil {
		t.Fatalf("reading api token: %v", err)
	}
	token := strings.TrimSpace(string(apiToken))
	addr := fmt.Sprintf("127.0.0.1:%d", testPort)
	baseURL := "http://" + addr

	// Publish a run event before subscribing isn't needed for /state (RunLog
	// is polled, not streamed), but /events needs the subscription opened
	// first — see observe.Bus.Publish (no fan-out to late subscribers).
	req, err := http.NewRequest("GET", baseURL+"/api/v1/events", nil)
	if err != nil {
		t.Fatalf("building SSE request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Host = addr
	resp, err := http.DefaultClient.Do(req) //nolint:bodyclose // closed below
	if err != nil {
		t.Fatalf("GET /api/v1/events: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	runLog.Publish(observe.Event{
		Type:    observe.EventAgentStopped,
		Payload: &observe.AgentStoppedPayload{Agent: "daemon-wiring-agent"},
	})

	r := bufio.NewReader(resp.Body)
	sawAgentStopped := false
	deadline := time.After(5 * time.Second)
	for !sawAgentStopped {
		type frame struct{ event, data string }
		frameCh := make(chan frame, 1)
		go func() {
			ev, data := readEventFrame(r)
			frameCh <- frame{ev, data}
		}()
		select {
		case f := <-frameCh:
			if f.event == string(observe.EventAgentStopped) && strings.Contains(f.data, "daemon-wiring-agent") {
				sawAgentStopped = true
			}
		case <-deadline:
			t.Fatal("did not observe published event over SSE — WithEventSource wasn't forwarded by StartWeb")
		}
	}

	stateReq, err := http.NewRequest("GET", baseURL+"/api/v1/state", nil)
	if err != nil {
		t.Fatalf("building state request: %v", err)
	}
	stateReq.Header.Set("Authorization", "Bearer "+token)
	stateReq.Host = addr
	stateResp, err := http.DefaultClient.Do(stateReq)
	if err != nil {
		t.Fatalf("GET /api/v1/state: %v", err)
	}
	defer stateResp.Body.Close()

	var parsed struct {
		Data struct {
			LeoVersion string `json:"leo_version"`
		} `json:"data"`
	}
	if err := json.NewDecoder(stateResp.Body).Decode(&parsed); err != nil {
		t.Fatalf("decoding state response: %v", err)
	}
	if parsed.Data.LeoVersion != "v-daemon-wiring-test" {
		t.Errorf("leo_version = %q, want v-daemon-wiring-test — WithVersion wasn't forwarded by StartWeb", parsed.Data.LeoVersion)
	}
}

// readEventFrame reads one SSE frame, skipping heartbeat comments.
func readEventFrame(r *bufio.Reader) (event, data string) {
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return "", ""
		}
		line = strings.TrimRight(line, "\n")
		if strings.HasPrefix(line, ": ") {
			continue
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
			return event, data
		}
	}
}
