//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/history"
)

// TestPersistentRuntimeHappyPath drives the persistent runner end-to-end
// against a real daemon (with a fake injector standing in for tmux) and
// asserts: (1) leo run exits 0, (2) the injected prompt carries the leo
// marker plus task body, (3) history is recorded as success, and (4) the
// session id derived from the auto-responder is persisted under the
// session name for next-run resume.
func TestPersistentRuntimeHappyPath(t *testing.T) {
	dir := mkTempE2EDir(t, "leo-e2e-persist-basic-*")

	cfgYAML := fmt.Sprintf(`defaults:
  model: sonnet
  max_turns: 15
tasks:
  daily:
    workspace: %s
    schedule: "0 9 * * *"
    prompt_file: prompts/DAILY.md
    runtime: persistent
    enabled: true
`, dir)

	cfgPath := filepath.Join(dir, "leo.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o600); err != nil {
		t.Fatalf("writing leo.yaml: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		t.Fatalf("mkdir prompts: %v", err)
	}
	const promptBody = "Run the daily report."
	if err := os.WriteFile(filepath.Join(dir, "prompts/DAILY.md"), []byte(promptBody+"\n"), 0o644); err != nil {
		t.Fatalf("writing prompt: %v", err)
	}

	srv := startDaemon(t, dir, cfgPath)
	cap := &promptCapture{}
	installAutoResponder(t, srv, dir, cap)

	stdout, stderr, code := runLeo(t, dir, nil, "run", "daily", "-c", cfgPath)
	if code != 0 {
		t.Fatalf("leo run exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	rows := cap.snapshot()
	if len(rows) != 1 {
		t.Fatalf("expected exactly 1 injected prompt, got %d", len(rows))
	}
	row := rows[0]
	// Topology A's implicit session is supervised as "leo-session-<task>";
	// the injector must target that, not the bare task name.
	if row.Session != "leo-session-daily" {
		t.Errorf("injected session = %q, want %q (implicit Topology A)", row.Session, "leo-session-daily")
	}
	if row.InvID == "" {
		t.Error("injected prompt missing leo:invocation marker")
	}
	if !strings.Contains(row.Prompt, promptBody) {
		t.Errorf("injected prompt missing body %q: %q", promptBody, row.Prompt)
	}

	entry := pollHistoryEntry(t, dir, "daily", 0, 3*time.Second)
	if entry.Reason != history.ReasonSuccess {
		t.Errorf("history reason = %q, want %q", entry.Reason, history.ReasonSuccess)
	}

	got := pollStoredSessionID(t, dir, "daily", 3*time.Second)
	if want := "csid-leo-session-daily"; got != want {
		t.Errorf("stored session id = %q, want %q", got, want)
	}
}

// TestPersistentRuntimeFailureNotify confirms that when the daemon times
// out on a pending invocation, the runner records a failure and fires a
// follow-up notice into the same session (no claude -p anywhere).
func TestPersistentRuntimeFailureNotify(t *testing.T) {
	dir := mkTempE2EDir(t, "leo-e2e-persist-fail-*")

	cfgYAML := fmt.Sprintf(`defaults:
  model: sonnet
  max_turns: 15
tasks:
  flaky:
    workspace: %s
    schedule: "0 9 * * *"
    prompt_file: prompts/FLAKY.md
    runtime: persistent
    notify_on_fail: true
    channels:
      - plugin:telegram@claude-plugins-official
    timeout: 1s
    enabled: true
`, dir)

	cfgPath := filepath.Join(dir, "leo.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o600); err != nil {
		t.Fatalf("writing leo.yaml: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		t.Fatalf("mkdir prompts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompts/FLAKY.md"), []byte("Do the thing.\n"), 0o644); err != nil {
		t.Fatalf("writing prompt: %v", err)
	}

	srv := startDaemon(t, dir, cfgPath)
	cap := &promptCapture{}

	// Injector records every prompt but never reports — every invocation
	// will time out via the router's pump deadline. This drives both
	// the original failure path AND lets us assert the follow-up notice
	// reached the same session.
	srv.SetInjector(func(ctx context.Context, session, prompt string) (*harness.Result, error) {
		cap.record(session, prompt)
		return nil, nil
	})
	srv.SetAborter(func(session string) error { return nil })

	_, _, code := runLeo(t, dir, nil, "run", "flaky", "-c", cfgPath)
	if code == 0 {
		t.Fatalf("expected non-zero exit on failure path, got 0")
	}

	// History should reflect the failure.
	entry := pollHistoryEntry(t, dir, "flaky", 1, 5*time.Second)
	if entry.Reason == "" {
		t.Error("expected failure reason on history entry")
	}

	// Wait briefly for the follow-up notice to be injected. notify is
	// fire-and-forget, so we don't block the runner on it.
	deadline := time.Now().Add(3 * time.Second)
	for cap.len() < 2 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	rows := cap.snapshot()
	if len(rows) < 2 {
		t.Fatalf("expected a follow-up notice injection; got %d row(s)", len(rows))
	}
	notice := rows[1]
	if !strings.Contains(strings.ToLower(notice.Prompt), "failed") {
		t.Errorf("follow-up notice should mention failure: %q", notice.Prompt)
	}
	if notice.Session != "leo-session-flaky" {
		t.Errorf("follow-up should target original session %q, got %q", "leo-session-flaky", notice.Session)
	}
	if notice.InvID == "" {
		t.Error("follow-up notice should carry its own invocation marker")
	}
}

// TestPersistentRuntimeNonClaudeSyncCompletion drives a codex persistent
// task end-to-end against a real daemon whose injector stands in for the
// codex driver's synchronous DriveTurns completion (a non-nil *harness.Result
// returned directly from the injector, no async Report round-trip). It
// asserts: (1) the enqueued prompt is bare — no leo:invocation marker, since
// a synchronous driver has nothing to correlate a later Stop-hook callback
// against — (2) leo run exits 0, and (3) the driver's returned SessionID is
// persisted under the session name, mirroring the claude Report path's
// session-id persistence.
func TestPersistentRuntimeNonClaudeSyncCompletion(t *testing.T) {
	dir := mkTempE2EDir(t, "leo-e2e-persist-nonclaude-*")

	cfgYAML := fmt.Sprintf(`defaults:
  max_turns: 15
tasks:
  nightly:
    workspace: %s
    schedule: "0 9 * * *"
    prompt_file: prompts/NIGHTLY.md
    runtime: persistent
    harness: codex
    enabled: true
`, dir)

	cfgPath := filepath.Join(dir, "leo.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o600); err != nil {
		t.Fatalf("writing leo.yaml: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		t.Fatalf("mkdir prompts: %v", err)
	}
	const promptBody = "Run the nightly build."
	if err := os.WriteFile(filepath.Join(dir, "prompts/NIGHTLY.md"), []byte(promptBody+"\n"), 0o644); err != nil {
		t.Fatalf("writing prompt: %v", err)
	}

	srv := startDaemon(t, dir, cfgPath)
	cap := &promptCapture{}
	srv.SetInjector(func(ctx context.Context, session, prompt string) (*harness.Result, error) {
		cap.record(session, prompt)
		return &harness.Result{Text: "done", SessionID: "thread-1"}, nil
	})
	srv.SetAborter(func(session string) error { return nil })

	stdout, stderr, code := runLeo(t, dir, nil, "run", "nightly", "-c", cfgPath)
	if code != 0 {
		t.Fatalf("leo run exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	rows := cap.snapshot()
	if len(rows) != 1 {
		t.Fatalf("expected exactly 1 injected prompt, got %d", len(rows))
	}
	row := rows[0]
	if row.Session != "leo-session-nightly" {
		t.Errorf("injected session = %q, want %q", row.Session, "leo-session-nightly")
	}
	if strings.Contains(row.Prompt, "leo:invocation=") {
		t.Errorf("codex prompt must be bare (no marker), got %q", row.Prompt)
	}
	if row.Prompt != promptBody+"\n" {
		t.Errorf("codex prompt = %q, want bare body %q", row.Prompt, promptBody+"\n")
	}

	entry := pollHistoryEntry(t, dir, "nightly", 0, 3*time.Second)
	if entry.Reason != history.ReasonSuccess {
		t.Errorf("history reason = %q, want %q", entry.Reason, history.ReasonSuccess)
	}

	got := pollStoredSessionID(t, dir, "nightly", 3*time.Second)
	if got != "thread-1" {
		t.Errorf("stored session id = %q, want %q", got, "thread-1")
	}
}

// mkTempE2EDir creates a /tmp-based temp dir to stay under the macOS
// 104-char Unix socket path limit. t.TempDir() can produce paths that
// exceed the limit when running from /Users/<user>/very-long-paths.
func mkTempE2EDir(t *testing.T, pattern string) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", pattern)
	if err != nil {
		t.Fatalf("mkTempE2EDir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
