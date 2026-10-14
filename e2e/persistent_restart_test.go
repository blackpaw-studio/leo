//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

// persistentRestartTasks gives each restart case its own persistent task:
// "daily" targets the agent synthesized from the task, "wake" one bound to
// the "worker" template. Both run the codex-shaped fake TUI. The short task
// timeout bounds the codex completion wait (docs: persistent-tasks.md).
const persistentRestartTasks = `  worker:
    harness: codex
    model: gpt-5
    env:
      CODEX_HOME: %[1]s
      FAKECLAUDE_DISPATCH_INTERACTIVE: "1"
tasks:
  daily:
    runtime: persistent
    harness: codex
    model: gpt-5
    prompt_file: prompt.md
    schedule: "0 3 1 1 *"
    timeout: 2s
    env:
      CODEX_HOME: %[1]s
      FAKECLAUDE_DISPATCH_INTERACTIVE: "1"
  wake:
    runtime: persistent
    template: worker
    prompt_file: prompt.md
    schedule: "0 3 1 1 *"
    timeout: 2s
`

// TestPersistentTaskAgentSurvivesDaemonRestart SIGKILLs the daemon (as
// `launchctl kickstart -k` does during `leo update`) after a persistent task
// spawned its agent, then checks the restarted daemon adopts the live tmux
// session instead of relaunching it, and a second firing reuses it.
func TestPersistentTaskAgentSurvivesDaemonRestart(t *testing.T) {
	cases := []struct{ name, task, agentName string }{
		{"implicit target", "daily", "daily"},
		{"template target", "wake", "worker"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newInteractiveE2EWithOptions(t, interactiveOptions{extraConfig: func(ws string) string {
				// prompt_file resolves against the default workspace, <home>/workspace.
				dir := filepath.Join(ws, "workspace")
				if err := os.MkdirAll(dir, 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "prompt.md"), []byte("scheduled-run"), 0o600); err != nil {
					t.Fatal(err)
				}
				return fmt.Sprintf(persistentRestartTasks, ws)
			}})
			session := agent.SessionName(tc.agentName)

			s.fire(t, tc.task)
			s.waitForSession(t, session)
			pid := s.panePID(t, session)

			s.restartDaemon(t)

			if got := s.panePID(t, session); got != pid {
				t.Fatalf("restart relaunched the session: pane pid %s, was %s", got, pid)
			}
			s.waitFor(t, func() bool { return strings.Contains(s.serviceLog(), "adopted existing tmux session '"+session+"'") })
			s.waitFor(t, func() bool { return s.agentRunning(t, tc.agentName) })

			s.fire(t, tc.task)
			if got := s.panePID(t, session); got != pid {
				t.Fatalf("the second firing relaunched the session: pane pid %s, was %s", got, pid)
			}
			recs, err := agentstore.Load(agentstore.FilePath(s.ws))
			if err != nil || len(recs) != 1 {
				t.Fatalf("agentstore records = %d (err %v), want exactly the %s agent", len(recs), err, tc.agentName)
			}
		})
	}
}

// fire runs `leo run <task>`. Codex persistent firings are recorded as
// failed (completion is timeout-based) and the fake TUI cannot satisfy the
// injector's input probe, so the exit code is not asserted: the firing's
// ensure-exists step is what matters, and callers assert on the session.
func (s *interactiveE2E) fire(t *testing.T, task string) {
	t.Helper()
	cmd := exec.Command(leoBin, "run", task, "-c", s.cfgPath)
	cmd.Dir = s.ws
	cmd.Env = append(s.daemonEnv(), "PATH="+filepath.Dir(fakeclaude)+":"+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Logf("leo run %s: %v\n%s", task, err, out)
	}
}

func (s *interactiveE2E) panePID(t *testing.T, session string) string {
	t.Helper()
	out, err := exec.Command(s.tmux, tmux.Args("list-panes", "-t", tmux.Target(session), "-F", "#{pane_pid}")...).Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		t.Fatalf("pane pid of %s: %v (%q)", session, err, out)
	}
	return strings.Fields(string(out))[0]
}

func (s *interactiveE2E) serviceLog() string {
	b, _ := os.ReadFile(filepath.Join(s.ws, "state", "service.log"))
	return string(b)
}

func (s *interactiveE2E) agentRunning(t *testing.T, name string) bool {
	t.Helper()
	var list []agent.Record
	s.request(t, "GET", "/api/agent/list", nil, &list)
	for _, rec := range list {
		if rec.Name == name {
			return rec.Status == "running"
		}
	}
	return false
}
