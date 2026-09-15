//go:build e2e

package e2e

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/consult"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

func TestDispatchOpensViewerWindow(t *testing.T) {
	if _, err := os.Stat(faketmux); err != nil {
		t.Skip("tmux not available; skipping live dispatch viewer test")
	}
	tmuxPath := faketmux
	t.Setenv("FAKECLAUDE_TMUX_SOCKET", "leo-dispatch-viewer-e2e")
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
	d := consult.NewDispatcherWithOnStart(nil, context.Background(), v.OnStart, v.Close)
	d.ExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", `printf '%s\n' '{"type":"assistant","message":{"id":"m","usage":{"input_tokens":100},"content":[{"type":"tool_use","id":"t"}]}}' '{"type":"result","result":"done","is_error":false,"usage":{"input_tokens":100,"output_tokens":20},"num_turns":1}'`)
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
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		rec, err := d.Get(started.ID)
		if err == nil && rec.InputTokens != nil && rec.OutputTokens != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	v.UpdateRoster(d.Records(), time.Now())
	target := tmux.Target(session) + ":"
	freshSession := session + "-fresh"
	if out, err := exec.Command(tmuxPath, tmux.Args("new-session", "-d", "-s", freshSession, "sleep", "30")...).CombinedOutput(); err != nil {
		t.Fatalf("tmux fresh new-session: %v: %s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command(tmuxPath, tmux.Args("kill-session", "-t", tmux.Target(freshSession))...).Run()
	})
	freshFormat, err := exec.Command(tmuxPath, tmux.Args("display-message", "-p", "-t", tmux.Target(freshSession)+":", "#{T:status-format[0]}")...).Output()
	if err != nil || strings.TrimSpace(string(freshFormat)) == "" {
		t.Fatalf("fresh status-format[0] = %q, err %v", freshFormat, err)
	}
	roster, err := exec.Command(tmuxPath, tmux.Args("show-options", "-v", "-t", target, "@leo_roster")...).Output()
	if err != nil || !strings.Contains(string(roster), "claude") || !strings.Contains(string(roster), "1 tools · 0.1k tokens") {
		t.Fatalf("live roster = %q, err %v", roster, err)
	}
	status, err := exec.Command(tmuxPath, tmux.Args("show-options", "-v", "-t", target, "status")...).Output()
	if err != nil || strings.TrimSpace(string(status)) != "2" {
		t.Fatalf("live status = %q, err %v", status, err)
	}
	window := "claude·" + started.ID[len(started.ID)-4:]
	out, err := exec.Command(tmuxPath, tmux.Args("list-panes", "-t", tmux.Target(session)+":="+window, "-F", "#{window_name}\t#{pane_start_command}")...).Output()
	if err != nil {
		t.Fatalf("tmux list-windows before collection: %v", err)
	}
	if !regexp.MustCompile(`claude·[0-9a-f]{4}`).Match(out) {
		t.Fatalf("viewer window with label and id suffix missing from %q", out)
	}
	if !strings.Contains(string(out), "--config '/tmp/leo-e2e.yaml' dispatch watch "+started.ID) {
		t.Fatalf("viewer command missing daemon config from %q", out)
	}
	if entries := d.Wait(context.Background(), []string{started.ID}, consult.RunTimeout); len(entries) != 1 || entries[0].Status != consult.StatusDone {
		t.Fatalf("Wait = %+v", entries)
	}
	v.UpdateRoster(d.Records(), time.Now())
	format, err := exec.Command(tmuxPath, tmux.Args("display-message", "-p", "-t", target, "#{T:status-format[0]}")...).Output()
	localFormat, localErr := exec.Command(tmuxPath, tmux.Args("show-options", "-t", target, "status-format")...).Output()
	if localErr != nil || strings.TrimSpace(string(localFormat)) != "" {
		t.Fatalf("session-local status-format remained after collection: %q, err %v", localFormat, localErr)
	}
	// tmux's default status-left truncates the session name to 10 columns
	// ("[leo-dispa"), so only require a prefix of it.
	prefix := session[:8]
	if err != nil || strings.TrimSpace(string(format)) == "" || !strings.Contains(string(format), prefix) {
		t.Fatalf("cleaned status-format[0] = %q, want non-empty format containing %q, err %v", format, prefix, err)
	}
	for _, option := range []string{"@leo_roster", "@leo_roster_owned", "@leo_roster_status_owned", "status-format[1]"} {
		out, _ := exec.Command(tmuxPath, tmux.Args("show-options", "-qv", "-t", target, option)...).Output()
		if strings.TrimSpace(string(out)) != "" {
			t.Fatalf("%s remained after collection: %q", option, out)
		}
	}
	// Without -A, show-options reports only the session-local override. The
	// inherited global value may still be "on", but Leo's status=2 must be gone.
	localStatus, err := exec.Command(tmuxPath, tmux.Args("show-options", "-qv", "-t", target, "status")...).Output()
	if err != nil || strings.TrimSpace(string(localStatus)) != "" {
		t.Fatalf("session-local status remained after collection: %q, err %v", localStatus, err)
	}
}
