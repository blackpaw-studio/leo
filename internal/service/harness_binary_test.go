package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// TestHarnessBinaryPath pins the supervise loop's per-harness binary choice:
// claude (or the empty pre-field value on old records) keeps the supervisor's
// resolved claudePath; any other registered harness resolves its own binary.
func TestHarnessBinaryPath(t *testing.T) {
	tests := []struct {
		name        string
		harnessName string
		want        string // substring the result must contain
		wantExact   bool   // when true, result must equal want exactly
	}{
		{name: "empty means claude", harnessName: "", want: "/resolved/claude", wantExact: true},
		{name: "claude keeps claudePath", harnessName: "claude", want: "/resolved/claude", wantExact: true},
		{name: "opencode resolves its own binary", harnessName: "opencode", want: "opencode"},
		{name: "unknown harness falls back to claudePath", harnessName: "bogus", want: "/resolved/claude", wantExact: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := harnessBinaryPath(tt.harnessName, "/resolved/claude")
			if tt.wantExact {
				if got != tt.want {
					t.Fatalf("harnessBinaryPath(%q) = %q, want %q", tt.harnessName, got, tt.want)
				}
				return
			}
			if !strings.Contains(got, tt.want) {
				t.Fatalf("harnessBinaryPath(%q) = %q, want it to contain %q", tt.harnessName, got, tt.want)
			}
			if got == "/resolved/claude" {
				t.Fatalf("harnessBinaryPath(%q) returned the claude path", tt.harnessName)
			}
		})
	}
}

// TestSuperviseProcessUsesHarnessBinary locks the call site: a DriveTmux spec
// with Harness "opencode" must compose its tmux pane command around the
// opencode binary, never the supervisor's global claude path. Regression for
// a production bug (post PR #99): opencode ephemeral agents spawned panes
// running `claude serve --port …`, which exits instantly on the unknown flag
// and crash-loops — the daemon spawn leg was never exercised with a real
// binary (e2e stubs the exec seams; the real-smoke drove the driver
// directly).
func TestSuperviseProcessUsesHarnessBinary(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "tmux-args.log")
	tmuxStub := filepath.Join(dir, "tmux")
	// Log every invocation; report no live sessions (has-session exit 1) so
	// the loop never adopts, and succeed everything else.
	script := "#!/bin/sh\necho \"$@\" >> " + logPath + "\ncase \"$1\" in has-session) exit 1;; esac\nexit 0\n"
	if err := os.WriteFile(tmuxStub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	sv := NewSupervisor(ctx)
	sv.homePath = t.TempDir()

	args := []string{"serve", "--port", "60999", "--hostname", "127.0.0.1"}
	id := newProcIdentity("qwen-binary-test", args)
	spec := ProcessSpec{
		Name:       "qwen-binary-test",
		ClaudeArgs: args,
		WorkDir:    t.TempDir(),
		Harness:    "opencode",
		Kind:       harness.KindAgent,
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		superviseProcess(ctx, tmuxStub, "/fake/claude-path", spec, sv.homePath, sv, id)
	}()
	defer func() {
		cancel()
		<-done
	}()

	// Wait for the first new-session invocation to hit the stub's log.
	var logged string
	deadline := time.After(5 * time.Second)
	for {
		b, _ := os.ReadFile(logPath)
		logged = string(b)
		if strings.Contains(logged, "new-session") {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("no new-session within deadline; tmux log:\n%s", logged)
		case <-time.After(20 * time.Millisecond):
		}
	}

	if strings.Contains(logged, "/fake/claude-path") {
		t.Fatalf("pane command uses the claude path for an opencode spec:\n%s", logged)
	}
	if !strings.Contains(logged, "opencode") {
		t.Fatalf("pane command does not reference the opencode binary:\n%s", logged)
	}
}
