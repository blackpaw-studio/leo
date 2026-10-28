package consult

import (
	"context"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/secretenv"
)

const secretSentinel = "S3CR3T-SENTINEL"

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

func (f *fakeSecrets) set(value string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.value = value
}

var opRef = secretenv.Ref{Backend: secretenv.Keychain, Service: "OP"}

func TestInteractiveDispatchResolvesSecretIntoLaunchEnv(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	cwd := t.TempDir()
	cfg := &config.Config{
		HomePath:  t.TempDir(),
		Templates: map[string]config.TemplateConfig{"claude": {Harness: "claude", Model: "sonnet", Env: config.EnvMap{"OP_TOKEN": config.Secret(opRef)}}},
	}
	r := NewInteractiveRuntime("/tmp/leo.yaml", func() (*config.Config, error) { return cfg, nil }, nil, "tmux", "/opt/leo")
	var launch []string
	r.ExecCommandContext = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		if slices.Contains(args, "new-window") {
			launch = append([]string(nil), args...)
			return exec.Command("echo", "%42")
		}
		if slices.Contains(args, "has-session") {
			return exec.Command("false")
		}
		return exec.Command("true")
	}

	r.Env = config.Expander{Secrets: &fakeSecrets{reason: "item not found"}}
	_, _, err := r.Launch(context.Background(), LaunchRequest{ID: "d-fail", Template: "claude", Cwd: cwd, Dispatched: true})
	if err == nil || !strings.Contains(err.Error(), "env.OP_TOKEN: secret keychain(service=OP): item not found") {
		t.Fatalf("err = %v, want the named reference", err)
	}
	if launch != nil {
		t.Fatalf("pane launched despite a failed secret: %v", launch)
	}

	r.Env = config.Expander{Secrets: &fakeSecrets{value: secretSentinel}}
	if _, _, err := r.Launch(context.Background(), LaunchRequest{ID: "d-ok", Template: "claude", Cwd: cwd, Dispatched: true}); err != nil {
		t.Fatalf("a later dispatch failed: %v", err)
	}
	if !containsSeq(launch, "-e", "OP_TOKEN="+secretSentinel) {
		t.Fatalf("launch argv lacks the secret: %v", launch)
	}
}

func TestHeadlessContinuationReResolvesSecrets(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	f := &fakeSecrets{value: "first"}
	d := NewDispatcher(NewFileRecorder(t.TempDir()))
	d.Env = config.Expander{Secrets: f}
	var cmds []*exec.Cmd
	d.ExecCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "printf", "%s", `{"type":"result","session_id":"sid-1","result":"ok","is_error":false}`)
		cmds = append(cmds, cmd)
		return cmd
	}
	cfg := envTestConfig()
	cfg.Web.Enabled = true
	tmpl := cfg.Templates["claude"]
	tmpl.Env = config.EnvMap{"OP_TOKEN": config.Secret(opRef)}
	cfg.Templates["claude"] = tmpl
	started, err := d.Start(context.Background(), cfg, Request{Template: "claude", Prompt: "first", Cwd: t.TempDir(), Kind: "dispatch"})
	if err != nil {
		t.Fatal(err)
	}
	d.Wait(context.Background(), []string{started.ID}, 5*time.Second)
	f.set("rotated")
	sent, err := d.SendWithConfig(context.Background(), cfg, started.ID, "again")
	if err != nil {
		t.Fatal(err)
	}
	d.Wait(context.Background(), []string{sent.TurnID}, 5*time.Second)
	if len(cmds) != 2 {
		t.Fatalf("launched %d processes, want 2", len(cmds))
	}
	for i, want := range []string{"first", "rotated"} {
		if !slices.Contains(cmds[i].Env, "OP_TOKEN="+want) {
			t.Errorf("turn %d env lacks OP_TOKEN=%s", i+1, want)
		}
	}
}
