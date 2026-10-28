package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

func moveReceiveHarness(t *testing.T, status int, out string, err error) (*bytes.Buffer, *[]string) {
	t.Helper()
	home := t.TempDir()
	path := home + "/leo.yaml"
	if err := config.Save(path, &config.Config{HomePath: home, Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 10}}); err != nil {
		t.Fatal(err)
	}
	oldCfg, oldFn, oldIn, oldOut := cfgFile, moveVerbFn, agentStdin, agentStdout
	t.Cleanup(func() { cfgFile, moveVerbFn, agentStdin, agentStdout = oldCfg, oldFn, oldIn, oldOut })
	cfgFile = path
	var calls []string
	moveVerbFn = func(_ context.Context, homePath, verb string, body []byte) (int, []byte, error) {
		calls = append(calls, homePath+"|"+verb+"|"+string(body))
		return status, []byte(out), err
	}
	agentStdin = strings.NewReader(`{"protocol":1,"mode":"move"}`)
	var buf bytes.Buffer
	agentStdout = &buf
	return &buf, &calls
}

func TestMoveReceivePipesStdinToSocket(t *testing.T) {
	out, calls := moveReceiveHarness(t, 200, `{"ok":true}`, nil)
	cmd := newAgentMoveReceiveCmd()
	cmd.SetArgs([]string{"preflight"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(*calls) != 1 || !strings.HasSuffix((*calls)[0], `|preflight|{"protocol":1,"mode":"move"}`) {
		t.Fatalf("daemon calls = %v", *calls)
	}
	if strings.TrimSpace(out.String()) != `{"ok":true}` {
		t.Fatalf("stdout = %q", out)
	}
}

func TestMoveReceiveRefusalExitsNonzeroWithBody(t *testing.T) {
	out, _ := moveReceiveHarness(t, 409, `{"error":"submodule_torn","message":"libs/a"}`, nil)
	cmd := newAgentMoveReceiveCmd()
	cmd.SetArgs([]string{"commit"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	if err := cmd.Execute(); err == nil {
		t.Fatal("a refused verb exited zero")
	}
	if strings.TrimSpace(out.String()) != `{"error":"submodule_torn","message":"libs/a"}` {
		t.Fatalf("stdout = %q", out)
	}

	out, _ = moveReceiveHarness(t, 0, "", errors.New("connecting to daemon: no such file"))
	cmd = newAgentMoveReceiveCmd()
	cmd.SetArgs([]string{"status"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	if err := cmd.Execute(); err == nil || !strings.Contains(out.String(), `"error":"daemon_unreachable"`) {
		t.Fatalf("unreachable daemon: err=%v stdout=%q", err, out)
	}

	_, calls := moveReceiveHarness(t, 200, "{}", nil)
	cmd = newAgentMoveReceiveCmd()
	cmd.SetArgs([]string{"rm"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	if err := cmd.Execute(); err == nil || len(*calls) != 0 {
		t.Fatalf("unknown verb reached the daemon: %v %v", err, *calls)
	}
}
