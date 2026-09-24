package service

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/observe"
	"github.com/blackpaw-studio/leo/internal/web"
)

// TestSurfaceFileBootWiring boots the daemon's observability the way
// defaultSupervisedExec does (wireObservability, wireDaemon, wireManager,
// StartWeb) and drives one leo_surface_file call through the real HTTP
// endpoint with the agent token. The one call must appear on the SSE stream
// and in both state projections — TCP /api/v1/state and IPC /state — as the
// same object.
func TestSurfaceFileBootWiring(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Short path: macOS caps Unix socket paths at 104 chars.
	homePath, err := os.MkdirTemp("/tmp", "leo-surf-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(homePath) })
	if err := os.MkdirAll(filepath.Join(homePath, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	port := freeTCPPort(t)
	cfgPath := filepath.Join(homePath, "leo.yaml")
	cfgYAML := fmt.Sprintf("defaults:\n  model: sonnet\nweb:\n  enabled: true\n  port: %d\n  bind: 127.0.0.1\n", port)
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	fakeTmux := writeFakeTmuxScript(t)
	sv := NewSupervisor(ctx)
	sv.tmuxPath, sv.homePath = fakeTmux, homePath
	obs := wireObservability(ctx, sv, fakeTmux)

	srv := daemon.New(daemon.SockPath(homePath), cfgPath, sv)
	obs.wireDaemon(srv, "v-surface-test")
	if err := srv.Start(); err != nil {
		t.Fatalf("daemon Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Shutdown() })
	agentMgr := agent.New(func() (*config.Config, error) { return config.Load(cfgPath) }, sv, fakeTmux, "")
	obs.wireManager(agentMgr)
	srv.SetAgentManager(agentMgr)
	if err := srv.StartWeb(cfg, agentMgr); err != nil {
		t.Fatalf("StartWeb: %v", err)
	}

	// A live supervised agent with a file in its workspace.
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "notes ü.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := agentstore.Save(homePath, agentstore.Record{Name: "surf", Workspace: workspace}); err != nil {
		t.Fatal(err)
	}
	startedAt := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	sv.mu.Lock()
	sv.states["surf"] = &ProcessState{Name: "surf", Status: "running", StartedAt: startedAt, Ephemeral: true}
	sv.mu.Unlock()

	apiToken, err := web.EnsureAPIToken(cfg.StatePath())
	if err != nil {
		t.Fatal(err)
	}
	agentToken, err := web.EnsureAgentToken(cfg.StatePath())
	if err != nil {
		t.Fatal(err)
	}
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	events := openEventStream(ctx, t, baseURL, apiToken)

	// --- The call, authenticated as an agent. ---
	body := strings.NewReader(`{"path":"notes ü.md","line":3,"reason":"read me"}`)
	resp := doJSON(t, ctx, http.MethodPost, baseURL+"/api/agent/surf/surface-file", agentToken, body)
	var created struct {
		OK   bool
		Data struct{ ID string }
	}
	decodeBody(t, resp, &created)
	if !created.OK || created.Data.ID == "" {
		t.Fatalf("surface-file response = %+v", created)
	}

	// --- SSE ---
	var fromSSE observe.FileSurfacedPayload
	if err := json.Unmarshal([]byte(events.next(t, string(observe.EventFileSurfaced))), &fromSSE); err != nil {
		t.Fatal(err)
	}
	want := fromSSE.SurfacedFile
	if want.ID != created.Data.ID || want.Type != observe.EventFileSurfaced || want.Agent != "surf" ||
		!want.StartedAt.Equal(startedAt) || want.Path != "notes ü.md" || want.AbsPath != filepath.Join(workspace, "notes ü.md") ||
		want.Line != 3 || want.Reason != "read me" || want.At.IsZero() {
		t.Fatalf("SSE payload = %+v", fromSSE)
	}

	// --- TCP /api/v1/state ---
	var tcpState struct {
		Data struct{ Agents []observe.Agent }
	}
	decodeBody(t, doJSON(t, ctx, http.MethodGet, baseURL+"/api/v1/state", apiToken, nil), &tcpState)
	assertSurfacedState(t, "TCP /api/v1/state", tcpState.Data.Agents, want)

	// --- IPC /state over the Unix socket ---
	ipc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", daemon.SockPath(homePath))
	}}}
	ipcResp, err := ipc.Get("http://leo/state")
	if err != nil {
		t.Fatalf("IPC /state: %v", err)
	}
	var ipcState struct {
		Data struct{ Agents []observe.Agent }
	}
	decodeBody(t, ipcResp, &ipcState)
	assertSurfacedState(t, "IPC /state", ipcState.Data.Agents, want)
}

func assertSurfacedState(t *testing.T, surface string, agents []observe.Agent, want observe.SurfacedFile) {
	t.Helper()
	for _, a := range agents {
		if a.Name != "surf" {
			continue
		}
		if len(a.SurfacedFiles) != 1 {
			t.Fatalf("%s surfaced_files = %+v, want exactly the one call", surface, a.SurfacedFiles)
		}
		got := a.SurfacedFiles[0]
		if got.ID != want.ID || got.Path != want.Path || got.AbsPath != want.AbsPath || got.Line != want.Line ||
			got.Reason != want.Reason || !got.At.Equal(want.At) || !got.StartedAt.Equal(want.StartedAt) || !a.StartedAt.Equal(want.StartedAt) {
			t.Fatalf("%s entry = %+v (agent started_at %v), want %+v", surface, got, a.StartedAt, want)
		}
		return
	}
	t.Fatalf("%s has no agent surf: %+v", surface, agents)
}

type eventStream struct{ r *bufio.Reader }

func openEventStream(ctx context.Context, t *testing.T, baseURL, token string) *eventStream {
	t.Helper()
	resp := doJSON(t, ctx, http.MethodGet, baseURL+"/api/v1/events", token, nil)
	t.Cleanup(func() { resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/events = %d", resp.StatusCode)
	}
	s := &eventStream{r: bufio.NewReader(resp.Body)}
	s.next(t, string(observe.EventHello)) // subscribed once hello arrives
	return s
}

// next returns the data line of the next event named name, skipping others.
func (s *eventStream) next(t *testing.T, name string) string {
	t.Helper()
	current := ""
	for {
		line, err := s.r.ReadString('\n')
		if err != nil {
			t.Fatalf("reading SSE while waiting for %s: %v", name, err)
		}
		line = strings.TrimRight(line, "\n")
		switch {
		case strings.HasPrefix(line, "event: "):
			current = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: ") && current == name:
			return strings.TrimPrefix(line, "data: ")
		}
	}
}

func doJSON(t *testing.T, ctx context.Context, method, url, token string, body io.Reader) *http.Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return resp
}

func decodeBody(t *testing.T, resp *http.Response, dst any) {
	t.Helper()
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		t.Fatalf("decoding %s: %v", raw, err)
	}
}
