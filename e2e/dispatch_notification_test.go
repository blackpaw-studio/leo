//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
			d := consult.NewDispatcher(nil)
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
			count := strings.Count(capture, "[leo] dispatch "+started.ID+" (notify) done · active 0:00 — collect with leo_wait")
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

func tmuxOutput(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command(faketmux, tmux.Args(args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("tmux %v: %v: %s", args, err, out)
	}
	return string(out)
}
