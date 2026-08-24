//go:build e2e

package e2e

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

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

// TestPersistentEnsureSpawnsMissingAgent exercises the genuinely new
// ensure-exists machinery (daemon.AgentEnsurer -> agent.Manager.
// SpawnFromTemplate) against a REAL agent.Manager and REAL tmux — not a
// stand-in. Only the tmux-session-creation primitive itself is provided by a
// small test double (tmuxAgentSupervisor, implementing agent.Supervisor)
// rather than the production internal/service.Supervisor, because that type's
// tmux/claude-binary wiring is package-private and only ever assembled inside
// internal/service.RunSupervised. Everything above that seam — Ensure,
// SpawnFromTemplate, template resolution, arg building, agentstore
// persistence — runs unmodified production code.
//
// The task's target agent ("worker") does not exist when the task fires. The
// test asserts: (1) a real tmux session "leo-worker" is created, (2) the
// prompt is genuinely delivered into it (via the real, exported
// tmux.InjectPromptTUI, using a permissive Profile appropriate for
// fakeclaude's plain-REPL double, which has no claude-style input-box chrome
// to probe) and fakeclaude's live process echoes a reply back into the pane,
// and (3) the spawn is persisted to the agentstore under the bare agent name.
func TestPersistentEnsureSpawnsMissingAgent(t *testing.T) {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not available; skipping live ensure-exists test")
	}

	dir := mkTempE2EDir(t, "leo-e2e-persist-ensure-*")
	templateWS := filepath.Join(dir, "worker-ws")

	cfgYAML := fmt.Sprintf(`defaults:
  model: sonnet
  max_turns: 15
templates:
  worker:
    workspace: %s
tasks:
  wake:
    workspace: %s
    schedule: "0 9 * * *"
    prompt_file: prompts/WAKE.md
    runtime: persistent
    template: worker
    enabled: true
`, templateWS, dir)

	cfgPath := filepath.Join(dir, "leo.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o600); err != nil {
		t.Fatalf("writing leo.yaml: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		t.Fatalf("mkdir prompts: %v", err)
	}
	const promptBody = "Wake up and report in."
	if err := os.WriteFile(filepath.Join(dir, "prompts/WAKE.md"), []byte(promptBody+"\n"), 0o644); err != nil {
		t.Fatalf("writing prompt: %v", err)
	}

	srv := startDaemon(t, dir, cfgPath)
	cap := &promptCapture{}

	cfgLoader := func() (*config.Config, error) { return config.Load(cfgPath) }
	sup := newTmuxAgentSupervisor(tmuxPath, fakeclaude)
	agentMgr := agent.New(cfgLoader, sup, tmuxPath, "")
	srv.SetEnsurer(daemon.NewAgentEnsurer(agentMgr))

	// fakeclaude's interactive REPL has no claude-style "❯ " input-box chrome
	// to probe, so ClaudeProfile's classifier would never recognize it as
	// ready — it isn't meant to; that classifier is specific to real claude's
	// rendered UI (already covered by internal/tmux's own tests). A plain
	// REPL double is ready to receive input the moment its process is up, so
	// this profile reports exactly that instead of waiting to recognize UI
	// chrome that will never appear.
	fastProfile := tmux.Profile{Classify: func(string) tmux.InputState { return tmux.InputHasContent }}

	sessionName := agent.SessionName("worker")

	srv.SetInjector(func(ctx context.Context, session, prompt string) (*harness.Result, error) {
		cap.record(session, prompt)
		invID := extractMarker(prompt)
		if invID == "" {
			return nil, fmt.Errorf("ensure-exists injector: missing invocation marker")
		}
		if err := tmux.InjectPromptTUI(ctx, tmuxPath, session, prompt, fastProfile); err != nil {
			return nil, fmt.Errorf("real tmux injection into %q: %w", session, err)
		}
		go func(invID, session string) {
			// Wait for fakeclaude's real echo to land in the pane before
			// reporting completion, so leo run's success genuinely proves
			// end-to-end delivery rather than racing an unconditional report
			// against real tmux/process timing.
			if !pollPaneContains(tmuxPath, session, "FAKE-REPLY", 5*time.Second) {
				t.Logf("ensure-exists injector: FAKE-REPLY never appeared in pane %q", session)
			}
			reportCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := daemon.ReportTask(reportCtx, dir, invID, "csid-"+session, "FAKE-REPLY", session); err != nil {
				t.Logf("ensure-exists injector: report invID=%s err=%v", invID, err)
			}
		}(invID, session)
		return nil, nil
	})
	srv.SetAborter(func(session string) error {
		return exec.Command(tmuxPath, tmux.Args("send-keys", "-t", tmux.PaneTarget(session), "Escape")...).Run()
	})

	// Precondition: the target agent's tmux session does not exist yet.
	if hasTmuxSession(tmuxPath, sessionName) {
		t.Fatalf("precondition failed: session %q already exists", sessionName)
	}

	_, stderr, code := runLeo(t, dir, nil, "run", "wake", "-c", cfgPath)
	if code != 0 {
		t.Fatalf("leo run exit=%d stderr=%q", code, stderr)
	}

	// The ensure-exists path must have spawned a real tmux session for the
	// target agent — this is the genuinely new machinery under test. Only
	// now that the test has actually created the session do we register the
	// cleanup that kills it, so a failed precondition (a real agent already
	// occupying this session name) can never be torn down by this test.
	if !hasTmuxSession(tmuxPath, sessionName) {
		t.Fatalf("expected tmux session %q to exist after ensure-exists spawn", sessionName)
	}
	t.Cleanup(func() {
		_ = exec.Command(tmuxPath, tmux.Args("kill-session", "-t", tmux.Target(sessionName))...).Run()
	})

	// The prompt must have genuinely reached the spawned process, not a stub
	// standing in for it.
	pane := capturePane(t, tmuxPath, sessionName)
	if !strings.Contains(pane, "FAKE-REPLY") {
		t.Errorf("spawned agent's pane never showed a FAKE-REPLY echo; delivery did not land: %q", pane)
	}

	rows := cap.snapshot()
	if len(rows) != 1 {
		t.Fatalf("expected exactly 1 injected prompt, got %d", len(rows))
	}
	if rows[0].Session != sessionName {
		t.Errorf("injected session = %q, want %q", rows[0].Session, sessionName)
	}
	if !strings.Contains(rows[0].Prompt, promptBody) {
		t.Errorf("injected prompt missing body %q: %q", promptBody, rows[0].Prompt)
	}

	// The spawn persisted an agentstore record under the bare agent name
	// (the ensure-exists spec's Name — see EnsureSpec/SpawnFromTemplate),
	// mirroring production's agent.Manager.spawnShared.
	recs, err := agentstore.Load(agentstore.FilePath(dir))
	if err != nil {
		t.Fatalf("loading agentstore: %v", err)
	}
	if _, ok := recs["worker"]; !ok {
		t.Errorf("expected agentstore record for spawned agent %q, got records: %v", "worker", recs)
	}
}

// hasTmuxSession reports whether a tmux session by that name exists on Leo's
// dedicated tmux server.
func hasTmuxSession(tmuxPath, session string) bool {
	return exec.Command(tmuxPath, tmux.Args("has-session", "-t", tmux.Target(session))...).Run() == nil
}

// capturePane returns the current visible contents of session's active pane.
func capturePane(t *testing.T, tmuxPath, session string) string {
	t.Helper()
	out, err := exec.Command(tmuxPath, tmux.Args("capture-pane", "-p", "-t", tmux.PaneTarget(session))...).Output()
	if err != nil {
		t.Fatalf("capture-pane %q: %v", session, err)
	}
	return string(out)
}

// pollPaneContains polls session's pane every 100ms until it contains needle
// or timeout elapses. Returns whether needle was observed.
func pollPaneContains(tmuxPath, session, needle string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out, err := exec.Command(tmuxPath, tmux.Args("capture-pane", "-p", "-t", tmux.PaneTarget(session))...).Output()
		if err == nil && strings.Contains(string(out), needle) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// tmuxAgentSupervisor is a minimal, real-tmux-backed agent.Supervisor test
// double. It stands in for internal/service.Supervisor, whose tmux/claude
// binary wiring is package-private and only ever assembled inside
// internal/service.RunSupervised — not reachable from e2e's black-box
// perspective. Every method here does the real thing (spawns/kills real tmux
// sessions running the real fakeclaude binary); nothing is stubbed out.
type tmuxAgentSupervisor struct {
	mu         sync.Mutex
	tmuxPath   string
	claudePath string
	states     map[string]agent.ProcessState
	reserved   map[string]struct{}
}

func newTmuxAgentSupervisor(tmuxPath, claudePath string) *tmuxAgentSupervisor {
	return &tmuxAgentSupervisor{
		tmuxPath:   tmuxPath,
		claudePath: claudePath,
		states:     make(map[string]agent.ProcessState),
		reserved:   make(map[string]struct{}),
	}
}

func (s *tmuxAgentSupervisor) ReserveAgent(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.reserved[name]; ok {
		return fmt.Errorf("agent %q already reserved", name)
	}
	if _, ok := s.states[name]; ok {
		return fmt.Errorf("agent %q already exists", name)
	}
	s.reserved[name] = struct{}{}
	return nil
}

func (s *tmuxAgentSupervisor) ReleaseAgent(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.reserved, name)
}

// SpawnAgent creates a real tmux session (on Leo's dedicated "leo" tmux
// server, matching production's socket selection) named after
// agent.SessionName(spec.Name), running the real fakeclaude binary with the
// production-built ClaudeArgs. Mirrors internal/service.Supervisor's
// new-session invocation.
func (s *tmuxAgentSupervisor) SpawnAgent(spec agent.SpawnRequest) error {
	s.mu.Lock()
	if _, ok := s.states[spec.Name]; ok {
		s.mu.Unlock()
		return fmt.Errorf("process %q already exists", spec.Name)
	}
	s.mu.Unlock()

	if spec.WorkDir != "" {
		if err := os.MkdirAll(spec.WorkDir, 0o750); err != nil {
			return fmt.Errorf("creating workdir: %w", err)
		}
	}

	sessionName := agent.SessionName(spec.Name)
	tmuxArgs := append([]string{"new-session", "-d", "-s", sessionName, "-c", spec.WorkDir, s.claudePath}, spec.ClaudeArgs...)
	cmd := exec.Command(s.tmuxPath, tmux.Args(tmuxArgs...)...)
	cmd.Env = append(os.Environ(), envSlice(spec.Env)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("tmux new-session: %w: %s", err, out)
	}

	s.mu.Lock()
	s.states[spec.Name] = agent.ProcessState{Name: spec.Name, Status: "running", StartedAt: time.Now(), Ephemeral: true}
	delete(s.reserved, spec.Name)
	s.mu.Unlock()
	return nil
}

func (s *tmuxAgentSupervisor) StopAgent(name string, wakeOnMessage bool) error {
	s.mu.Lock()
	_, ok := s.states[name]
	delete(s.states, name)
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("agent %q not found", name)
	}
	sessionName := agent.SessionName(name)
	return exec.Command(s.tmuxPath, tmux.Args("kill-session", "-t", tmux.Target(sessionName))...).Run()
}

func (s *tmuxAgentSupervisor) RenameAgent(old, new string) error {
	return fmt.Errorf("tmuxAgentSupervisor: RenameAgent not supported by this e2e test double")
}

func (s *tmuxAgentSupervisor) EphemeralAgents() map[string]agent.ProcessState {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]agent.ProcessState, len(s.states))
	for k, v := range s.states {
		out[k] = v
	}
	return out
}

// envSlice renders an env map as KEY=VALUE entries for exec.Cmd.Env.
func envSlice(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}
