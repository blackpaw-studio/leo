package run

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/observe"
)

// recordingPublisher captures every event published to it.
type recordingPublisher struct {
	events []observe.Event
}

func (r *recordingPublisher) Publish(ev observe.Event) { r.events = append(r.events, ev) }

func setUpTaskWorkspace(t *testing.T, dir, taskName string) *config.Config {
	t.Helper()
	ws := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(ws, 0755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws, "task.md"), []byte("test prompt"), 0644); err != nil {
		t.Fatalf("write prompt file: %v", err)
	}
	return &config.Config{
		HomePath: dir,
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 15},
		Tasks: map[string]config.TaskConfig{
			taskName: {PromptFile: "task.md", Schedule: "0 * * * *", Enabled: true},
		},
	}
}

func TestRunWithNoPublisherDoesNotPanic(t *testing.T) {
	// Arrange
	origExec := execCommand
	t.Cleanup(func() { execCommand = origExec })
	execCommand = func(name string, args ...string) *exec.Cmd { return exec.Command("echo", "ok") }

	cfg := setUpTaskWorkspace(t, t.TempDir(), "mytask")

	// Act + Assert: no publisher configured must be a safe no-op.
	if err := Run(cfg, "mytask", nil); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
}

func TestRunPublishesStartedThenSucceeded(t *testing.T) {
	// Arrange
	origExec := execCommand
	origNow := runNow
	t.Cleanup(func() {
		execCommand = origExec
		runNow = origNow
		SetPublisher(nil)
	})
	execCommand = func(name string, args ...string) *exec.Cmd { return exec.Command("echo", "task output") }
	fixedStart := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	runNow = func() time.Time { return fixedStart }

	pub := &recordingPublisher{}
	SetPublisher(pub)

	cfg := setUpTaskWorkspace(t, t.TempDir(), "mytask")

	// Act
	if err := Run(cfg, "mytask", nil); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// Assert
	if len(pub.events) != 2 {
		t.Fatalf("expected 2 events, got %d: %+v", len(pub.events), pub.events)
	}
	started, ok := pub.events[0].Payload.(*observe.TaskRunPayload)
	if !ok || pub.events[0].Type != observe.EventTaskRunStarted {
		t.Fatalf("expected first event to be EventTaskRunStarted, got %s (%T)", pub.events[0].Type, pub.events[0].Payload)
	}
	if started.Run.Task != "mytask" || started.Run.Status != observe.RunRunning {
		t.Fatalf("unexpected started payload: %+v", started.Run)
	}
	if !started.Run.StartedAt.Equal(fixedStart) {
		t.Fatalf("expected StartedAt %v, got %v", fixedStart, started.Run.StartedAt)
	}
	if started.Run.ID == "" {
		t.Fatal("expected a non-empty run ID")
	}
	if started.Run.Model != "sonnet" {
		t.Fatalf("expected Model %q resolved from defaults, got %q", "sonnet", started.Run.Model)
	}
	if started.Run.Harness != "claude" {
		t.Fatalf("expected Harness %q (default), got %q", "claude", started.Run.Harness)
	}
	if started.Run.Workspace == "" {
		t.Fatal("expected a non-empty resolved Workspace")
	}

	succeeded, ok := pub.events[1].Payload.(*observe.TaskRunPayload)
	if !ok || pub.events[1].Type != observe.EventTaskRunSucceeded {
		t.Fatalf("expected second event to be EventTaskRunSucceeded, got %s (%T)", pub.events[1].Type, pub.events[1].Payload)
	}
	if succeeded.Run.ID != started.Run.ID {
		t.Fatalf("expected succeeded run ID %q to match started run ID %q", succeeded.Run.ID, started.Run.ID)
	}
	if succeeded.Run.Status != observe.RunSucceeded {
		t.Fatalf("expected RunSucceeded, got %s", succeeded.Run.Status)
	}
	if succeeded.Run.EndedAt == nil {
		t.Fatal("expected EndedAt to be set on a finished run")
	}
	if succeeded.Run.DurationMS == nil {
		t.Fatal("expected DurationMS to be set on a finished run")
	}
	if succeeded.Run.Error != "" {
		t.Fatalf("expected no error on a succeeded run, got %q", succeeded.Run.Error)
	}
	if succeeded.Run.Model != started.Run.Model || succeeded.Run.Harness != started.Run.Harness || succeeded.Run.Workspace != started.Run.Workspace {
		t.Fatalf("expected finished run to carry the same resolved Workspace/Model/Harness as started: started=%+v succeeded=%+v", started.Run, succeeded.Run)
	}
}

func TestRunPublishesFailedWithReasonOnError(t *testing.T) {
	// Arrange
	origExec := execCommand
	t.Cleanup(func() {
		execCommand = origExec
		SetPublisher(nil)
	})
	execCommand = func(name string, args ...string) *exec.Cmd { return exec.Command("false") }

	pub := &recordingPublisher{}
	SetPublisher(pub)

	cfg := setUpTaskWorkspace(t, t.TempDir(), "mytask")

	// Act
	if err := Run(cfg, "mytask", nil); err == nil {
		t.Fatal("expected Run() to return an error")
	}

	// Assert
	if len(pub.events) != 2 {
		t.Fatalf("expected 2 events, got %d: %+v", len(pub.events), pub.events)
	}
	failed, ok := pub.events[1].Payload.(*observe.TaskRunPayload)
	if !ok || pub.events[1].Type != observe.EventTaskRunFailed {
		t.Fatalf("expected EventTaskRunFailed, got %s (%T)", pub.events[1].Type, pub.events[1].Payload)
	}
	if failed.Run.Status != observe.RunFailed {
		t.Fatalf("expected RunFailed, got %s", failed.Run.Status)
	}
	if failed.Run.Error == "" {
		t.Fatal("expected a non-empty failure reason")
	}
}
