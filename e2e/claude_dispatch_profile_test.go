//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/consult"
	"github.com/blackpaw-studio/leo/internal/web"
)

func TestClaudeDispatchProfile(t *testing.T) {
	ws := mkTempE2EDir(t, "leo-dispatch-profile-*")
	port := freeTCPPort(t)
	argLog := filepath.Join(ws, "dispatch-argv.json")
	home := filepath.Join(ws, "home")
	registry := filepath.Join(home, ".claude", "plugins", "installed_plugins.json")
	if err := os.MkdirAll(filepath.Dir(registry), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registry, []byte(`{"plugins":{"b@market":{},"a@local":{}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(ws, "leo.yaml")
	cfgText := fmt.Sprintf("web:\n  enabled: true\n  bind: 127.0.0.1\n  port: %d\ntemplates:\n  worker:\n    harness: claude\n    model: sonnet\n    workspace: %s\n    env:\n      HOME: %s\n      FAKECLAUDE_ARGLOG: %s\n      FAKECLAUDE_SCENARIO: usage\n", port, ws, home, argLog)
	if err := os.WriteFile(cfgPath, []byte(cfgText), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(fakeclaude)+":"+os.Getenv("PATH"))

	srv := startDaemon(t, ws, cfgPath)
	// Registered after startDaemon, so it runs before Shutdown: nothing this
	// daemon starts may outlive the test and touch later tests' tmux sockets.
	daemonCtx, cancelDaemon := context.WithCancel(context.Background())
	t.Cleanup(cancelDaemon)
	srv.SetParentContext(daemonCtx)
	sup := &profileCaptureSupervisor{}
	mgr := agent.New(func() (*config.Config, error) { return config.Load(cfgPath) }, sup, "tmux-unused", "")
	srv.SetAgentManager(mgr)
	if err := srv.StartWeb(cfg, mgr); err != nil {
		t.Fatal(err)
	}
	tokenBytes, err := os.ReadFile(web.APITokenPath(cfg.StatePath()))
	if err != nil {
		t.Fatal(err)
	}
	token := string(bytes.TrimSpace(tokenBytes))
	client := &http.Client{Timeout: 3 * time.Second}

	var started consult.Started
	profileAPI(t, client, token, port, http.MethodPost, "/api/dispatch", map[string]string{"template": "worker", "prompt": "work", "cwd": ws}, &started)
	var waited []consult.Entry
	profileAPI(t, client, token, port, http.MethodGet, "/api/dispatch/wait?id="+started.ID+"&timeout=3", nil, &waited)
	if len(waited) != 1 || waited[0].Status != consult.StatusDone {
		t.Fatalf("dispatch result = %+v", waited)
	}
	var dispatchArgs []string
	if raw, err := os.ReadFile(argLog); err != nil {
		t.Fatal(err)
	} else if err := json.Unmarshal(raw, &dispatchArgs); err != nil {
		t.Fatal(err)
	}
	wantPrompt := "You are a subagent dispatched by an orchestrator. Do not spawn agents or run your own code review; the orchestrator reviews your work. When the orchestrator has finished with you, it releases this pane. work"
	wantDispatch := []string{"-p", wantPrompt, "--model", "sonnet", "--max-turns", "15", "--output-format", "stream-json", "--verbose", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{"leo":{"command":"leo","args":["mcp-server"]}}}`, "--add-dir", ws, "--disallowed-tools", "Agent", "--settings", `{"enabledPlugins":{"a@local":false,"b@market":false}}`}
	if !reflect.DeepEqual(structuralArgv(t, dispatchArgs), structuralArgv(t, wantDispatch)) {
		t.Fatalf("dispatch argv\n got: %#v\nwant: %#v", dispatchArgs, wantDispatch)
	}

	var spawned agent.Record
	profileAPI(t, client, token, port, http.MethodPost, "/api/agent/spawn", map[string]string{"template": "worker", "name": "ephemeral"}, &spawned)
	args := sup.spawn.ClaudeArgs
	if slices.Contains(args, "--strict-mcp-config") {
		t.Fatalf("agent argv contains strict profile: %#v", args)
	}
	for i, arg := range args {
		if arg == "--mcp-config" && i+1 < len(args) && json.Valid([]byte(args[i+1])) {
			t.Fatalf("agent argv contains inline MCP config: %#v", args)
		}
		if arg == "--settings" && i+1 < len(args) {
			var settings map[string]any
			if err := json.Unmarshal([]byte(args[i+1]), &settings); err != nil {
				t.Fatal(err)
			}
			if _, ok := settings["enabledPlugins"]; ok {
				t.Fatalf("agent settings disable plugins: %#v", settings)
			}
		}
	}
}

func structuralArgv(t *testing.T, args []string) []any {
	t.Helper()
	out := make([]any, len(args))
	for i, arg := range args {
		out[i] = arg
		if i > 0 && (args[i-1] == "--mcp-config" || args[i-1] == "--settings") && json.Valid([]byte(arg)) {
			var decoded any
			if err := json.Unmarshal([]byte(arg), &decoded); err != nil {
				t.Fatal(err)
			}
			out[i] = decoded
		}
	}
	return out
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func profileAPI(t *testing.T, client *http.Client, token string, port int, method, path string, body any, out any) {
	t.Helper()
	var data []byte
	if body != nil {
		data, _ = json.Marshal(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, fmt.Sprintf("http://127.0.0.1:%d%s", port, path), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		t.Fatalf("%s %s: %s", method, path, resp.Status)
	}
	if out != nil {
		var envelope struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			t.Fatal(err)
		}
	}
}

type profileCaptureSupervisor struct{ spawn agent.SpawnRequest }

func (s *profileCaptureSupervisor) ReserveAgent(string) error { return nil }
func (s *profileCaptureSupervisor) ReleaseAgent(string)       {}
func (s *profileCaptureSupervisor) SpawnAgent(req agent.SpawnRequest) error {
	s.spawn = req
	return nil
}
func (s *profileCaptureSupervisor) StopAgent(string, bool) error                   { return nil }
func (s *profileCaptureSupervisor) RenameAgent(string, string) error               { return nil }
func (s *profileCaptureSupervisor) EphemeralAgents() map[string]agent.ProcessState { return nil }

var _ agent.Supervisor = (*profileCaptureSupervisor)(nil)
