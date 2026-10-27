package opencode

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/blackpaw-studio/leo/internal/harness"
)

type recordedCmd struct {
	name string
	args []string
	cmd  *exec.Cmd
}

// recordExec records each command and runs a stand-in printing stdout.
func recordExec(t *testing.T, stdout string) *[]recordedCmd {
	t.Helper()
	var calls []recordedCmd
	withExecCommand(t, func(ctx context.Context, name string, args ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", `printf '%s' "$1"`, "sh", stdout)
		calls = append(calls, recordedCmd{name: name, args: args, cmd: cmd})
		return cmd
	})
	return &calls
}

func hasEnv(cmd *exec.Cmd, kv string) bool { return slices.Contains(cmd.Env, kv) }

func TestOpencodeExportArgv(t *testing.T) {
	calls := recordExec(t, `{"info":{"id":"ses_1"}}`)
	stage, ws := t.TempDir(), t.TempDir()
	mc := harness.MoveContext{Env: map[string]string{"XDG_DATA_HOME": "/xdg"}, Workspace: ws, SessionIDs: []string{"ses_1"}}
	if err := (Opencode{}).PrepareSend(context.Background(), mc, stage); err != nil {
		t.Fatalf("PrepareSend: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(*calls))
	}
	c := (*calls)[0]
	if c.name != "opencode" || !reflect.DeepEqual(c.args, []string{"export", "ses_1"}) {
		t.Fatalf("argv = %s %v", c.name, c.args)
	}
	if c.cmd.Dir != ws || !hasEnv(c.cmd, "XDG_DATA_HOME=/xdg") {
		t.Fatalf("Dir = %q, XDG_DATA_HOME set = %v", c.cmd.Dir, hasEnv(c.cmd, "XDG_DATA_HOME=/xdg"))
	}
	got, err := os.ReadFile(filepath.Join(stage, "harness", "opencode", "ses_1.json"))
	if err != nil || string(got) != `{"info":{"id":"ses_1"}}` {
		t.Fatalf("staged export = %q, %v", got, err)
	}
}

func TestOpencodeImportArgv(t *testing.T) {
	stage := t.TempDir()
	file := filepath.Join(stage, "harness", "opencode", "ses_1.json")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := recordExec(t, "")
	ws := t.TempDir()
	mc := harness.MoveContext{Env: map[string]string{"XDG_DATA_HOME": "/dst"}, Workspace: ws, SessionIDs: []string{"ses_1"}}
	if err := (Opencode{}).FinishReceive(context.Background(), mc, stage); err != nil {
		t.Fatalf("FinishReceive: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(*calls))
	}
	c := (*calls)[0]
	if c.name != "opencode" || !reflect.DeepEqual(c.args, []string{"import", file}) {
		t.Fatalf("argv = %s %v", c.name, c.args)
	}
	// import rewrites the session's directory to its cwd, so it runs in the
	// destination workspace.
	if c.cmd.Dir != ws || !hasEnv(c.cmd, "XDG_DATA_HOME=/dst") {
		t.Fatalf("Dir = %q env = %v", c.cmd.Dir, c.cmd.Env)
	}
}

func TestOpencodeLocalCopySkipsImport(t *testing.T) {
	calls := recordExec(t, "{}")
	stage := t.TempDir()
	mc := harness.MoveContext{Workspace: "/ws/src", SessionIDs: []string{"ses_1"}, Copy: true, Local: true}
	if err := (Opencode{}).PrepareSend(context.Background(), mc, stage); err != nil {
		t.Fatal(err)
	}
	mc.Workspace = "/ws/src-copy"
	if err := (Opencode{}).FinishReceive(context.Background(), mc, stage); err != nil {
		t.Fatal(err)
	}
	// Re-importing on the same host would move the source session to the
	// copy's directory; the shared DB already has it.
	if len(*calls) != 0 {
		t.Fatalf("local copy ran %v", *calls)
	}
}

func TestOpencodeRefreshStripsFork(t *testing.T) {
	fork := (Opencode{}).ForkArgs([]string{"--model", "m", "-s", "old"}, "S", "ignored")
	if want := []string{"--model", "m", "-s", "S", "--fork"}; !reflect.DeepEqual(fork, want) {
		t.Fatalf("ForkArgs = %v, want %v", fork, want)
	}
	if got := refreshSessionArgs(fork, ""); !reflect.DeepEqual(got, fork) {
		t.Fatalf("refresh before discovery = %v, want the pending fork", got)
	}
	if got := refreshSessionArgs(fork, "N"); !reflect.DeepEqual(got, []string{"--model", "m", "-s", "N"}) {
		t.Fatalf("refresh after discovery = %v", got)
	}
	if got, _ := recoverQuickExitArgs(fork); !reflect.DeepEqual(got, []string{"--model", "m"}) {
		t.Fatalf("recover = %v", got)
	}
}
