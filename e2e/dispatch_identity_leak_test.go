//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

// TestSupervisedAgentDoesNotInheritDispatchIdentity reproduces `make e2e`
// run from inside a dispatch pane: the daemon's env carries that pane's
// LEO_DISPATCH_ID and LEO_CONFIG. A supervised agent's `leo dispatch report`
// hooks must not report into the caller's run. TestMain scrubs the real
// caller env, so the canary is set explicitly on this daemon — the only thing
// standing between it and the agent is the supervisor's session env.
func TestSupervisedAgentDoesNotInheritDispatchIdentity(t *testing.T) {
	var mu sync.Mutex
	var hits []string
	caller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, r.Method+" "+r.URL.Path)
		mu.Unlock()
	}))
	t.Cleanup(caller.Close)
	callerPort := caller.Listener.Addr().(*net.TCPAddr).Port
	callerCfg := filepath.Join(mkTempE2EDir(t, "leo-e2e-canary-*"), "leo.yaml")
	if err := os.WriteFile(callerCfg, []byte(fmt.Sprintf("web:\n  port: %d\ntasks: {}\n", callerPort)), 0o600); err != nil {
		t.Fatal(err)
	}

	s := newInteractiveE2EWithOptions(t, interactiveOptions{daemonEnv: []string{
		"LEO_DISPATCH_ID=d-canary",
		"LEO_CONFIG=" + callerCfg,
		"LEO_API_TOKEN=canary-token",
	}})
	marker := filepath.Join(s.ws, "hook-ran.log")
	writeReportHooks(t, s.ws, marker)

	session := agent.SessionName(s.spawnAgent(t, "leakcheck"))
	s.waitForSession(t, session)
	pane := s.sessionPane(t, session)
	if out, err := exec.Command(s.tmux, tmux.Args("send-keys", "-t", pane, "-l", "hello from the agent")...).CombinedOutput(); err != nil {
		t.Fatalf("send-keys: %v: %s", err, out)
	}
	if out, err := exec.Command(s.tmux, tmux.Args("send-keys", "-t", pane, "Enter")...).CombinedOutput(); err != nil {
		t.Fatalf("send-keys Enter: %v: %s", err, out)
	}

	// The marker hook runs after the report hook for the same event, so once
	// it has written, the report has already been attempted.
	var ran string
	s.waitFor(t, func() bool {
		data, _ := os.ReadFile(marker)
		ran = string(data)
		return strings.Contains(ran, "Stop")
	})
	mu.Lock()
	defer mu.Unlock()
	if len(hits) != 0 {
		t.Fatalf("supervised agent reported into the caller's dispatch: %q\nhook env: %s", hits, ran)
	}
	if strings.Contains(ran, "d-canary") {
		t.Fatalf("agent hook saw the caller's dispatch id: %s", ran)
	}
}

// writeReportHooks installs Codex-shaped hooks in home: every event runs
// `leo dispatch report`, then appends the event and the dispatch identity it
// saw to marker.
func writeReportHooks(t *testing.T, home, marker string) {
	t.Helper()
	groups := map[string]any{}
	for _, event := range []string{"UserPromptSubmit", "Stop"} {
		groups[event] = []any{map[string]any{"hooks": []any{
			map[string]string{"type": "command", "command": leoBin + " dispatch report"},
			map[string]string{"type": "command", "command": fmt.Sprintf(`printf '%%s id=%%s config=%%s\n' %s "$LEO_DISPATCH_ID" "$LEO_CONFIG" >> '%s'`, event, marker)},
		}}}
	}
	data, err := json.Marshal(map[string]any{"hooks": groups})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "hooks.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}
