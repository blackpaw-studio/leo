package cli

import (
	"bytes"
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

	"github.com/blackpaw-studio/leo/internal/consult"
)

// runDispatchCapturingBody runs `leo dispatch run` against a fake daemon and
// returns the JSON body it posted to /api/dispatch.
func runDispatchCapturingBody(t *testing.T, args ...string) map[string]any {
	t.Helper()
	body, err := runDispatchCommand(t, true, args...)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// runDispatchCommand runs `leo dispatch run --role explore look` (with
// --cwd <home> when withCwd) plus args against a fake daemon, returning the
// posted body (nil when nothing was posted) and the command's error.
func runDispatchCommand(t *testing.T, withCwd bool, args ...string) (map[string]any, error) {
	t.Helper()
	var postBody []byte
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/dispatch":
			postBody, _ = io.ReadAll(r.Body)
			_, _ = w.Write([]byte(`{"ok":true,"data":{"id":"d-x","mode":"interactive"}}`))
		case "/api/dispatch/wait":
			_, _ = w.Write([]byte(`{"ok":true,"data":[{"id":"d-x","status":"idle","outcome":"finished","text":"ok"}]}`))
		}
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "state", "api.token"), []byte("tok"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(home, "leo.yaml")
	if err := os.WriteFile(configPath, []byte(fmt.Sprintf("web:\n  port: %d\ntasks: {}\n", listener.Addr().(*net.TCPAddr).Port)), 0o600); err != nil {
		t.Fatal(err)
	}
	oldCfg, oldOut := cfgFile, consultStdout
	cfgFile, consultStdout = configPath, io.Discard
	t.Cleanup(func() { cfgFile, consultStdout = oldCfg, oldOut })
	cmd := newDispatchRunCmd()
	base := []string{"--role", "explore", "look"}
	if withCwd {
		base = append(base, "--cwd", home)
	}
	cmd.SetArgs(append(base, args...))
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); err != nil {
		return nil, err
	}
	var body map[string]any
	if err := json.Unmarshal(postBody, &body); err != nil {
		t.Fatalf("body %q: %v", postBody, err)
	}
	return body, nil
}

func TestDispatchRunSendsAdditionalMCPServers(t *testing.T) {
	t.Run("repeatable and not comma-split", func(t *testing.T) {
		body := runDispatchCapturingBody(t, "--additional-mcp-server", "chrome", "--additional-mcp-server", "other")
		got, _ := json.Marshal(body["additional_mcp_servers"])
		if string(got) != `["chrome","other"]` {
			t.Fatalf("additional_mcp_servers = %s", got)
		}
	})
	t.Run("comma stays in the name", func(t *testing.T) {
		body := runDispatchCapturingBody(t, "--additional-mcp-server", "a,b")
		got, _ := json.Marshal(body["additional_mcp_servers"])
		if string(got) != `["a,b"]` {
			t.Fatalf("additional_mcp_servers = %s", got)
		}
	})
	t.Run("omitted is absent", func(t *testing.T) {
		body := runDispatchCapturingBody(t)
		if _, ok := body["additional_mcp_servers"]; ok {
			t.Fatalf("omitted flag must not be sent: %v", body)
		}
	})
}

func TestWatchHeaderShowsAdditionalMCPServers(t *testing.T) {
	state := t.TempDir()
	dir := filepath.Join(state, "dispatches")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	record := consult.Record{ID: "d-addl", Template: "coding", Harness: "claude", Model: "opus", Mode: consult.ModeInteractive, Status: consult.StatusClosed, StartedAt: time.Now().Add(-time.Second), EndedAt: time.Now(), AdditionalMCPServers: []string{"chrome", "other"}, Warnings: []string{`mcp_servers: "other" skipped`}}
	writeTestRecord(t, dir, record)
	line := `{"t":0,"d":{"type":"status","seq":1,"data":"closed"}}` + "\n"
	if err := os.WriteFile(consult.StreamPath(state, record.ID), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer

	if err := watchConsult(t.Context(), state, record.ID, &out); err != nil {
		t.Fatal(err)
	}

	want := "additional MCP servers: chrome, other\nwarning: "
	if !strings.Contains(out.String(), want) {
		t.Fatalf("watch output lacks the server list before the warnings: %s", out.String())
	}
}
