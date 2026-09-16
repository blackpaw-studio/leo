//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/consult"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

func TestDispatchViewerPanePlacement(t *testing.T) {
	tmuxPath, session, caller, window, other, before := viewerPaneFixture(t)
	records := []consult.Record{}
	v := consult.NewViewer(viewerPaneConfig(t, 3), nil)
	v.TmuxPath = tmuxPath
	sleeper := viewerSleeper(t)
	v.Executable = func() (string, error) { return sleeper, nil }
	v.Records = func() []consult.Record { return append([]consult.Record(nil), records...) }
	var viewers []string
	for i := 1; i <= 3; i++ {
		rec := consult.Record{ID: fmt.Sprintf("d-%04d", i), Kind: "dispatch", Template: "worker", Cwd: t.TempDir(), CallerPaneID: caller, CallerSessionID: session, CallerWindowID: window}
		pane := v.OnStart(rec)
		if !strings.HasPrefix(pane, "%") {
			t.Fatalf("viewer %d=%q", i, pane)
		}
		rec.ViewerKind, rec.ViewerPaneID = "split", pane
		records = append(records, rec)
		v.Coordinator.Publish(rec.ID, nil)
		viewers = append(viewers, pane)
	}
	out, err := exec.Command(tmuxPath, tmux.Args("list-panes", "-t", window, "-F", "#{pane_id} #{pane_top} #{pane_left} #{pane_width}")...).Output()
	if err != nil {
		t.Fatalf("panes=%q err=%v", out, err)
	}
	widthOut, err := exec.Command(tmuxPath, tmux.Args("display-message", "-p", "-t", window, "#{window_width}")...).Output()
	if err != nil {
		t.Fatal(err)
	}
	width := strings.TrimSpace(string(widthOut))
	if !strings.Contains(string(out), caller+" 0 0 "+width) {
		t.Fatalf("caller does not span the full width at top: panes=%q width=%q", out, width)
	}
	viewerTop := -1
	lefts := map[int]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 4 || !slices.Contains(viewers, fields[0]) {
			continue
		}
		top, _ := strconv.Atoi(fields[1])
		left, _ := strconv.Atoi(fields[2])
		if top == 0 || (viewerTop >= 0 && top != viewerTop) || lefts[left] {
			t.Fatalf("viewers are not side by side beneath caller: %q", out)
		}
		viewerTop, lefts[left] = top, true
	}
	if len(lefts) != 3 {
		t.Fatalf("viewer geometry=%q", out)
	}
	after, _ := exec.Command(tmuxPath, tmux.Args("display-message", "-p", "-t", other, "#{window_layout}")...).Output()
	if string(after) != before {
		t.Fatalf("other window layout changed: before=%q after=%q", before, after)
	}
}

func TestDispatchViewerPaneCap(t *testing.T) {
	tmuxPath, session, caller, window, _, _ := viewerPaneFixture(t)
	records := []consult.Record{}
	v := consult.NewViewer(viewerPaneConfig(t, 3), func(string) (string, bool) { return session, true })
	v.TmuxPath = tmuxPath
	sleeper := viewerSleeper(t)
	v.Executable = func() (string, error) { return sleeper, nil }
	v.Records = func() []consult.Record { return append([]consult.Record(nil), records...) }
	for i := 1; i <= 4; i++ {
		rec := consult.Record{ID: fmt.Sprintf("d-%04d", i), Kind: "dispatch", Caller: "caller", Template: "worker", Cwd: t.TempDir(), CallerPaneID: caller, CallerSessionID: session, CallerWindowID: window}
		id := v.OnStart(rec)
		if i <= 3 {
			if !strings.HasPrefix(id, "%") {
				t.Fatalf("viewer %d=%q", i, id)
			}
			rec.ViewerKind, rec.ViewerPaneID = "split", id
		} else {
			if !strings.HasPrefix(id, "@") {
				t.Fatalf("fourth viewer=%q", id)
			}
			rec.ViewerKind, rec.ViewerWindowID = "window", id
		}
		records = append(records, rec)
		v.Coordinator.Publish(rec.ID, nil)
	}
	out, _ := exec.Command(tmuxPath, tmux.Args("list-panes", "-t", window, "-F", "#{pane_id}")...).Output()
	if len(strings.Fields(string(out))) != 4 {
		t.Fatalf("caller window panes=%q", out)
	}
}

func viewerPaneConfig(t *testing.T, max int) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "leo.yaml")
	if err := os.WriteFile(p, []byte(fmt.Sprintf("defaults:\n  dispatch:\n    viewer:\n      placement: pane\n      max_panes: %d\ntasks: {}\n", max)), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
func viewerSleeper(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "leo-sleep")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nsleep 60\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}
func viewerPaneFixture(t *testing.T) (string, string, string, string, string, string) {
	t.Helper()
	tmuxPath := faketmux
	t.Setenv("FAKECLAUDE_TMUX_SOCKET", fmt.Sprintf("viewer-pane-%d", time.Now().UnixNano()))
	_ = exec.Command(tmuxPath, tmux.Args("kill-server")...).Run()
	out, err := exec.Command(tmuxPath, tmux.Args("new-session", "-d", "-P", "-F", "#{pane_id} #{session_id} #{window_id}", "-s", "caller", "sleep", "60")...).Output()
	if err != nil {
		t.Fatal(err)
	}
	f := strings.Fields(string(out))
	if len(f) != 3 {
		t.Fatalf("identity=%q", out)
	}
	otherOut, err := exec.Command(tmuxPath, tmux.Args("new-window", "-d", "-P", "-F", "#{window_id}", "-t", "=caller", "sleep", "60")...).Output()
	if err != nil {
		t.Fatal(err)
	}
	other := strings.TrimSpace(string(otherOut))
	before, _ := exec.Command(tmuxPath, tmux.Args("display-message", "-p", "-t", other, "#{window_layout}")...).Output()
	t.Cleanup(func() { _ = exec.Command(tmuxPath, tmux.Args("kill-server")...).Run() })
	return tmuxPath, f[1], f[0], f[2], other, string(before)
}

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
	if err != nil || !strings.Contains(string(roster), "claude") || !strings.Contains(string(roster), "1 tool · 0.1k tokens") {
		t.Fatalf("live roster = %q, err %v", roster, err)
	}
	formatWhileRoster, err := exec.Command(tmuxPath, tmux.Args("display-message", "-p", "-t", target, "#{T:status-format[0]}")...).Output()
	prefix := session[:8]
	if err != nil || strings.TrimSpace(string(formatWhileRoster)) == "" || !strings.Contains(string(formatWhileRoster), prefix) {
		t.Fatalf("live status-format[0] = %q, want non-empty format containing %q, err %v", formatWhileRoster, prefix, err)
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
	prefix = session[:8]
	if err != nil || strings.TrimSpace(string(format)) == "" || !strings.Contains(string(format), prefix) {
		t.Fatalf("cleaned status-format[0] = %q, want non-empty format containing %q, err %v", format, prefix, err)
	}
	for _, option := range []string{"@leo_roster", "@leo_roster_owned", "@leo_roster_status_owned", "@leo_roster_format0_owned", "@leo_roster_format0_value", "status-format[1]"} {
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
