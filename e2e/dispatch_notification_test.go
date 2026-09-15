//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/consult"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

func TestHeadlessDispatchCompletionNotification(t *testing.T) {
	for _, covered := range []bool{false, true} {
		name := "uncovered"
		if covered {
			name = "covered-wait"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("FAKECLAUDE_TMUX_SOCKET", fmt.Sprintf("leo-notify-e2e-%d", time.Now().UnixNano()))
			t.Cleanup(func() { _ = exec.Command(faketmux, tmux.Args("kill-server")...).Run() })
			if out, err := exec.Command(faketmux, tmux.Args("new-session", "-d", "-x", "240", "-y", "24", "-s", "caller", "sh", "-c", "printf '› Ask Codex to do anything\\n'; while IFS= read -r line; do :; done")...).CombinedOutput(); err != nil {
				t.Fatalf("caller pane: %v: %s", err, out)
			}
			pane := strings.TrimSpace(tmuxOutput(t, "display-message", "-p", "-t", "caller:", "#{pane_id}"))
			sessionID := strings.TrimSpace(tmuxOutput(t, "display-message", "-p", "-t", pane, "#{session_id}"))
			ws := mkTempE2EDir(t, "leo-notify-dispatch-*")
			cfgPath := filepath.Join(ws, "leo.yaml")
			if err := os.WriteFile(cfgPath, []byte("templates:\n  worker:\n    harness: codex\n    model: gpt-5\n    workspace: "+ws+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				t.Fatal(err)
			}
			d := consult.NewDispatcher(consult.NewFileRecorder(filepath.Join(ws, "state")))
			d.ExecCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
				return exec.CommandContext(ctx, "sh", "-c", "sleep .2; printf '%s\\n' '{\"type\":\"thread.started\",\"thread_id\":\"t\"}' '{\"type\":\"item.completed\",\"item\":{\"id\":\"i\",\"type\":\"agent_message\",\"text\":\"done\"}}' '{\"type\":\"turn.completed\",\"usage\":{}}'")
			}
			d.SetNotificationDelivery(consult.NewTmuxNotificationDelivery(faketmux, func(ctx context.Context, name string, args ...string) *exec.Cmd {
				return exec.CommandContext(ctx, name, args...)
			}))
			started, err := d.Start(context.Background(), cfg, consult.Request{Template: "worker", Prompt: "work", Cwd: ws, Name: "notify", CallerPaneID: pane, CallerSessionID: sessionID, CallerHarness: "codex"})
			if err != nil {
				t.Fatal(err)
			}
			if covered {
				entries := d.Wait(context.Background(), []string{started.ID}, 3*time.Second)
				if len(entries) != 1 || entries[0].Status != consult.StatusDone {
					t.Fatalf("wait=%+v", entries)
				}
			} else {
				for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
					if rec, err := d.Get(started.ID); err == nil && rec.Status.Terminal() {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
			d.SweepNotifications(context.Background())
			capture := tmuxOutput(t, "capture-pane", "-p", "-t", pane)
			count := strings.Count(capture, "[leo] dispatch "+started.ID+"#1 (notify) done · active 0:00 — collect with leo_wait")
			want := 1
			if covered {
				want = 0
			}
			if count != want {
				t.Fatalf("notification count=%d want=%d capture=%q", count, want, capture)
			}
		})
	}
}

func TestHeadlessDispatchUsageAppearsInWaitAndRoster(t *testing.T) {
	ws := mkTempE2EDir(t, "leo-usage-dispatch-*")
	cfgPath := filepath.Join(ws, "leo.yaml")
	if err := os.WriteFile(cfgPath, []byte("templates:\n  worker:\n    harness: claude\n    model: sonnet\n    workspace: "+ws+"\n    env:\n      FAKECLAUDE_SCENARIO: usage\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	d := consult.NewDispatcher(consult.NewFileRecorder(filepath.Join(ws, "state")))
	d.ExecCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, fakeclaude, "-p", "usage")
		return cmd
	}
	started, err := d.Start(context.Background(), cfg, consult.Request{Template: "worker", Prompt: "usage", Cwd: ws, Name: "usage"})
	if err != nil {
		t.Fatal(err)
	}
	entries := d.Wait(context.Background(), []string{started.ID}, 3*time.Second)
	if len(entries) != 1 || entries[0].InputTokens == nil || entries[0].OutputTokens == nil || entries[0].ToolCalls == nil || *entries[0].InputTokens != 125 || *entries[0].OutputTokens != 10 || *entries[0].ToolCalls != 1 {
		t.Fatalf("wait usage = %+v", entries)
	}
	rec, err := d.Get(started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := consult.RenderRoster([]consult.Record{rec}, time.Now()); !strings.Contains(got, "1 tool · 0.1k tokens") {
		t.Fatalf("roster=%q", got)
	}
}

func TestDaemonDeliversHeadlessCompletionIntoCaller(t *testing.T) {
	s := newInteractiveE2E(t)
	caller := s.spawnAgent(t, "notify-caller")
	session := agent.SessionName(caller)
	s.waitForSession(t, session)
	submitted := filepath.Join(s.ws, "submitted-notification")
	script := "printf '› Ask Codex to do anything\\n'; IFS= read -r line; printf '%s' \"$line\" > " + submitted + "; sleep 30"
	pane := strings.TrimSpace(tmuxOutput(t, "new-window", "-d", "-P", "-F", "#{pane_id}", "-t", tmux.Target(session), "-n", "notify-inbox", "sh", "-c", script))
	_ = tmuxOutput(t, "resize-window", "-x", "240", "-y", "24", "-t", pane)
	var started consult.Started
	s.request(t, http.MethodPost, "/api/dispatch", map[string]any{"template": "interactive", "prompt": "daemon notify", "cwd": s.ws, "mode": "headless", "from": caller, "caller_pane_id": pane}, &started)
	key := started.ID + "#1"
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		if s.record(t, started.ID).Notifications[key].Disposition == consult.NotificationDelivered {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	rec := s.record(t, started.ID)
	if rec.Notifications[key].Disposition != consult.NotificationDelivered {
		t.Fatalf("notification=%+v caller alive=%v capture=%q service=%s", rec.Notifications[key], s.paneAlive(pane), tmuxOutput(t, "capture-pane", "-p", "-t", pane), s.output.String())
	}
	raw, err := os.ReadFile(submitted)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != rec.Notifications[key].Message {
		t.Fatalf("submitted=%q want=%q", raw, rec.Notifications[key].Message)
	}
}

func TestDaemonSuppressesCoveredHeadlessCompletion(t *testing.T) {
	s := newInteractiveE2E(t)
	caller := s.spawnAgent(t, "wait-caller")
	session := agent.SessionName(caller)
	s.waitForSession(t, session)
	submitted := filepath.Join(s.ws, "covered-notification")
	script := "printf '› Ask Codex to do anything\\n'; IFS= read -r line; printf '%s' \"$line\" > " + submitted + "; sleep 30"
	pane := strings.TrimSpace(tmuxOutput(t, "new-window", "-d", "-P", "-F", "#{pane_id}", "-t", tmux.Target(session), "-n", "wait-inbox", "sh", "-c", script))
	var started consult.Started
	s.request(t, http.MethodPost, "/api/dispatch", map[string]any{"template": "interactive", "prompt": "covered", "cwd": s.ws, "mode": "headless", "from": caller, "caller_pane_id": pane}, &started)
	_ = s.wait(t, started.ID)
	rec := s.record(t, started.ID)
	if rec.Notifications[started.ID+"#1"].Disposition != consult.NotificationSuppressed {
		t.Fatalf("notification=%+v", rec.Notifications[started.ID+"#1"])
	}
	if _, err := os.Stat(submitted); !os.IsNotExist(err) {
		t.Fatalf("covered notification was submitted: %v", err)
	}
}

func tmuxOutput(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command(faketmux, tmux.Args(args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("tmux %v: %v: %s", args, err, out)
	}
	return string(out)
}
