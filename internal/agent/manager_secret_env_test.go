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

func TestSpawnPersistsRefsNotValues(t *testing.T) {
	f := &fakeSecrets{value: secretSentinel}
	m, sup, home := secretManager(t, f, config.EnvMap{"OP_TOKEN": config.Secret(opRef)})
	if _, err := m.Spawn(context.Background(), SpawnSpec{Template: "coding", Repo: "x"}); err != nil {
		t.Fatal(err)
	}
	if sup.spawnCall.Env["OP_TOKEN"] != secretSentinel {
		t.Fatalf("launch env lacks the value")
	}
	data, err := os.ReadFile(agentstore.FilePath(home))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secretSentinel) || !strings.Contains(string(data), `"env_secrets"`) {
		t.Fatalf("agents.json:\n%s", data)
	}
}

func TestHydrateEnvResolvesStoredRefs(t *testing.T) {
	rec := agentstore.Record{Name: "a", Env: map[string]string{"P": "p"}, EnvSecrets: map[string]secretenv.Ref{"OP_TOKEN": opRef}}
	env, err := HydrateEnv(rec, config.Expander{Secrets: &fakeSecrets{value: "v"}})
	if err != nil || env["OP_TOKEN"] != "v" || env["P"] != "p" {
		t.Fatalf("HydrateEnv = %v, %v", env, err)
	}
	_, err = HydrateEnv(rec, config.Expander{Secrets: &fakeSecrets{reason: "item not found"}})
	if err == nil || !strings.Contains(err.Error(), "OP_TOKEN: secret keychain(service=OP): item not found") {
		t.Fatalf("err = %v", err)
	}
}

// failingResolver fails the test if anything resolves through it.
type failingResolver struct{ t *testing.T }

func (r failingResolver) Resolve(secretenv.Ref) (string, error) {
	r.t.Error("StaleAgents resolved a secret")
	return "", errors.New("unexpected")
}

func TestStaleAgentsComparesRefsWithoutResolving(t *testing.T) {
	f := &fakeSecrets{value: secretSentinel}
	m, sup, home := secretManager(t, f, config.EnvMap{"OP_TOKEN": config.Secret(opRef)})
	if _, err := m.Spawn(context.Background(), SpawnSpec{Template: "coding", Repo: "x"}); err != nil {
		t.Fatal(err)
	}
	_ = sup
	m.env.Secrets = failingResolver{t}
	if stale := m.StaleAgents(); len(stale) != 0 {
		t.Fatalf("unchanged ref reported stale: %+v", stale)
	}

	cfg, _ := m.cfgLoader()
	tmpl := cfg.Templates["coding"]
	tmpl.Env = config.EnvMap{"OP_TOKEN": config.Secret(secretenv.Ref{Backend: secretenv.Keychain, Service: "OTHER"})}
	cfg.Templates["coding"] = tmpl
	stale := m.StaleAgents()
	if len(stale) != 1 || len(stale[0].EnvChanged) != 1 || stale[0].EnvChanged[0] != "OP_TOKEN" {
		t.Fatalf("stale = %+v, want EnvChanged [OP_TOKEN]", stale)
	}
	_ = home
}

func TestResetReResolvesSecrets(t *testing.T) {
	f := &fakeSecrets{value: "first"}
	m, sup, _ := secretManager(t, f, config.EnvMap{"OP_TOKEN": config.Secret(opRef)})
	rec, err := m.Spawn(context.Background(), SpawnSpec{Template: "coding", Repo: "x"})
	if err != nil {
		t.Fatal(err)
	}
	f.value = "rotated"
	if err := m.Reset(rec.Name); err != nil {
		t.Fatal(err)
	}
	if got := sup.spawnCall.Env["OP_TOKEN"]; got != "rotated" {
		t.Errorf("reset launch OP_TOKEN = %q, want rotated", got)
	}
}

func TestResetSecretFailureLeavesAgentRunning(t *testing.T) {
	f := &fakeSecrets{value: "first"}
	m, sup, _ := secretManager(t, f, config.EnvMap{"OP_TOKEN": config.Secret(opRef)})
	rec, err := m.Spawn(context.Background(), SpawnSpec{Template: "coding", Repo: "x"})
	if err != nil {
		t.Fatal(err)
	}
	f.reason = "item not found"
	if err := m.Reset(rec.Name); err == nil || !strings.Contains(err.Error(), "OP_TOKEN") {
		t.Fatalf("err = %v", err)
	}
	if len(sup.stopCalls) != 0 {
		t.Errorf("reset stopped the agent before its secret resolved: %v", sup.stopCalls)
	}
}

func TestRestartAndStartKeepSecretsOutOfAgentsJSON(t *testing.T) {
	f := &fakeSecrets{value: secretSentinel}
	m, sup, home := secretManager(t, f, config.EnvMap{"OP_TOKEN": config.Secret(opRef)})
	rec, err := m.Spawn(context.Background(), SpawnSpec{Template: "coding", Repo: "x"})
	if err != nil {
		t.Fatal(err)
	}
	assertClean := func(step string) {
		t.Helper()
		data, err := os.ReadFile(agentstore.FilePath(home))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), secretSentinel) || !strings.Contains(string(data), `"env_secrets"`) {
			t.Fatalf("after %s agents.json:\n%s", step, data)
		}
		if sup.spawnCall.Env["OP_TOKEN"] != secretSentinel {
			t.Fatalf("after %s the launch env lacks the value", step)
		}
	}
	if err := m.Restart(rec.Name); err != nil {
		t.Fatal(err)
	}
	assertClean("restart")
	if err := m.Stop(rec.Name, StopOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(rec.Name); err != nil {
		t.Fatal(err)
	}
	assertClean("start")
}

// An agent moved or copied here starts with this host's resolved secrets
// and stores their references, never the values: also when its first start
// fails and the record stays behind dormant.
func TestReceivePersistsRefsNotValues(t *testing.T) {
	for name, spawnErr := range map[string]error{"started": nil, "start failed": errors.New("tmux exploded")} {
		t.Run(name, func(t *testing.T) {
			f := &fakeSecrets{value: secretSentinel}
			m, sup, home := secretManager(t, f, config.EnvMap{"OP_TOKEN": config.Secret(opRef)})
			sup.spawnErr = spawnErr
			err := m.Receive(context.Background(), ReceiveSpec{Record: arriving(t.TempDir()), From: "studio", At: receiveAt})
			if (err != nil) != (spawnErr != nil) {
				t.Fatalf("Receive = %v", err)
			}
			if spawnErr == nil && sup.spawnCall.Env["OP_TOKEN"] != secretSentinel {
				t.Fatalf("launch env lacks the value")
			}
			if rec := loadRec(t, home, "leo-x"); rec.EnvSecrets["OP_TOKEN"].Service != opRef.Service {
				t.Fatalf("record refs = %v", rec.EnvSecrets)
			}
			data, err := os.ReadFile(agentstore.FilePath(home))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), secretSentinel) {
				t.Fatalf("agents.json holds the value:\n%s", data)
			}
		})
	}
}
