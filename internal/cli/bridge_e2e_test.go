package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
	"github.com/blackpaw-studio/leo/internal/daemon"
)

// startBridgeDaemon runs a real daemon on a short /tmp home (macOS caps unix
// socket paths at 104 bytes) and returns that home and its hub.
func startBridgeDaemon(t *testing.T) (string, *bridge.Hub) {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "leo-br-*")
	if err != nil {
		t.Fatalf("temp home: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	if err := os.MkdirAll(filepath.Join(home, "state"), 0o700); err != nil {
		t.Fatalf("state dir: %v", err)
	}
	hub := bridge.New(bridge.Options{})
	srv := daemon.New(daemon.SockPath(home), filepath.Join(home, "leo.yaml"), nil, daemon.WithBridge(hub))
	if err := srv.Start(); err != nil {
		t.Fatalf("daemon Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Shutdown() })
	return home, hub
}

// The production deps (daemon socket client, flag parsing) end to end: a
// queued command reaches stdout, an ack posted by `bridge report` settles
// it, and the command exits 0 when the daemon ends the stream.
func TestBridgeAgainstRealDaemon(t *testing.T) {
	home, hub := startBridgeDaemon(t)
	deps := defaultBridgeDeps()
	deps.homeDir = func() string { return home }
	deps.parentGone = func(context.Context) <-chan struct{} { return nil }
	deps.getenv = func(string) string { return "" }

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sent := make(chan error, 1)
	go func() { sent <- hub.Send(ctx, "leo-e2e", bridge.Deliver("ship it", true)) }()

	stdout, stdoutW := io.Pipe()
	exited := make(chan error, 1)
	go func() {
		cmd := newBridgeCmdWith(deps)
		cmd.SetArgs([]string{"--agent", "leo-e2e"})
		cmd.SetOut(stdoutW)
		exited <- cmd.ExecuteContext(ctx)
		_ = stdoutW.Close()
	}()

	line, err := bufio.NewReader(stdout).ReadBytes('\n')
	if err != nil {
		t.Fatalf("reading bridge stdout: %v", err)
	}
	var cmd struct {
		ID     string `json:"id"`
		Op     string `json:"op"`
		Text   string `json:"text"`
		AsUser bool   `json:"as_user"`
	}
	if err := json.Unmarshal(line, &cmd); err != nil || cmd.Op != "deliver" || cmd.Text != "ship it" || !cmd.AsUser {
		t.Fatalf("stdout line %q (err %v), want the queued deliver", line, err)
	}

	report := newBridgeCmdWith(deps)
	report.SetArgs([]string{"report", "--agent", "leo-e2e"})
	report.SetIn(strings.NewReader(`{"type":"ack","id":"` + cmd.ID + `","ok":true}`))
	if err := report.ExecuteContext(ctx); err != nil {
		t.Fatalf("bridge report: %v", err)
	}
	if err := <-sent; err != nil {
		t.Fatalf("Send did not settle on the CLI's ack: %v", err)
	}

	hub.Close() // what daemon shutdown does
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("bridge must exit 0 when the daemon ends the stream, got %v", err)
		}
	case <-ctx.Done():
		t.Fatal("bridge did not exit after the daemon ended the stream")
	}
}

func TestBridgeReportAgainstRealDaemonRejectsBadReport(t *testing.T) {
	home, _ := startBridgeDaemon(t)
	deps := defaultBridgeDeps()
	deps.homeDir = func() string { return home }
	cmd := newBridgeCmdWith(deps)
	cmd.SetArgs([]string{"report", "--agent", "leo-e2e"})
	cmd.SetIn(strings.NewReader(`{"type":"bogus"}`))
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("a report the daemon rejects must exit non-zero with its status, got %v", err)
	}
}
