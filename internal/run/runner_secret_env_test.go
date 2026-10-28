package run

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/history"
	"github.com/blackpaw-studio/leo/internal/leomcp"
	"github.com/blackpaw-studio/leo/internal/secretenv"
)

const secretSentinel = "S3CR3T-SENTINEL"

type fakeSecrets struct {
	calls  int
	value  string
	reason string
}

func (f *fakeSecrets) Resolve(r secretenv.Ref) (string, error) {
	f.calls++
	if f.reason != "" {
		return "", &secretenv.ResolutionError{Ref: r, Reason: f.reason}
	}
	return f.value, nil
}

func secretTaskConfig(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	ws := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "task.md"), []byte("test prompt"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		HomePath: dir,
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 15},
		Web:      config.WebConfig{Enabled: true, Port: 8888},
		Tasks: map[string]config.TaskConfig{
			"mytask": {
				PromptFile: "task.md",
				Schedule:   "0 * * * *",
				Enabled:    true,
				Env:        config.EnvMap{"OP_TOKEN": config.Secret(secretenv.Ref{Backend: secretenv.Keychain, Service: "OP"})},
			},
		},
	}
	if err := os.MkdirAll(cfg.StatePath(), 0o750); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// assertNoFileContains walks dir and fails on any file holding needle.
func assertNoFileContains(t *testing.T, dir, needle string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), needle) {
			t.Errorf("%s contains the secret value", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRunWithResolvesSecretIntoChildEnv(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()
	cfg := secretTaskConfig(t)
	// The child reports the value's length and prefix, never the value, so
	// the task log stays clean of it.
	execCommand = func(string, ...string) *exec.Cmd {
		return exec.Command("sh", "-c", `echo "len=${#OP_TOKEN}"; case "$OP_TOKEN" in S3CR3T-*) echo prefix-ok;; esac`)
	}
	f := &fakeSecrets{value: secretSentinel}
	if err := RunWith(cfg, "mytask", nil, leomcp.Server{}, config.Expander{Secrets: f}); err != nil {
		t.Fatalf("RunWith: %v", err)
	}
	logs, _ := filepath.Glob(filepath.Join(cfg.HomePath, "state", "logs", "mytask-*.log"))
	if len(logs) == 0 {
		t.Fatal("no task log")
	}
	data, _ := os.ReadFile(logs[0])
	if !strings.Contains(string(data), "len=15") || !strings.Contains(string(data), "prefix-ok") {
		t.Errorf("child did not receive the value:\n%s", data)
	}
	assertNoFileContains(t, cfg.HomePath, secretSentinel)
}

func TestRunWithSecretFailureNeverExecs(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()
	cfg := secretTaskConfig(t)
	execCommand = func(string, ...string) *exec.Cmd {
		t.Error("task process started despite a failed secret")
		return exec.Command("true")
	}
	err := RunWith(cfg, "mytask", nil, leomcp.Server{}, config.Expander{Secrets: &fakeSecrets{reason: "item not found"}})
	if err == nil || !strings.Contains(err.Error(), "OP_TOKEN: secret keychain(service=OP): item not found") {
		t.Fatalf("err = %v", err)
	}
	e := history.NewStore(cfg.HomePath).Get("mytask")
	if e == nil || e.Reason != history.ReasonFailure {
		t.Errorf("history = %+v, want a failure row", e)
	}
	assertNoFileContains(t, cfg.HomePath, secretSentinel)
}

// A child that prints its secret, in plain output and in its result's
// errors, leaves no trace of the value in anything leo keeps or returns:
// the task log, the returned error, the failure card, or the notify-on-fail
// child's prompt and appended output.
func TestRunWithRedactsSecretFromChildOutput(t *testing.T) {
	cards := stubFailureCards(t, nil)
	orig := execCommand
	defer func() { execCommand = orig }()
	cfg := secretTaskConfig(t)
	task := cfg.Tasks["mytask"]
	task.NotifyOnFail = true
	task.Channels = []string{"plugin:telegram@claude-plugins-official"}
	cfg.Tasks["mytask"] = task
	var argvs []string
	execCommand = func(_ string, args ...string) *exec.Cmd {
		argvs = append(argvs, strings.Join(args, " "))
		return exec.Command("sh", "-c", `echo "plain $OP_TOKEN"; printf '{"type":"result","subtype":"error_during_execution","is_error":true,"errors":["bad token %s"]}\n' "$OP_TOKEN"`)
	}
	err := RunWith(cfg, "mytask", nil, leomcp.Server{}, config.Expander{Secrets: &fakeSecrets{value: secretSentinel}})
	if err == nil || strings.Contains(err.Error(), secretSentinel) || !strings.Contains(err.Error(), "<secret OP_TOKEN>") {
		t.Errorf("returned error = %v", err)
	}
	if len(*cards) != 1 || strings.Contains((*cards)[0].Error, secretSentinel) || !strings.Contains((*cards)[0].Error, "<secret OP_TOKEN>") {
		t.Errorf("failure cards = %+v", *cards)
	}
	if len(argvs) != 2 {
		t.Fatalf("children = %d, want the task and its notify child", len(argvs))
	}
	for _, a := range argvs {
		if strings.Contains(a, secretSentinel) {
			t.Errorf("a child's argv carries the value: %s", a)
		}
	}
	logs, _ := filepath.Glob(filepath.Join(cfg.HomePath, "state", "logs", "mytask-*.log"))
	if len(logs) != 1 {
		t.Fatalf("logs = %v", logs)
	}
	if data, _ := os.ReadFile(logs[0]); strings.Count(string(data), "<secret OP_TOKEN>") < 4 {
		t.Errorf("log lacks the redacted value in both outputs:\n%s", data)
	}
	assertNoFileContains(t, cfg.HomePath, secretSentinel)
}
