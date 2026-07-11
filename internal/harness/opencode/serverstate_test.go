package opencode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureServerStateAllocatesAndPersists(t *testing.T) {
	home := t.TempDir()
	state, err := EnsureServerState(home, "leo-test-alloc", "anthropic/claude-sonnet-4-5")
	if err != nil {
		t.Fatalf("EnsureServerState: %v", err)
	}
	if state.Port == 0 {
		t.Error("Port = 0, want nonzero")
	}
	if len(state.Password) != 32 {
		t.Errorf("Password length = %d, want 32", len(state.Password))
	}
	for _, c := range state.Password {
		if !strings.ContainsRune("0123456789abcdef", c) {
			t.Fatalf("Password %q is not all lowercase hex", state.Password)
		}
	}
	if state.Model != "anthropic/claude-sonnet-4-5" {
		t.Errorf("Model = %q, want %q", state.Model, "anthropic/claude-sonnet-4-5")
	}

	stateDir := filepath.Join(home, "state", "opencode")
	dirInfo, err := os.Stat(stateDir)
	if err != nil {
		t.Fatalf("stat state dir: %v", err)
	}
	if dirInfo.Mode().Perm() != 0750 {
		t.Errorf("state dir mode = %v, want 0750", dirInfo.Mode().Perm())
	}

	path := filepath.Join(stateDir, "leo-test-alloc.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat state file: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("state file mode = %v, want 0600", info.Mode().Perm())
	}

	again, err := EnsureServerState(home, "leo-test-alloc", "anthropic/claude-sonnet-4-5")
	if err != nil {
		t.Fatalf("second EnsureServerState: %v", err)
	}
	if again != state {
		t.Errorf("second call = %+v, want identical %+v (port stability across restarts)", again, state)
	}
}

func TestEnsureServerStateDistinctSessionsGetDistinctState(t *testing.T) {
	home := t.TempDir()
	a, err := EnsureServerState(home, "leo-test-a", "")
	if err != nil {
		t.Fatalf("EnsureServerState a: %v", err)
	}
	b, err := EnsureServerState(home, "leo-test-b", "")
	if err != nil {
		t.Fatalf("EnsureServerState b: %v", err)
	}
	if a.Password == b.Password {
		t.Error("expected distinct passwords for distinct sessions")
	}
}

func TestLoadServerStateMissingErrors(t *testing.T) {
	home := t.TempDir()
	if _, err := LoadServerState(home, "leo-nonexistent"); err == nil {
		t.Fatal("expected an error loading a nonexistent server state")
	}
}

func TestServerStateURL(t *testing.T) {
	s := ServerState{Port: 4242}
	if got, want := s.URL(), "http://127.0.0.1:4242"; got != want {
		t.Errorf("URL() = %q, want %q", got, want)
	}
}
