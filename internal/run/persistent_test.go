package run

import (
	"context"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
)

func TestWrapPromptWithMarkerAndFooter(t *testing.T) {
	out := wrapPromptForPersistent("abcdef0123456789abcdef0123456789", "hello", []string{"plugin:slack@official", "plugin:tg@official"})
	if !strings.Contains(out, "<!-- leo:invocation=abcdef0123456789abcdef0123456789 -->") {
		t.Fatalf("missing marker:\n%s", out)
	}
	if !strings.Contains(out, "hello") {
		t.Fatalf("missing body")
	}
	if !strings.Contains(out, "plugin:slack@official, plugin:tg@official") {
		t.Fatalf("missing channel footer")
	}
}

func TestWrapPromptOmitsFooterWhenNoChannels(t *testing.T) {
	out := wrapPromptForPersistent("abcdef0123456789abcdef0123456789", "hello", nil)
	if strings.Contains(out, "deliver your final reply") {
		t.Fatalf("expected no delivery footer when channels empty:\n%s", out)
	}
	if !strings.Contains(out, "<!-- leo:invocation=") {
		t.Fatalf("marker should still be present")
	}
}

// TestPromptForPersistentNonClaudeIsBare verifies that a non-claude
// persistent task enqueues the bare assembled prompt body — no leo
// invocation marker — since completion for those harnesses arrives
// synchronously via the driver's Result, not an async Stop-hook Report that
// needs the marker to correlate.
func TestPromptForPersistentNonClaudeIsBare(t *testing.T) {
	cfg := &config.Config{Defaults: config.DefaultsConfig{Harness: "codex"}}
	task := config.TaskConfig{Runtime: "persistent"}
	out := promptForPersistent(cfg, task, "abcdef0123456789abcdef0123456789", "hello")
	if out != "hello" {
		t.Fatalf("expected bare prompt %q, got %q", "hello", out)
	}
	if strings.Contains(out, "leo:invocation=") {
		t.Fatalf("marker must be absent for non-claude persistent tasks: %q", out)
	}
}

// TestPromptForPersistentOpencodeIsBare mirrors the codex case for opencode.
func TestPromptForPersistentOpencodeIsBare(t *testing.T) {
	cfg := &config.Config{}
	task := config.TaskConfig{Runtime: "persistent", Harness: "opencode"}
	out := promptForPersistent(cfg, task, "abcdef0123456789abcdef0123456789", "do the thing")
	if out != "do the thing" {
		t.Fatalf("expected bare prompt, got %q", out)
	}
	if strings.Contains(out, "leo:invocation=") {
		t.Fatalf("marker must be absent for opencode persistent tasks: %q", out)
	}
}

// TestPromptForPersistentClaudeKeepsWrap verifies claude tasks (explicit or
// via the default harness) keep the marker+footer wrap byte-identical to
// wrapPromptForPersistent.
func TestPromptForPersistentClaudeKeepsWrap(t *testing.T) {
	cfg := &config.Config{}
	task := config.TaskConfig{Runtime: "persistent", Channels: []string{"plugin:slack@official"}}
	out := promptForPersistent(cfg, task, "abcdef0123456789abcdef0123456789", "hello")
	want := wrapPromptForPersistent("abcdef0123456789abcdef0123456789", "hello", task.Channels)
	if out != want {
		t.Fatalf("promptForPersistent(claude) = %q, want %q", out, want)
	}
	if !strings.Contains(out, "<!-- leo:invocation=abcdef0123456789abcdef0123456789 -->") {
		t.Fatalf("expected marker for claude: %q", out)
	}
}

func TestRunPersistentDispatchSelected(t *testing.T) {
	called := false
	orig := persistentImpl
	defer func() { persistentImpl = orig }()
	persistentImpl = func(cfg *config.Config, taskName string) error {
		called = true
		return nil
	}
	cfg := &config.Config{
		Tasks: map[string]config.TaskConfig{
			"t1": {Runtime: "persistent", PromptFile: "_", Workspace: "/tmp"},
		},
	}
	_ = Run(cfg, "t1", nil)
	if !called {
		t.Fatalf("expected runPersistent dispatch")
	}
}

func TestPersistentFailureEnqueuesFollowUpWhenNotifyOnFail(t *testing.T) {
	// Capture follow-up enqueues via the seam.
	var followUpPrompts []string
	var followUpChannels [][]string
	orig := enqueueFollowUp
	defer func() { enqueueFollowUp = orig }()
	enqueueFollowUp = func(ctx context.Context, homePath string, req daemon.EnqueueRequest) {
		followUpPrompts = append(followUpPrompts, req.Prompt)
		followUpChannels = append(followUpChannels, req.Channels)
	}

	cfg := &config.Config{
		HomePath: t.TempDir(),
		Tasks: map[string]config.TaskConfig{
			"t1": {
				Runtime:      "persistent",
				Channels:     []string{"plugin:slack@official"},
				NotifyOnFail: true,
			},
		},
	}
	handlePersistentFailure(cfg, "t1", "test-failure-reason")

	if len(followUpPrompts) != 1 {
		t.Fatalf("expected 1 follow-up enqueue, got %d", len(followUpPrompts))
	}
	if !strings.Contains(followUpPrompts[0], "test-failure-reason") {
		t.Fatalf("follow-up prompt missing reason:\n%s", followUpPrompts[0])
	}
	if !strings.Contains(followUpPrompts[0], "plugin:slack@official") {
		t.Fatalf("follow-up prompt missing channels footer:\n%s", followUpPrompts[0])
	}
	if len(followUpChannels[0]) != 1 || followUpChannels[0][0] != "plugin:slack@official" {
		t.Fatalf("follow-up channels wrong: %v", followUpChannels[0])
	}
}

func TestPersistentFailureDoesNotNotifyWithoutFlag(t *testing.T) {
	var called bool
	orig := enqueueFollowUp
	defer func() { enqueueFollowUp = orig }()
	enqueueFollowUp = func(ctx context.Context, homePath string, req daemon.EnqueueRequest) {
		called = true
	}
	cfg := &config.Config{
		HomePath: t.TempDir(),
		Tasks: map[string]config.TaskConfig{
			"t1": {
				Runtime:      "persistent",
				Channels:     []string{"plugin:slack@official"},
				NotifyOnFail: false,
			},
		},
	}
	handlePersistentFailure(cfg, "t1", "reason")
	if called {
		t.Fatalf("expected no follow-up when NotifyOnFail is false")
	}
}

func TestPersistentFailureDoesNotNotifyWithoutChannels(t *testing.T) {
	var called bool
	orig := enqueueFollowUp
	defer func() { enqueueFollowUp = orig }()
	enqueueFollowUp = func(ctx context.Context, homePath string, req daemon.EnqueueRequest) {
		called = true
	}
	cfg := &config.Config{
		HomePath: t.TempDir(),
		Tasks: map[string]config.TaskConfig{
			"t1": {
				Runtime:      "persistent",
				NotifyOnFail: true,
				// no channels
			},
		},
	}
	handlePersistentFailure(cfg, "t1", "reason")
	if called {
		t.Fatalf("expected no follow-up when channels are empty")
	}
}

func TestNewInvocationID16IsHex32(t *testing.T) {
	for i := 0; i < 5; i++ {
		id := newInvocationID16()
		if len(id) != 32 {
			t.Fatalf("expected 32 hex chars, got %d (%q)", len(id), id)
		}
		for _, ch := range id {
			isHex := (ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')
			if !isHex {
				t.Fatalf("non-hex char in id: %q", id)
			}
		}
	}
}

// TestSessionTmuxTargetTopologies verifies the bare logical session name is
// mapped to the correct concrete tmux session for each topology. This is the
// seam that was wrong: the router was injecting into the bare name.
func TestSessionTmuxTargetTopologies(t *testing.T) {
	cfg := &config.Config{
		Tasks: map[string]config.TaskConfig{
			"dedicated": {Runtime: "persistent", Workspace: "/w"},
			"shared":    {Runtime: "persistent", Session: "team"},
			"attached":  {Runtime: "persistent", Session: "process:web"},
		},
		Sessions:  map[string]config.SessionConfig{"team": {Workspace: "/w"}},
		Processes: map[string]config.ProcessConfig{"web": {Workspace: "/w"}},
	}
	cases := map[string]string{
		"dedicated": "leo-session-dedicated", // Topology A — implicit dedicated
		"shared":    "leo-session-team",      // Topology B — shared session
		"attached":  "leo-web",               // Topology C — process-attached
	}
	for task, want := range cases {
		got, err := sessionTmuxTarget(cfg, task)
		if err != nil {
			t.Fatalf("%s: %v", task, err)
		}
		if got != want {
			t.Fatalf("%s: tmux target = %q, want %q", task, got, want)
		}
	}
}
