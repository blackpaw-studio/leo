//go:build e2e

package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/consult"
)

func TestHeadlessDispatchContinuationEndToEnd(t *testing.T) {
	ws := mkTempE2EDir(t, "leo-continuation-*")
	cfgPath := filepath.Join(ws, "leo.yaml")
	if err := os.WriteFile(cfgPath, []byte("templates:\n  worker:\n    harness: claude\n    model: sonnet\n    workspace: "+ws+"\n    env:\n      FAKECLAUDE_SCENARIO: usage\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	d := consult.NewDispatcher(consult.NewFileRecorder(filepath.Join(ws, "state")))
	d.ExecCommandContext = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, fakeclaude, args...)
	}
	started, err := d.Start(context.Background(), cfg, consult.Request{Template: "worker", Prompt: "first", Cwd: ws, Name: "continuation"})
	if err != nil {
		t.Fatal(err)
	}
	if got := d.Wait(context.Background(), []string{started.ID}, 3*time.Second)[0]; got.Status != consult.StatusDone || got.TurnID != started.ID+"#1" {
		t.Fatalf("first=%+v", got)
	}
	sent, err := d.SendWithConfig(context.Background(), cfg, started.ID, "follow-up")
	if err != nil {
		t.Fatal(err)
	}
	if sent.TurnID != started.ID+"#2" {
		t.Fatalf("sent=%+v", sent)
	}
	if got := d.Wait(context.Background(), []string{sent.TurnID}, 3*time.Second)[0]; got.Status != consult.StatusDone {
		t.Fatalf("second=%+v", got)
	}
	rec, err := d.Get(started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Turns) != 2 || len(rec.UsageInvocations) != 2 || rec.InputTokens == nil || *rec.InputTokens != 250 {
		t.Fatalf("record=%+v", rec)
	}
	if len(rec.Notifications) != 2 {
		t.Fatalf("notifications=%+v", rec.Notifications)
	}
	out, err := consult.ReadOutput(filepath.Join(ws, "state"), started.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(strings.Join(out.Lines, "\n"), "usage done") != 2 {
		t.Fatalf("output=%q", out.Lines)
	}
}
