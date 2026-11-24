//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/consult"
	"github.com/blackpaw-studio/leo/internal/web"
)

// A granted target_agent dispatch through the real daemon runs the fake
// claude in the target's workspace with the read-only sandbox in its argv;
// an ungranted caller is refused with a 400.
func TestDispatchTargetAgentRunsSandboxed(t *testing.T) {
	ws := mkTempE2EDir(t, "leo-dispatch-target-*")
	port := freeTCPPort(t)
	argLog := filepath.Join(ws, "dispatch-argv.json")
	targetWS := filepath.Join(ws, "agents", "leo")
	if err := os.MkdirAll(targetWS, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(ws, "leo.yaml")
	cfgText := fmt.Sprintf(`web:
  enabled: true
  bind: 127.0.0.1
  port: %d
templates:
  scout:
    harness: claude
    model: sonnet
    workspace: %s
    env:
      HOME: %s
      FAKECLAUDE_ARGLOG: %s
      FAKECLAUDE_SCENARIO: usage
  leo:
    harness: claude
    share:
      readable_by: [template:marketing]
  marketing:
    harness: claude
delegation:
  active_profile: p
  profiles:
    p:
      roles:
        explore:
          template: scout
`, port, ws, filepath.Join(ws, "home"), argLog)
	if err := os.WriteFile(cfgPath, []byte(cfgText), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range []agentstore.Record{
		{Name: "leo", Template: "leo", Workspace: targetWS},
		{Name: "mk", Template: "marketing", Workspace: filepath.Join(ws, "agents", "mk")},
		{Name: "stranger", Template: "scout", Workspace: filepath.Join(ws, "agents", "stranger")},
	} {
		if err := agentstore.Save(cfg.HomePath, rec); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", filepath.Dir(fakeclaude)+":"+os.Getenv("PATH"))

	srv := startDaemon(t, ws, cfgPath)
	daemonCtx, cancelDaemon := context.WithCancel(context.Background())
	t.Cleanup(cancelDaemon)
	srv.SetParentContext(daemonCtx)
	mgr := agent.New(func() (*config.Config, error) { return config.Load(cfgPath) }, &profileCaptureSupervisor{}, "tmux-unused", "")
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
	profileAPI(t, client, token, port, http.MethodPost, "/api/dispatch", map[string]string{"from": "mk", "role": "explore", "prompt": "read", "target_agent": "leo", "mode": "headless"}, &started)
	if started.Cwd != targetWS {
		t.Fatalf("cwd = %q, want the target workspace %q", started.Cwd, targetWS)
	}
	var waited []consult.Entry
	profileAPI(t, client, token, port, http.MethodGet, "/api/dispatch/wait?id="+started.ID+"&timeout=3", nil, &waited)
	if len(waited) != 1 || waited[0].Status != consult.StatusDone {
		t.Fatalf("dispatch result = %+v", waited)
	}
	var argv []string
	if raw, err := os.ReadFile(argLog); err != nil {
		t.Fatal(err)
	} else if err := json.Unmarshal(raw, &argv); err != nil {
		t.Fatal(err)
	}
	assertSandboxedArgv(t, argv, targetWS)

	body, _ := json.Marshal(map[string]string{"from": "stranger", "role": "explore", "prompt": "read", "target_agent": "leo", "mode": "headless"})
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/api/dispatch", port), bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("ungranted dispatch: %s, want 400", resp.Status)
	}
}

func assertSandboxedArgv(t *testing.T, argv []string, ws string) {
	t.Helper()
	if !slices.Contains(argv, "--setting-sources") {
		t.Fatalf("argv lacks --setting-sources: %q", argv)
	}
	settings := 0
	for i, a := range argv {
		if a != "--settings" || i+1 >= len(argv) {
			continue
		}
		settings++
		var s struct {
			Sandbox struct {
				Enabled                  bool `json:"enabled"`
				AllowUnsandboxedCommands bool `json:"allowUnsandboxedCommands"`
				Filesystem               struct {
					DenyWrite []string `json:"denyWrite"`
					DenyRead  []string `json:"denyRead"`
				} `json:"filesystem"`
			} `json:"sandbox"`
		}
		if err := json.Unmarshal([]byte(argv[i+1]), &s); err != nil {
			t.Fatalf("--settings %q: %v", argv[i+1], err)
		}
		if !s.Sandbox.Enabled || s.Sandbox.AllowUnsandboxedCommands || !slices.Contains(s.Sandbox.Filesystem.DenyWrite, ws) || !slices.Contains(s.Sandbox.Filesystem.DenyRead, ws+"/.env") {
			t.Fatalf("sandbox settings = %+v", s.Sandbox)
		}
	}
	if settings != 1 {
		t.Fatalf("argv carries %d --settings, want 1: %q", settings, argv)
	}
}
