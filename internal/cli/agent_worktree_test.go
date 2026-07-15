package cli

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
)

// startStubDaemonSocket spins up a real unix-socket HTTP server at
// <home>/state/leo.sock — the same path daemon.Send() dials — so local
// dispatch tests can capture the JSON request body without needing a full
// daemon.Server. The handler always answers 200 with the given response.
func startStubDaemonSocket(t *testing.T, home string, handler func(w http.ResponseWriter, r *http.Request)) {
	t.Helper()
	stateDir := filepath.Join(home, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}
	sockPath := filepath.Join(stateDir, "leo.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(handler)}
	go srv.Serve(ln) //nolint:errcheck
	t.Cleanup(func() {
		srv.Close()
	})
}

// newAgentWorktreeTestConfig writes a hostless config (so dispatch resolves
// Localhost) rooted at a fresh temp home.
func newAgentWorktreeTestConfig(t *testing.T) (string, string) {
	t.Helper()
	// Use a short-prefixed temp dir directly under the system temp root
	// (rather than t.TempDir(), which nests under a long test-name path) —
	// the unix socket path built from it (home/state/leo.sock) must stay
	// under the platform's sun_path limit (~104 bytes on macOS).
	home, err := os.MkdirTemp("", "leo-wt-*")
	if err != nil {
		t.Fatalf("mkdirtemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	cfg := &config.Config{
		HomePath: home,
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 10},
	}
	path := home + "/leo.yaml"
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return path, home
}

func TestAgentWorktreeLocalDispatchSendsRequest(t *testing.T) {
	path, home := newAgentWorktreeTestConfig(t)
	out, _ := withStubStdio(t)

	var gotBody []byte
	startStubDaemonSocket(t, home, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agents/spawn" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		var buf bytes.Buffer
		buf.ReadFrom(r.Body) //nolint:errcheck
		gotBody = buf.Bytes()

		rec := agent.Record{Name: "chronicle-a11y", Branch: "a11y", Workspace: "/tmp/chronicle/.worktrees/chronicle/a11y"}
		data, _ := json.Marshal(rec)
		resp := daemon.Response{OK: true, Data: data}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
	})

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "worktree", "chronicle", "a11y", "--base", "main", "--template", "foo", "--env", "K=V"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	var req daemon.AgentSpawnRequest
	if err := json.Unmarshal(gotBody, &req); err != nil {
		t.Fatalf("decoding sent request: %v\nbody: %s", err, gotBody)
	}
	if req.FromAgent != "chronicle" {
		t.Errorf("FromAgent = %q, want chronicle", req.FromAgent)
	}
	if req.Branch != "a11y" {
		t.Errorf("Branch = %q, want a11y", req.Branch)
	}
	if req.Base != "main" {
		t.Errorf("Base = %q, want main", req.Base)
	}
	if req.Template != "foo" {
		t.Errorf("Template = %q, want foo", req.Template)
	}
	if req.Env["K"] != "V" {
		t.Errorf("Env[K] = %q, want V", req.Env["K"])
	}

	got := out.String()
	if !strings.Contains(got, "spawned chronicle-a11y") || !strings.Contains(got, "branch: a11y") {
		t.Errorf("stdout = %q, want spawned+branch message", got)
	}
	if !strings.Contains(got, "attach with: leo agent attach chronicle-a11y") {
		t.Errorf("stdout = %q, want attach hint", got)
	}
}

func TestAgentWorktreeJSONOutput(t *testing.T) {
	path, home := newAgentWorktreeTestConfig(t)
	out, _ := withStubStdio(t)

	startStubDaemonSocket(t, home, func(w http.ResponseWriter, r *http.Request) {
		rec := agent.Record{Name: "chronicle-a11y", Branch: "a11y", Workspace: "/tmp/ws"}
		data, _ := json.Marshal(rec)
		resp := daemon.Response{OK: true, Data: data}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
	})

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "worktree", "chronicle", "a11y", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	var got agent.Record
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output is not JSON record: %v\noutput: %s", err, out.String())
	}
	if got.Name != "chronicle-a11y" || got.Branch != "a11y" {
		t.Errorf("unexpected decoded record: %+v", got)
	}
	// Confirm it's indented (pretty-printed), not compact.
	if !strings.Contains(out.String(), "\n  ") {
		t.Errorf("expected indented JSON output, got: %s", out.String())
	}
}

func TestAgentWorktreeRemoteDispatchForwardsArgs(t *testing.T) {
	path := newAgentCLITestConfig(t)
	stub := withStubExec(t)
	withStubStdio(t)

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "worktree", "chronicle", "a11y", "--base", "main", "--template", "foo", "--env", "K=V"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("expected 1 ssh call, got %d: %v", len(stub.calls), stub.calls)
	}
	joined := strings.Join(stub.calls[0], " ")
	for _, want := range []string{"agent", "worktree", "chronicle", "a11y", "--base", "main", "--template", "foo", "--env", "K=V"} {
		if !strings.Contains(joined, want) {
			t.Errorf("ssh call missing %q: %s", want, joined)
		}
	}
}

func TestAgentWorktreeRequiresTwoArgs(t *testing.T) {
	cmd := newAgentWorktreeCmd()
	if err := cmd.Args(cmd, []string{"only-one"}); err == nil {
		t.Error("expected error for 1 arg, got nil")
	}
	if err := cmd.Args(cmd, []string{}); err == nil {
		t.Error("expected error for 0 args, got nil")
	}
	if err := cmd.Args(cmd, []string{"a", "b", "c"}); err == nil {
		t.Error("expected error for 3 args, got nil")
	}
	if err := cmd.Args(cmd, []string{"chronicle", "a11y"}); err != nil {
		t.Errorf("expected no error for 2 args, got %v", err)
	}
}
