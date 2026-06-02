//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestPersistentProcessSession exercises Topology C: a task references a
// supervised process via session: process:<name>. The injected prompt
// should land in the process's session (not the task's), proving the
// dispatch picks the process's session name rather than the task's.
//
// We don't actually start a process supervisor here — only the routing
// half is under test. The injector accepts the prompt regardless of
// whether tmux is attached on the other side.
func TestPersistentProcessSession(t *testing.T) {
	dir := mkTempE2EDir(t, "leo-e2e-persist-proc-*")

	cfgYAML := fmt.Sprintf(`defaults:
  model: sonnet
  max_turns: 15
processes:
  assistant:
    workspace: %s
    enabled: true
    channels:
      - plugin:telegram@claude-plugins-official
tasks:
  nudge:
    workspace: %s
    schedule: "0 12 * * *"
    prompt_file: prompts/NUDGE.md
    runtime: persistent
    session: process:assistant
    channels:
      - plugin:telegram@claude-plugins-official
    enabled: true
`, dir, dir)

	cfgPath := filepath.Join(dir, "leo.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o600); err != nil {
		t.Fatalf("writing leo.yaml: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		t.Fatalf("mkdir prompts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompts/NUDGE.md"), []byte("Send a friendly nudge.\n"), 0o644); err != nil {
		t.Fatalf("writing prompt: %v", err)
	}

	srv := startDaemon(t, dir, cfgPath)
	cap := &promptCapture{}
	installAutoResponder(t, srv, dir, cap)

	_, _, code := runLeo(t, dir, nil, "run", "nudge", "-c", cfgPath)
	if code != 0 {
		t.Fatalf("nudge run failed: %d", code)
	}

	rows := cap.snapshot()
	if len(rows) != 1 {
		t.Fatalf("expected 1 injection, got %d", len(rows))
	}
	// Topology C routes into a supervised process, whose tmux session is
	// "leo-<process>" (agent.SessionName) — not "leo-session-*" and not the
	// bare process name.
	if got, want := rows[0].Session, "leo-assistant"; got != want {
		t.Errorf("injected session = %q, want %q (process tmux name, not the task name)", got, want)
	}
	if !strings.Contains(rows[0].Prompt, "Send a friendly nudge.") {
		t.Errorf("prompt missing body: %q", rows[0].Prompt)
	}

	// Session id should be persisted under the process's session name.
	got := pollStoredSessionID(t, dir, "assistant", 3*time.Second)
	if want := "csid-leo-assistant"; got != want {
		t.Errorf("stored session id = %q, want %q", got, want)
	}
}
