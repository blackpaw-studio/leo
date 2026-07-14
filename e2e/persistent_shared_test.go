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

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/daemon"
)

// TestPersistentSharedTemplate exercises multiple tasks pointing at the same
// templates: entry via template: <name>. Each task should land in the same
// agent's tmux session and the sentinel marker is what correlates each
// invocation to its waiting runner. A "human turn" (Report with empty
// invocation_id) must be silently ignored, not crash anything.
func TestPersistentSharedTemplate(t *testing.T) {
	dir := mkTempE2EDir(t, "leo-e2e-persist-shared-*")

	cfgYAML := fmt.Sprintf(`defaults:
  model: sonnet
  max_turns: 15
templates:
  homebase:
    workspace: %s
    model: sonnet
    channels:
      - plugin:telegram@claude-plugins-official
tasks:
  morning:
    workspace: %s
    schedule: "0 9 * * *"
    prompt_file: prompts/MORNING.md
    runtime: persistent
    template: homebase
    channels:
      - plugin:telegram@claude-plugins-official
    enabled: true
  evening:
    workspace: %s
    schedule: "0 21 * * *"
    prompt_file: prompts/EVENING.md
    runtime: persistent
    template: homebase
    channels:
      - plugin:telegram@claude-plugins-official
    enabled: true
`, dir, dir, dir)

	cfgPath := filepath.Join(dir, "leo.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o600); err != nil {
		t.Fatalf("writing leo.yaml: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		t.Fatalf("mkdir prompts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompts/MORNING.md"), []byte("Morning briefing.\n"), 0o644); err != nil {
		t.Fatalf("writing morning prompt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompts/EVENING.md"), []byte("Evening wrap-up.\n"), 0o644); err != nil {
		t.Fatalf("writing evening prompt: %v", err)
	}

	// No AgentEnsurer is wired on this daemon (the test drives the injector
	// directly, bypassing agent.Manager.Spawn), so pre-seed an agentstore
	// record for the shared target agent — mirrors the real-world side
	// effect of a first spawn — so the report-path agentstore.Update call
	// below has a record to mutate.
	if err := agentstore.Save(dir, agentstore.Record{Name: "homebase", Workspace: dir}); err != nil {
		t.Fatalf("seeding agentstore: %v", err)
	}

	srv := startDaemon(t, dir, cfgPath)
	cap := &promptCapture{}
	installAutoResponder(t, srv, dir, cap)

	// Inject a "human turn" report BEFORE the first task fires. The
	// router must accept this without disturbing future correlations.
	humanCtx, humanCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer humanCancel()
	if err := daemon.ReportTask(humanCtx, dir, "" /* no invocation id */, "human-csid", "human typing", "leo-homebase"); err != nil {
		t.Fatalf("human-turn report: %v", err)
	}

	// Fire both tasks. We run them sequentially so the assertions on the
	// recorded prompt content stay deterministic; the queue-FIFO test
	// already covers concurrent enqueue ordering.
	_, _, code1 := runLeo(t, dir, nil, "run", "morning", "-c", cfgPath)
	if code1 != 0 {
		t.Fatalf("morning run failed: %d", code1)
	}
	_, _, code2 := runLeo(t, dir, nil, "run", "evening", "-c", cfgPath)
	if code2 != 0 {
		t.Fatalf("evening run failed: %d", code2)
	}

	rows := cap.snapshot()
	if len(rows) != 2 {
		t.Fatalf("expected 2 injections (one per task), got %d", len(rows))
	}
	for i, row := range rows {
		// The injector must receive the concrete tmux session name for the
		// shared agent ("leo-<template>"), NOT either task's own name —
		// both tasks deliver into the same agent-ensure target.
		if row.Session != "leo-homebase" {
			t.Errorf("row[%d] injected session = %q, want %q", i, row.Session, "leo-homebase")
		}
		if row.InvID == "" {
			t.Errorf("row[%d] missing invocation marker", i)
		}
	}
	if !strings.Contains(rows[0].Prompt, "Morning briefing.") {
		t.Errorf("first prompt should be morning's; got %q", rows[0].Prompt)
	}
	if !strings.Contains(rows[1].Prompt, "Evening wrap-up.") {
		t.Errorf("second prompt should be evening's; got %q", rows[1].Prompt)
	}
	if rows[0].InvID == rows[1].InvID {
		t.Errorf("each invocation must have its own marker (got %s twice)", rows[0].InvID)
	}

	// Each task carries its own channels list; the runner appends a
	// delivery footer that lists those channels (no claude -p anywhere).
	for i, row := range rows {
		if !strings.Contains(row.Prompt, "plugin:telegram@claude-plugins-official") {
			t.Errorf("row[%d] delivery footer missing telegram channel: %q", i, row.Prompt)
		}
	}

	// Both tasks share an agent; the second run should overwrite the
	// first's stored session id (the auto-responder derives it from the
	// injected tmux session name), demonstrating shared resume state. The
	// id is keyed under the shared agent's name ("homebase") in the
	// agentstore, not the legacy generic session store.
	got := pollAgentstoreSessionID(t, dir, "homebase", 3*time.Second)
	if want := "csid-leo-homebase"; got != want {
		t.Errorf("stored session id = %q, want %q", got, want)
	}
}
