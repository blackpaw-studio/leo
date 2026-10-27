package agentmove

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func script(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func hookEnv(t *testing.T) HookEnv {
	t.Helper()
	return HookEnv{
		Agent: "web", Workspace: t.TempDir(), SessionID: "s-new",
		From: "studio", To: "laptop", Dir: t.TempDir(), Mode: "move",
		Last:        time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
		SourceAgent: "web", SourceWorkspace: "/src/ws", SourceSessionID: "s-old",
		AgentEnv: map[string]string{"FOO": "bar", "LEO_API_TOKEN": "secret", "OPENCODE_CONFIG_CONTENT": "{}"},
	}
}

func readEnvFile(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			out[k] = v
		}
	}
	return out
}

func TestHooksEnvAndCwd(t *testing.T) {
	t.Setenv("LEO_API_TOKEN", "daemon-secret")
	he := hookEnv(t)
	hook := script(t, t.TempDir(), "rec", `env > "$LEO_MOVE_DIR/env"; pwd -P > "$LEO_MOVE_DIR/pwd"`)
	if err := (ExecHooks{}).RunHooks(context.Background(), "pre_send", []string{hook}, time.Minute, he); err != nil {
		t.Fatalf("RunHooks: %v", err)
	}
	env := readEnvFile(t, filepath.Join(he.Dir, "env"))
	want := map[string]string{
		"FOO": "bar", "LEO_AGENT": "web", "LEO_WORKSPACE": he.Workspace, "LEO_SESSION_ID": "s-new",
		"LEO_MOVE_FROM": "studio", "LEO_MOVE_TO": "laptop", "LEO_MOVE_DIR": he.Dir,
		"LEO_MOVE_LAST": "2026-10-01T12:00:00Z", "LEO_MOVE_MODE": "move",
		"LEO_SOURCE_AGENT": "web", "LEO_SOURCE_WORKSPACE": "/src/ws", "LEO_SOURCE_SESSION_ID": "s-old",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
	for _, k := range []string{"LEO_API_TOKEN", "OPENCODE_CONFIG_CONTENT"} {
		if _, ok := env[k]; ok {
			t.Errorf("%s leaked into the hook env", k)
		}
	}
	if env["PATH"] == "" {
		t.Error("the hook lost PATH")
	}
	pwd, _ := os.ReadFile(filepath.Join(he.Dir, "pwd"))
	if real, _ := filepath.EvalSymlinks(he.Workspace); strings.TrimSpace(string(pwd)) != real {
		t.Errorf("cwd = %q, want the workspace %q", pwd, real)
	}

	// A copy's hooks and a first move see an empty LEO_MOVE_LAST.
	he.Mode = "copy"
	if err := (ExecHooks{}).RunHooks(context.Background(), "post_receive", []string{hook}, time.Minute, he); err != nil {
		t.Fatal(err)
	}
	if env := readEnvFile(t, filepath.Join(he.Dir, "env")); env["LEO_MOVE_LAST"] != "" || env["LEO_MOVE_MODE"] != "copy" {
		t.Errorf("copy: LEO_MOVE_LAST=%q LEO_MOVE_MODE=%q", env["LEO_MOVE_LAST"], env["LEO_MOVE_MODE"])
	}
}

func TestHookNonzeroAborts(t *testing.T) {
	hook := script(t, t.TempDir(), "fail", `echo "export failed: disk full" >&2; exit 3`)
	err := (ExecHooks{}).RunHooks(context.Background(), "pre_send", []string{hook}, time.Minute, hookEnv(t))
	if err == nil {
		t.Fatal("a failing hook did not abort")
	}
	for _, want := range []string{"pre_send", hook, "status 3", "export failed: disk full"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	missing := filepath.Join(t.TempDir(), "absent")
	if err := (ExecHooks{}).RunHooks(context.Background(), "pre_send", []string{missing}, time.Minute, hookEnv(t)); err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("missing hook = %v, want a spawn error naming it", err)
	}
}

func TestHookTimeout(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "never")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	// Opening a FIFO with no writer blocks: the hook never finishes on its own.
	hook := script(t, dir, "block", `echo started; read x < "`+fifo+`"`)
	err := (ExecHooks{}).RunHooks(context.Background(), "post_receive", []string{hook}, 50*time.Millisecond, hookEnv(t))
	if err == nil || !strings.Contains(err.Error(), "timed out after 50ms") || !strings.Contains(err.Error(), hook) {
		t.Fatalf("blocked hook = %v, want a timeout naming it", err)
	}
}

func TestHooksStopAtFirstFailure(t *testing.T) {
	dir := t.TempDir()
	he := hookEnv(t)
	first := script(t, dir, "a", `touch "$LEO_MOVE_DIR/a"`)
	failing := script(t, dir, "b", `exit 1`)
	never := script(t, dir, "c", `touch "$LEO_MOVE_DIR/c"`)
	err := (ExecHooks{}).RunHooks(context.Background(), "pre_send", []string{first, failing, never}, time.Minute, he)
	if err == nil || !strings.Contains(err.Error(), failing) {
		t.Fatalf("RunHooks = %v, want the second hook's failure", err)
	}
	if _, err := os.Stat(filepath.Join(he.Dir, "a")); err != nil {
		t.Error("the first hook did not run")
	}
	if _, err := os.Stat(filepath.Join(he.Dir, "c")); err == nil {
		t.Error("a hook after the failure ran")
	}
}
