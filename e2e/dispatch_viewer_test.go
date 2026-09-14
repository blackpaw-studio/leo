//go:build e2e

package e2e

import (
	"context"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/consult"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

func TestDispatchOpensViewerWindow(t *testing.T) {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not available; skipping live dispatch viewer test")
	}
	session := "leo-dispatch-viewer-e2e"
	if hasTmuxSession(tmuxPath, session) {
		_ = exec.Command(tmuxPath, tmux.Args("kill-session", "-t", tmux.Target(session))...).Run()
	}
	if out, err := exec.Command(tmuxPath, tmux.Args("new-session", "-d", "-s", session, "sleep", "30")...).CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command(tmuxPath, tmux.Args("kill-session", "-t", tmux.Target(session))...).Run() })

	v := consult.NewViewer("/tmp/leo-e2e.yaml", func(caller string) (string, bool) { return session, caller == "viewer-e2e" })
	v.TmuxPath = tmuxPath
	d := consult.NewDispatcherWithOnStart(nil, context.Background(), v.OnStart)
	d.ExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "echo", `{"type":"result","result":"done","is_error":false}`)
	}
	cfg := &config.Config{Templates: map[string]config.TemplateConfig{
		"claude": {Harness: "claude", Model: "opus"},
	}}
	started, err := d.Start(context.Background(), cfg, consult.Request{
		Caller: "viewer-e2e", Template: "claude", Prompt: "trivial", Cwd: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if entries := d.Wait(context.Background(), []string{started.ID}, consult.RunTimeout); len(entries) != 1 || entries[0].Status != consult.StatusDone {
		t.Fatalf("Wait = %+v", entries)
	}
	window := "claude·" + started.ID[len(started.ID)-4:]
	out, err := exec.Command(tmuxPath, tmux.Args("list-panes", "-t", tmux.Target(session)+":="+window, "-F", "#{window_name}\t#{pane_start_command}")...).Output()
	if err != nil {
		t.Fatalf("tmux list-windows: %v", err)
	}
	if !regexp.MustCompile(`claude·[0-9a-f]{4}`).Match(out) {
		t.Fatalf("viewer window with label and id suffix missing from %q", out)
	}
	if !strings.Contains(string(out), "--config '/tmp/leo-e2e.yaml' dispatch watch "+started.ID) {
		t.Fatalf("viewer command missing daemon config from %q", out)
	}
}
