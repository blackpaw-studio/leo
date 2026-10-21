package daemon

import (
	"bufio"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/observe"
)

// A client holding /events open must not hold Shutdown open: the stream ends
// as soon as shutdown begins, so nothing in the daemon's exit sequence waits on
// it (the agent watchers run in that window).
func TestShutdownEndsOpenEventStreamPromptly(t *testing.T) {
	sockPath := tmpSockPath(t, "s.sock")
	s := New(sockPath, "/nonexistent/leo.yaml", nil)
	s.SetObservability(observe.NewBus(), nil, nil, nil, "test")
	if err := s.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	client := socketHTTPClient(sockPath)
	client.Timeout = 0 // the stream is long-lived by design
	resp, err := client.Get("http://localhost/events")
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /events status = %d, want 200", resp.StatusCode)
	}
	body := bufio.NewReader(resp.Body)
	if line, err := body.ReadString('\n'); err != nil || !strings.HasPrefix(line, "event: hello") {
		t.Fatalf("first frame = %q, %v; want the hello event", line, err)
	}

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- s.Shutdown() }()

	streamEnded := make(chan struct{})
	go func() {
		defer close(streamEnded)
		for {
			if _, err := body.ReadString('\n'); err != nil {
				return
			}
		}
	}()

	select {
	case <-streamEnded:
	case <-time.After(2 * time.Second):
		t.Fatal("event stream still open 2s into shutdown")
	}
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("Shutdown() = %v, want nil (no stream left to wait out)", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown did not return after the stream ended")
	}
}
