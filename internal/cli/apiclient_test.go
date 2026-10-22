package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
)

// runClientCmd runs `leo client ...` against the config at path and returns
// what it printed.
func runClientCmd(t *testing.T, path string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	root := newRootCmd()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"--config", path, "client"}, args...))
	err := root.Execute()
	return out.String(), err
}

// startReloadStub answers /health and counts POST /config/reload; status is
// what the reload answers.
func startReloadStub(t *testing.T, home string, status int) *atomic.Int32 {
	t.Helper()
	var reloads atomic.Int32
	startStubDaemonSocket(t, home, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/health":
			_ = json.NewEncoder(w).Encode(daemon.Response{OK: true})
		case "/config/reload":
			reloads.Add(1)
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(daemon.Response{OK: status == http.StatusOK, Error: "boom"})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	return &reloads
}

func loadClients(t *testing.T, path string) map[string]config.APIClientConfig {
	t.Helper()
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.APIClients
}

func TestClientAddNeedsAScope(t *testing.T) {
	path, _ := newAgentWorktreeTestConfig(t)
	if _, err := runClientCmd(t, path, "add", "pipe"); err == nil {
		t.Fatal("add with neither --can-message nor --can-post succeeded")
	}
	if got := loadClients(t, path); len(got) != 0 {
		t.Fatalf("a refused add wrote %v", got)
	}
}

func TestClientAddSavesRepeatedCanPost(t *testing.T) {
	path, _ := newAgentWorktreeTestConfig(t)
	out, err := runClientCmd(t, path, "add", "pipe", "--can-post", "pipe-*", "--can-post", "wharf")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	got := loadClients(t, path)["pipe"]
	if !slices.Equal(got.CanPost, []string{"pipe-*", "wharf"}) || len(got.CanMessage) != 0 {
		t.Fatalf("saved client = %+v", got)
	}
	if !strings.Contains(out, "token:") {
		t.Errorf("output lacks the token: %s", out)
	}
}

func TestClientAddRejectsABadGlobWithoutWriting(t *testing.T) {
	path, _ := newAgentWorktreeTestConfig(t)
	if _, err := runClientCmd(t, path, "add", "pipe", "--can-post", "pipe-["); err == nil {
		t.Fatal("add with a malformed glob succeeded")
	}
	if got := loadClients(t, path); len(got) != 0 {
		t.Fatalf("a refused add wrote %v", got)
	}
}

func TestClientAddAndRemoveReloadARunningDaemon(t *testing.T) {
	path, home := newAgentWorktreeTestConfig(t)
	reloads := startReloadStub(t, home, http.StatusOK)

	out, err := runClientCmd(t, path, "add", "pipe", "--can-post", "pipe-*")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if reloads.Load() != 1 || !strings.Contains(out, "active now") {
		t.Fatalf("add: reloads = %d, output:\n%s", reloads.Load(), out)
	}
	if strings.Contains(out, "restart") {
		t.Errorf("add still tells the operator to restart:\n%s", out)
	}

	out, err = runClientCmd(t, path, "rm", "pipe")
	if err != nil {
		t.Fatalf("rm: %v\n%s", err, out)
	}
	if reloads.Load() != 2 || !strings.Contains(out, "revoked") {
		t.Fatalf("rm: reloads = %d, output:\n%s", reloads.Load(), out)
	}
	if strings.Contains(out, "NOT YET REVOKED") {
		t.Errorf("rm still warns the token is live:\n%s", out)
	}
}

func TestClientAddWithoutADaemonSaysItAppliesAtNextStart(t *testing.T) {
	path, _ := newAgentWorktreeTestConfig(t)
	out, err := runClientCmd(t, path, "add", "pipe", "--can-post", "pipe-*")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if !strings.Contains(out, "applies when the daemon next starts") {
		t.Errorf("output lacks the next-start note:\n%s", out)
	}
}

func TestClientRemoveFailsLoudlyWhenTheReloadFails(t *testing.T) {
	path, home := newAgentWorktreeTestConfig(t)
	if _, err := runClientCmd(t, path, "add", "pipe", "--can-post", "pipe-*"); err != nil {
		t.Fatal(err)
	}
	startReloadStub(t, home, http.StatusInternalServerError)

	out, err := runClientCmd(t, path, "rm", "pipe")
	if err == nil {
		t.Fatalf("rm with a failing reload succeeded:\n%s", out)
	}
	if !strings.Contains(err.Error(), "not revoked") || !strings.Contains(err.Error(), "leo service reload") {
		t.Errorf("error = %q, want it to say the revocation is not active and name the fix", err)
	}
	if got := loadClients(t, path); len(got) != 0 {
		t.Errorf("the client is still in the config: %v", got)
	}
}

func TestClientListShowsCanPost(t *testing.T) {
	path, _ := newAgentWorktreeTestConfig(t)
	if _, err := runClientCmd(t, path, "add", "pipe", "--can-post", "pipe-*"); err != nil {
		t.Fatal(err)
	}
	out, err := runClientCmd(t, path, "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "CAN POST") || !strings.Contains(out, "pipe-*") {
		t.Errorf("list output:\n%s", out)
	}
}
