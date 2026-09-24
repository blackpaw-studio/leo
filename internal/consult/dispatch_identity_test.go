package consult

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// envCapturingExec runs a real process that records its own LEO_DISPATCH_ID,
// so the assertion covers the environment the harness actually receives
// rather than what the seam was handed.
func envCapturingExec(t *testing.T, dir string, output string) (func(context.Context, string, ...string) *exec.Cmd, func() []string) {
	t.Helper()
	var mu sync.Mutex
	n := 0
	exec := func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		mu.Lock()
		n++
		file := filepath.Join(dir, fmt.Sprintf("env-%d", n))
		mu.Unlock()
		script := `printf '%s' "${LEO_DISPATCH_ID-unset}" > "$1"; printf '%s' "$2"`
		return exec.CommandContext(ctx, "sh", "-c", script, "sh", file, output)
	}
	seen := func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := make([]string, 0, n)
		for i := 1; i <= n; i++ {
			raw, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("env-%d", i)))
			if err != nil {
				t.Fatalf("invocation %d did not record its env: %v", i, err)
			}
			out = append(out, string(raw))
		}
		return out
	}
	return exec, seen
}

func TestHeadlessDispatchSetsDispatchIDForEveryHarness(t *testing.T) {
	// A stale identity inherited by the daemon must never reach a run.
	t.Setenv("LEO_DISPATCH_ID", "d-stale-parent")
	t.Setenv("HOME", t.TempDir())
	for _, template := range []string{"claude", "codex", "opencode"} {
		t.Run(template, func(t *testing.T) {
			d := NewDispatcher(nil)
			execFn, seen := envCapturingExec(t, t.TempDir(), "")
			d.ExecCommandContext = execFn

			started, err := d.Start(context.Background(), testConfig(), Request{Template: template, Prompt: "q", Cwd: t.TempDir(), Kind: "dispatch"})
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			d.Wait(context.Background(), []string{started.ID}, 5*time.Second)

			if got := seen(); len(got) != 1 || got[0] != started.ID {
				t.Fatalf("LEO_DISPATCH_ID seen by the harness = %q, want [%s]", got, started.ID)
			}
		})
	}
}

func TestConsultSetsDispatchID(t *testing.T) {
	t.Setenv("LEO_DISPATCH_ID", "")
	d := NewDispatcher(nil)
	execFn, seen := envCapturingExec(t, t.TempDir(), `{"type":"result","result":"ok","is_error":false}`)
	d.ExecCommandContext = execFn

	if _, err := d.Consult(context.Background(), testConfig(), Request{Template: "claude", Prompt: "q", Cwd: t.TempDir()}); err != nil {
		t.Fatalf("Consult: %v", err)
	}

	if got := seen(); len(got) != 1 || !strings.HasPrefix(got[0], "d-") {
		t.Fatalf("LEO_DISPATCH_ID seen by the consultant = %q, want a dispatch id", got)
	}
}

func TestHeadlessContinuationSetsDispatchID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LEO_DISPATCH_ID", "d-stale-parent")
	d := NewDispatcher(NewFileRecorder(t.TempDir()))
	execFn, seen := envCapturingExec(t, t.TempDir(), `{"type":"result","session_id":"sid-1","result":"ok","is_error":false}`)
	d.ExecCommandContext = execFn
	cfg := testConfig()

	started, err := d.Start(context.Background(), cfg, Request{Template: "claude", Prompt: "first", Cwd: t.TempDir(), Kind: "dispatch"})
	if err != nil {
		t.Fatal(err)
	}
	d.Wait(context.Background(), []string{started.ID}, 5*time.Second)
	sent, err := d.SendWithConfig(context.Background(), cfg, started.ID, "again")
	if err != nil {
		t.Fatal(err)
	}
	d.Wait(context.Background(), []string{sent.TurnID}, 5*time.Second)

	if got := seen(); len(got) != 2 || got[0] != started.ID || got[1] != started.ID {
		t.Fatalf("LEO_DISPATCH_ID per invocation = %q, want both %s", got, started.ID)
	}
}
