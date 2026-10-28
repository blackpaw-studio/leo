package service

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/secretenv"
)

// switchableSecrets fails every resolution until value is set, counting calls.
type switchableSecrets struct {
	mu    sync.Mutex
	value string
	calls int
}

func (s *switchableSecrets) Resolve(r secretenv.Ref) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.value == "" {
		return "", &secretenv.ResolutionError{Ref: r, Reason: "item not found"}
	}
	return s.value, nil
}

func (s *switchableSecrets) set(v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.value = v
}

func (s *switchableSecrets) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// An agent adopted at boot without its secrets (they failed to resolve)
// must never relaunch without them: once the adopted session ends, each
// launch re-resolves, waits and retries while that fails, and launches with
// the value once it resolves.
func TestRelaunchAfterAdoptionReResolvesSecrets(t *testing.T) {
	origPoll, origBackoff := sessionPollInterval, initialBackoff
	sessionPollInterval, initialBackoff = time.Millisecond, time.Millisecond
	t.Cleanup(func() { sessionPollInterval, initialBackoff = origPoll, origBackoff })
	claude := &claudeLookupStub{path: "/bin/claude"}
	sv, logPath := tmuxArgvSupervisor(t, claude)
	secrets := &switchableSecrets{}
	sv.secretEnv = config.Expander{Secrets: secrets}
	ended := filepath.Join(lenientTempDir(t), "ended")
	script := "#!/bin/sh\necho \"$@\" >> " + logPath + "\ncase \"$*\" in *has-session*) [ -e " + ended + " ] && exit 1;; *show-options*) echo %1;; *show-environment*) exit 1;; *display-message*) echo 0;; *new-session*) echo %1;; esac\nexit 0\n"
	if err := os.WriteFile(sv.tmuxPath, []byte(script), 0o755); err != nil { //nolint:gosec // test fixture, needs +x
		t.Fatal(err)
	}
	err := sv.SpawnAgent(daemon.AgentSpawnSpec{
		Name: "survivor", WorkDir: lenientTempDir(t), Adopt: true,
		Env:        map[string]string{"PLAIN": "p"},
		EnvSecrets: map[string]secretenv.Ref{"OP_TOKEN": {Backend: secretenv.Keychain, Service: "OP"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForLog(t, logPath, "show-options")

	if err := os.WriteFile(ended, nil, 0o600); err != nil { // the adopted session is gone
		t.Fatal(err)
	}
	waitFor(t, "two failed resolutions for the relaunch", func() bool { return secrets.callCount() >= 2 })
	if strings.Contains(loggedLines(logPath), "new-session") {
		t.Fatal("relaunched without its secret")
	}

	secrets.set("S3CR3T-SENTINEL")
	if logged := waitForLog(t, logPath, "new-session"); !strings.Contains(logged, "OP_TOKEN=S3CR3T-SENTINEL") || !strings.Contains(logged, "PLAIN=p") {
		t.Errorf("relaunch env lacks the resolved secret:\n%s", logged)
	}
}
