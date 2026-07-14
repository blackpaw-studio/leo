package run

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/session"
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

// --- resolveDeliveryTarget: every persistent task resolves to an agent
// target (explicit via `template:`, or implicit/synthesized from the task
// itself). ---

func TestResolveDeliveryTargetTemplateTaskUsesAgentEnsurePath(t *testing.T) {
	cfg := &config.Config{
		Tasks: map[string]config.TaskConfig{
			"nightly": {Runtime: "persistent", Template: "worker"},
		},
		Templates: map[string]config.TemplateConfig{
			"worker": {Workspace: "/tmp/worker-ws", Model: "sonnet"},
		},
	}

	target, err := resolveDeliveryTarget(cfg, "nightly")
	if err != nil {
		t.Fatalf("resolveDeliveryTarget: %v", err)
	}
	if target.queueKey != "worker" {
		t.Errorf("queueKey = %q, want %q", target.queueKey, "worker")
	}
	if target.tmux != "leo-worker" {
		t.Errorf("tmux = %q, want %q", target.tmux, "leo-worker")
	}
	if target.ensure == nil {
		t.Fatalf("expected non-nil ensure spec")
	}
	if target.ensure.Name != "worker" {
		t.Errorf("ensure.Name = %q, want %q", target.ensure.Name, "worker")
	}
	if target.ensure.TemplateName != "worker" {
		t.Errorf("ensure.TemplateName = %q, want %q", target.ensure.TemplateName, "worker")
	}
	if target.ensure.Implicit {
		t.Errorf("expected Implicit=false for an explicit template: task")
	}
	if target.ensure.Template.Workspace != "/tmp/worker-ws" {
		t.Errorf("ensure.Template.Workspace = %q, want %q", target.ensure.Template.Workspace, "/tmp/worker-ws")
	}
}

func TestResolveDeliveryTargetImplicitTaskSynthesizesTemplate(t *testing.T) {
	cfg := &config.Config{
		Tasks: map[string]config.TaskConfig{
			"digest": {Runtime: "persistent", Workspace: "/tmp/digest-ws", Model: "opus"},
		},
	}

	target, err := resolveDeliveryTarget(cfg, "digest")
	if err != nil {
		t.Fatalf("resolveDeliveryTarget: %v", err)
	}
	if target.queueKey != "digest" {
		t.Errorf("queueKey = %q, want %q", target.queueKey, "digest")
	}
	if target.tmux != "leo-digest" {
		t.Errorf("tmux = %q, want %q", target.tmux, "leo-digest")
	}
	if target.ensure == nil {
		t.Fatalf("expected non-nil ensure spec")
	}
	if !target.ensure.Implicit {
		t.Errorf("expected Implicit=true for a task with no template:")
	}
	if target.ensure.Template.Workspace != "/tmp/digest-ws" {
		t.Errorf("ensure.Template.Workspace = %q, want %q", target.ensure.Template.Workspace, "/tmp/digest-ws")
	}
}

// shortTempDir returns a short-path temp dir under /tmp (unlike t.TempDir(),
// which nests under the test name and can exceed the ~104-char Unix socket
// path limit on macOS once daemon.SockPath appends "state/leo.sock").
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "leo-rt-*")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// fakeEnsurer adapts a func to daemon.AgentEnsurer for tests.
type fakeEnsurer func(ctx context.Context, spec daemon.EnsureSpec) error

func (f fakeEnsurer) Ensure(ctx context.Context, spec daemon.EnsureSpec) error { return f(ctx, spec) }

// newTestDaemon starts a real daemon.Server on a temp Unix socket under
// cfg.HomePath/state/leo.sock (the path daemon.EnqueueTask/AwaitTask dial),
// wired with a synchronous fake injector (returns a *harness.Result
// immediately, so the pump completes without an async Report/timeout) and
// the given ensurer. Returns the server for Shutdown.
func newTestDaemon(t *testing.T, homePath string, ensurer daemon.AgentEnsurer) *daemon.Server {
	t.Helper()
	stateDir := filepath.Join(homePath, "state")
	if err := os.MkdirAll(stateDir, 0750); err != nil {
		t.Fatalf("creating state dir: %v", err)
	}
	srv := daemon.New(daemon.SockPath(homePath), filepath.Join(homePath, "leo.yaml"), nil)
	srv.SetInjector(func(ctx context.Context, tmuxSession, prompt string) (*harness.Result, error) {
		return &harness.Result{SessionID: "sid-123", Text: "done"}, nil
	})
	if ensurer != nil {
		srv.SetEnsurer(ensurer)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("starting daemon: %v", err)
	}
	t.Cleanup(func() { _ = srv.Shutdown() })
	return srv
}

// writePromptFile writes a minimal prompt file under workspace and returns
// its basename, ready to use as TaskConfig.PromptFile.
func writePromptFile(t *testing.T, workspace string) string {
	t.Helper()
	if err := os.MkdirAll(workspace, 0750); err != nil {
		t.Fatalf("creating workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "prompt.md"), []byte("do the thing"), 0600); err != nil {
		t.Fatalf("writing prompt file: %v", err)
	}
	return "prompt.md"
}

// TestRunPersistentTemplateTaskEnqueuesWithAgentEnsure is the end-to-end
// counterpart to TestResolveDeliveryTargetTemplateTaskUsesAgentEnsurePath:
// a real runPersistent call against a real (in-process) daemon must invoke
// the wired AgentEnsurer with the resolved agent target before the fake
// injector completes the turn.
func TestRunPersistentTemplateTaskEnqueuesWithAgentEnsure(t *testing.T) {
	home := shortTempDir(t)
	ws := filepath.Join(home, "ws")
	promptFile := writePromptFile(t, ws)

	var calls []daemon.EnsureSpec
	newTestDaemon(t, home, fakeEnsurer(func(_ context.Context, spec daemon.EnsureSpec) error {
		calls = append(calls, spec)
		return nil
	}))

	cfg := &config.Config{
		HomePath: home,
		Tasks: map[string]config.TaskConfig{
			"nightly": {Runtime: "persistent", Workspace: ws, PromptFile: promptFile, Template: "worker"},
		},
		Templates: map[string]config.TemplateConfig{
			"worker": {Workspace: ws},
		},
	}

	if err := runPersistent(cfg, "nightly"); err != nil {
		t.Fatalf("runPersistent: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 Ensure call, got %d: %+v", len(calls), calls)
	}
	if calls[0].Name != "worker" {
		t.Errorf("Ensure spec Name = %q, want %q", calls[0].Name, "worker")
	}
}

// TestRunPersistentAgentTaskPersistsSessionIDToAgentstore is the report-path
// counterpart to TestRunPersistentTemplateTaskEnqueuesWithAgentEnsure: once
// the (fake) daemon reports completion with a discovered session id, a
// persistent task's invocation must persist that id onto the agentstore
// record — NOT the generic "session:"+name key-value store.
func TestRunPersistentAgentTaskPersistsSessionIDToAgentstore(t *testing.T) {
	home := shortTempDir(t)
	ws := filepath.Join(home, "ws")
	promptFile := writePromptFile(t, ws)

	newTestDaemon(t, home, fakeEnsurer(func(context.Context, daemon.EnsureSpec) error { return nil }))

	// Simulate the ensurer's real-world side effect (agent.Manager.Spawn
	// persists a record before the tmux-TUI driver starts): pre-seed an
	// agentstore record for the target agent so the report-path Update finds
	// something to mutate.
	if err := agentstore.Save(home, agentstore.Record{Name: "worker", Workspace: ws}); err != nil {
		t.Fatalf("seeding agentstore: %v", err)
	}

	cfg := &config.Config{
		HomePath: home,
		Tasks: map[string]config.TaskConfig{
			"nightly": {Runtime: "persistent", Workspace: ws, PromptFile: promptFile, Template: "worker"},
		},
		Templates: map[string]config.TemplateConfig{
			"worker": {Workspace: ws},
		},
	}

	if err := runPersistent(cfg, "nightly"); err != nil {
		t.Fatalf("runPersistent: %v", err)
	}

	recs, err := agentstore.Load(agentstore.FilePath(home))
	if err != nil {
		t.Fatalf("loading agentstore: %v", err)
	}
	if got := recs["worker"].SessionID; got != "sid-123" {
		t.Errorf("agentstore SessionID = %q, want %q", got, "sid-123")
	}

	if _, found, err := session.NewStore(home).Get("session:worker"); err != nil {
		t.Fatalf("checking session store: %v", err)
	} else if found {
		t.Error("persistent task invocations must not write to the generic session store")
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
