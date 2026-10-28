package consult

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
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

const rotatedSentinel = "R0TATED-SENTINEL"

// What a headless child prints never reaches a wait result, an error or
// the recording with a resolved secret value in it — on the first turn
// and on a follow-up that resolved a rotated value.
func TestHeadlessDispatchRedactsSecretsFromOutput(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	f := &fakeSecrets{value: secretSentinel}
	recDir := t.TempDir()
	d := NewDispatcher(NewFileRecorder(recDir))
	d.Env = config.Expander{Secrets: f}
	turn := 0
	d.ExecCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		turn++
		if turn == 1 {
			return exec.CommandContext(ctx, "sh", "-c", `printf 'token is %s\n{"type":"result","session_id":"sid-1","result":"got %s","is_error":false}\n' "$OP_TOKEN" "$OP_TOKEN"`)
		}
		return exec.CommandContext(ctx, "sh", "-c", `printf '{"type":"result","session_id":"sid-1","result":"%s then %s","is_error":true,"errors":["%s"]}\n' "$OP_TOKEN" "`+secretSentinel+`" "$OP_TOKEN"; exit 1`)
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
	first := d.Wait(context.Background(), []string{started.ID}, 5*time.Second)
	if len(first) != 1 || !strings.Contains(first[0].Text, "got <secret OP_TOKEN>") {
		t.Fatalf("first turn = %+v, want the redacted result", first)
	}
	f.set(rotatedSentinel)
	sent, err := d.SendWithConfig(context.Background(), cfg, started.ID, "again")
	if err != nil {
		t.Fatal(err)
	}
	second := d.Wait(context.Background(), []string{sent.TurnID}, 5*time.Second)
	if len(second) != 1 || second[0].Text+second[0].Err == "" {
		t.Fatalf("second turn = %+v, want its output", second)
	}

	for _, e := range append(first, second...) {
		assertNoSentinel(t, "wait entry", e.Text+" "+e.Err)
	}
	if turn != 2 {
		t.Fatalf("launched %d processes, want 2", turn)
	}
	err = filepath.WalkDir(recDir, func(path string, de fs.DirEntry, err error) error {
		if err != nil || de.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		assertNoSentinel(t, path, string(data))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func assertNoSentinel(t *testing.T, where, text string) {
	t.Helper()
	for _, v := range []string{secretSentinel, rotatedSentinel} {
		if strings.Contains(text, v) {
			t.Errorf("%s holds the secret value %s:\n%s", where, v, text)
		}
	}
}

// An interactive turn's text — the message Claude stopped on, including a
// waiting turn's earlier one — is redacted before it is recorded or waited on.
func TestInteractiveTurnTextRedactsSecrets(t *testing.T) {
	now := time.Date(2026, time.October, 28, 12, 0, 0, 0, time.UTC)
	d := NewDispatcher(newFakeRecorder())
	d.now = sharedClock(&now)
	d.Env = config.Expander{Secrets: &fakeSecrets{value: secretSentinel}}
	d.SetInteractiveRuntime(&fakeInteractiveRuntime{arm: true, empty: true})
	cfg := testConfig()
	tmpl := cfg.Templates["claude"]
	tmpl.Env = config.EnvMap{"OP_TOKEN": config.Secret(opRef)}
	cfg.Templates["claude"] = tmpl
	started, err := d.Start(context.Background(), cfg, Request{Template: "claude", Prompt: "hello", Cwd: t.TempDir(), Mode: ModeInteractive})
	if err != nil {
		t.Fatal(err)
	}
	id := started.ID
	waitForArmed(t, d, id)
	if err := d.Report(id, claudeHook(t, "submit-1", "UserPromptSubmit", dispatchPreamble+" hello")); err != nil {
		t.Fatal(err)
	}
	done := make(chan []Entry, 1)
	go func() { done <- d.Wait(context.Background(), []string{id + "#1"}, time.Minute) }()
	if err := d.Report(id, claudeStop(t, "stop-1", "waiting on "+secretSentinel, shellAndMonitor)); err != nil {
		t.Fatal(err)
	}
	advanceClock(&now, time.Minute)
	if err := d.Report(id, claudeHook(t, "submit-2", "UserPromptSubmit", taskNotification)); err != nil {
		t.Fatal(err)
	}
	advanceClock(&now, time.Second)
	if err := d.Report(id, claudeStop(t, "stop-2", "used "+secretSentinel, map[string]any{"background_tasks": []any{}, "session_crons": []any{}})); err != nil {
		t.Fatal(err)
	}
	select {
	case entries := <-done:
		if len(entries) != 1 || entries[0].Text != "waiting on <secret OP_TOKEN>\n\nused <secret OP_TOKEN>" {
			t.Fatalf("entries=%+v", entries)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not resolve")
	}
	rec, _ := d.Get(id)
	for _, turn := range rec.Turns {
		assertNoSentinel(t, "turn "+turn.TurnID, turn.Text+" "+strings.Join(turn.WaitText, " "))
	}
}
