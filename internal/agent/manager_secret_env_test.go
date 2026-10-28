package agent

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/secretenv"
)

const secretSentinel = "S3CR3T-SENTINEL"

var opRef = secretenv.Ref{Backend: secretenv.Keychain, Service: "OP"}

// fakeSecrets resolves every reference to value, or fails with reason.
type fakeSecrets struct {
	mu     sync.Mutex
	calls  int
	value  string
	reason string
}

func (f *fakeSecrets) Resolve(r secretenv.Ref) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.reason != "" {
		return "", &secretenv.ResolutionError{Ref: r, Reason: f.reason}
	}
	return f.value, nil
}

func secretManager(t *testing.T, f *fakeSecrets, tmplEnv config.EnvMap, edits ...func(*config.Config)) (*Manager, *capturingSupervisor, string) {
	t.Helper()
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home, ws := t.TempDir(), t.TempDir()
	cfg := envTestConfig(home, ws, "/a", "/b")
	cfg.Templates["coding"] = config.TemplateConfig{Workspace: ws, Environments: []string{"acct-a"}, Env: tmplEnv}
	for _, edit := range edits {
		edit(cfg)
	}
	sup := &capturingSupervisor{}
	m := envManager(cfg, sup)
	m.env = config.Expander{Lookup: func(string) (string, bool) { return "", false }, Secrets: f}
	return m, sup, home
}

func TestSpawnResolvesSecretIntoLaunchEnv(t *testing.T) {
	f := &fakeSecrets{value: secretSentinel}
	m, sup, _ := secretManager(t, f, config.EnvMap{"OP_TOKEN": config.Secret(opRef)})
	if _, err := m.Spawn(context.Background(), SpawnSpec{Template: "coding", Repo: "x"}); err != nil {
		t.Fatal(err)
	}
	if got := sup.spawnCall.Env["OP_TOKEN"]; got != secretSentinel {
		t.Errorf("launch env OP_TOKEN = %q", got)
	}
}

func TestSpawnSecretFailureFailsCleanly(t *testing.T) {
	f := &fakeSecrets{reason: "item not found"}
	m, sup, home := secretManager(t, f, config.EnvMap{"OP_TOKEN": config.Secret(opRef)})
	_, err := m.Spawn(context.Background(), SpawnSpec{Template: "coding", Repo: "x"})
	var se *config.SecretResolutionError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v (%T), want *config.SecretResolutionError", err, err)
	}
	if want := "templates.coding.env.OP_TOKEN: secret keychain(service=OP): item not found"; !strings.Contains(err.Error(), want) {
		t.Errorf("err = %q, want it to contain %q", err, want)
	}
	if sup.spawnCall != nil || len(sup.reservations) != 0 {
		t.Errorf("side effects: spawn %v reservations %v", sup.spawnCall, sup.reservations)
	}
	if _, statErr := os.Stat(agentstore.FilePath(home)); statErr == nil {
		recs, _ := agentstore.Load(agentstore.FilePath(home))
		if len(recs) != 0 {
			t.Errorf("records written: %v", recs)
		}
	}
}

func TestSpawnEnvShadowsNamedSecretWithoutResolving(t *testing.T) {
	f := &fakeSecrets{reason: "not supported on linux"}
	m, sup, _ := secretManager(t, f, nil, func(cfg *config.Config) {
		cfg.Environments["acct-a"] = config.EnvMap{"OP_TOKEN": config.Secret(opRef), "CLAUDE_CONFIG_DIR": config.Literal("/a")}
	})
	if _, err := m.Spawn(context.Background(), SpawnSpec{Template: "coding", Repo: "x", Env: map[string]string{"OP_TOKEN": "local"}}); err != nil {
		t.Fatal(err)
	}
	if got := sup.spawnCall.Env["OP_TOKEN"]; got != "local" {
		t.Errorf("OP_TOKEN = %q, want local", got)
	}
	if f.calls != 0 {
		t.Errorf("resolver called %d times for a shadowed ref", f.calls)
	}
}
