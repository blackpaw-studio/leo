package cli

import (
	"context"
	"encoding/base64"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

func encodeBase64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// stubStdinIsTerminal forces stdinIsTerminal() to the given value for the
// duration of the test. Tests run from CI or a pipe without this override
// would bail out of the picker with "stdin is not a terminal" before we can
// exercise the interesting branches.
func stubStdinIsTerminal(t *testing.T, v bool) {
	t.Helper()
	old := stdinIsTerminal
	stdinIsTerminal = func() bool { return v }
	t.Cleanup(func() { stdinIsTerminal = old })
}

func TestRunAttachPickerRejectsNonTTY(t *testing.T) {
	stubStdinIsTerminal(t, false)
	cfg := &config.Config{HomePath: t.TempDir()}
	err := runAttachPicker(context.Background(), cfg, config.HostResolution{Localhost: true}, attachOptions{})
	if err == nil || !strings.Contains(err.Error(), "not a terminal") {
		t.Fatalf("want non-TTY error, got %v", err)
	}
}

func TestRunAttachPickerErrorsWhenNoSessions(t *testing.T) {
	stubStdinIsTerminal(t, true)
	// Localhost + no processes + daemon unreachable (HomePath with no
	// socket) = zero choices, which should surface as a clean error rather
	// than dropping into an empty picker.
	cfg := &config.Config{HomePath: t.TempDir()}
	err := runAttachPicker(context.Background(), cfg, config.HostResolution{Localhost: true}, attachOptions{})
	if err == nil || !strings.Contains(err.Error(), "no attachable sessions") {
		t.Fatalf("want no-sessions error, got %v", err)
	}
}

func TestRunAttachPickerAutoAttachesSingle(t *testing.T) {
	stubStdinIsTerminal(t, true)
	stubOutsideTmux(t)
	stubTmuxLookPath(t, "/usr/bin/tmux", nil)

	cfg := &config.Config{
		HomePath: t.TempDir(),
		Processes: map[string]config.ProcessConfig{
			"only": {Enabled: true, Model: "sonnet"},
		},
	}

	var execed bool
	var execedArgv []string
	old := agentSyscallExec
	agentSyscallExec = func(argv0 string, argv []string, envv []string) error {
		execed = true
		execedArgv = argv
		return nil
	}
	t.Cleanup(func() { agentSyscallExec = old })

	if err := runAttachPicker(context.Background(), cfg, config.HostResolution{Localhost: true}, attachOptions{}); err != nil {
		t.Fatalf("runAttachPicker: %v", err)
	}
	if !execed {
		t.Fatalf("single-session path should bypass picker and exec tmux directly")
	}
	want := []string{"tmux", "-L", "leo", "attach", "-t", "leo-only"}
	if !equalStrings(execedArgv, want) {
		t.Errorf("argv = %v, want %v", execedArgv, want)
	}
}

func TestLocalAttachChoicesSortsProcesses(t *testing.T) {
	cfg := &config.Config{
		HomePath: t.TempDir(),
		Processes: map[string]config.ProcessConfig{
			"zulu":  {Enabled: true, Model: "sonnet"},
			"alpha": {Enabled: true, Model: "sonnet"},
			"mike":  {Enabled: true, Model: "sonnet"},
		},
	}
	out := localAttachChoices(context.Background(), cfg)
	if len(out) != 3 {
		t.Fatalf("want 3 choices, got %d", len(out))
	}
	wantOrder := []string{"leo-alpha", "leo-mike", "leo-zulu"}
	for i, c := range out {
		if c.session != wantOrder[i] {
			t.Errorf("choice[%d].session = %q, want %q", i, c.session, wantOrder[i])
		}
	}
}

// fakeRemoteExec wires agentExecCommand to a synthetic process whose stdout is
// a canned tmux list-sessions reply. Exercised via the `go test -run` trick
// that re-executes the test binary with TestHelperProcess set.
func fakeRemoteExec(t *testing.T, stdout string, exitCode int) {
	t.Helper()
	old := agentExecCommand
	agentExecCommand = func(name string, args ...string) *exec.Cmd {
		// `printf '%s'` would eat backslashes; base64-pipe keeps the payload
		// byte-exact including embedded newlines.
		encoded := encodeBase64(stdout)
		script := fmt.Sprintf("printf '%%s' '%s' | base64 -d; exit %d", encoded, exitCode)
		return exec.Command("sh", "-c", script)
	}
	t.Cleanup(func() { agentExecCommand = old })
}

func TestRemoteAttachChoicesFiltersLeoPrefix(t *testing.T) {
	fakeRemoteExec(t, "leo-primary\nleo-scratch\nunrelated-session\n", 0)

	res := config.HostResolution{
		Name: "prod",
		Host: config.HostConfig{SSH: "user@prod.example.com"},
	}
	out, err := remoteAttachChoices(res)
	if err != nil {
		t.Fatalf("remoteAttachChoices: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("want 2 leo- sessions, got %d: %+v", len(out), out)
	}
	if out[0].session != "leo-primary" || out[1].session != "leo-scratch" {
		t.Errorf("unexpected session ordering: %+v", out)
	}
}

// ssh concatenates argv into a single string and pipes it to the remote
// `$SHELL -c`, so a `#` anywhere in the command starts a comment and eats
// the rest. The format string `#{session_name}` must be shell-quoted by the
// time it reaches ssh — otherwise the remote tmux sees `-F` with no argument
// and errors with "command list-sessions: -F expects an argument".
func TestRemoteAttachChoicesQuotesFormatString(t *testing.T) {
	var captured []string
	old := agentExecCommand
	agentExecCommand = func(name string, args ...string) *exec.Cmd {
		captured = append([]string{name}, args...)
		return exec.Command("sh", "-c", "exit 0")
	}
	t.Cleanup(func() { agentExecCommand = old })

	res := config.HostResolution{
		Name: "prod",
		Host: config.HostConfig{SSH: "user@prod.example.com"},
	}
	if _, err := remoteAttachChoices(res); err != nil {
		t.Fatalf("remoteAttachChoices: %v", err)
	}

	joined := strings.Join(captured, " ")
	if !strings.Contains(joined, "'#{session_name}'") {
		t.Fatalf("format arg must be single-quoted for remote shell; got: %s", joined)
	}
}

// When no tmux server is running on the remote side, tmux exits 1 with a
// "no server running" stderr message. remoteAttachChoices should treat that as
// "nothing attachable" rather than surfacing a hard error.
func TestRemoteAttachChoicesHandlesNoServer(t *testing.T) {
	old := agentExecCommand
	agentExecCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("sh", "-c", "echo 'no server running on /tmp/tmux-501/leo' 1>&2; exit 1")
	}
	t.Cleanup(func() { agentExecCommand = old })

	res := config.HostResolution{
		Name: "prod",
		Host: config.HostConfig{SSH: "user@prod.example.com"},
	}
	out, err := remoteAttachChoices(res)
	if err != nil {
		t.Fatalf("no-server should be treated as empty, got err=%v", err)
	}
	if len(out) != 0 {
		t.Fatalf("want empty list, got %+v", out)
	}
}
