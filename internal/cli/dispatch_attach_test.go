package cli

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/consult"
)

const watchSession = "_watch-d-abc123-1f2e"

// attachFixture fakes every seam `leo dispatch attach` touches: the daemon
// lookup, the tmux admin commands, and the final exec of the tmux client.
type attachFixture struct {
	record   consult.Record
	fetchErr error
	// failing names the tmux subcommand that should exit non-zero.
	failing string
	// probe is what the live window probe prints.
	probe string
	tmux  [][]string
	execs [][]string
	// attachCmd is the child process the tmux client runs as; nil succeeds.
	attachCmd func() *exec.Cmd
}

func newAttachFixture(t *testing.T) *attachFixture {
	t.Helper()
	f := &attachFixture{
		record: consult.Record{ID: "d-abc123", Kind: "dispatch", Mode: consult.ModeInteractive, PaneID: "%7", ViewerKind: "window", Status: consult.StatusRunning},
		probe:  "@3 1\n",
	}
	oldFetch, oldSuffix, oldLocate, oldExec, oldChild, oldTmuxLocate, oldTmuxEnv := dispatchRecordFn, watchSuffixFn, viewerLocateTmux, viewerExecCommandContext, agentExecCommand, tmuxLocate, tmuxEnv
	dispatchRecordFn = func(context.Context, *config.Config, string) (consult.Record, error) { return f.record, f.fetchErr }
	watchSuffixFn = func() string { return "1f2e" }
	viewerLocateTmux = func() (string, error) { return "/tmux", nil }
	tmuxLocate = func() (string, error) { return "/usr/bin/tmux", nil }
	tmuxEnv = func() string { return "" }
	viewerExecCommandContext = f.fakeTmux
	agentExecCommand = func(name string, args ...string) *exec.Cmd {
		f.execs = append(f.execs, append([]string{name}, args...))
		if f.attachCmd != nil {
			return f.attachCmd()
		}
		return exec.Command("true")
	}
	t.Cleanup(func() {
		dispatchRecordFn, watchSuffixFn, viewerLocateTmux, viewerExecCommandContext, agentExecCommand, tmuxLocate, tmuxEnv = oldFetch, oldSuffix, oldLocate, oldExec, oldChild, oldTmuxLocate, oldTmuxEnv
	})
	return f
}

func (f *attachFixture) fakeTmux(_ context.Context, name string, args ...string) *exec.Cmd {
	f.tmux = append(f.tmux, append([]string{name}, args...))
	sub := args[2] // after -L leo
	if sub == f.failing {
		return exec.Command("false")
	}
	switch sub {
	case "display-message":
		return exec.Command("printf", "%s", f.probe)
	case "new-session":
		return exec.Command("printf", "%s", "$9 @10\n")
	}
	return exec.Command("true")
}

func (f *attachFixture) run(t *testing.T, id string) error {
	t.Helper()
	return runDispatchAttachLocal(context.Background(), &config.Config{}, config.HostResolution{Localhost: true}, id)
}

func TestDispatchAttachLinksWindowIntoReadOnlyWatchSession(t *testing.T) {
	f := newAttachFixture(t)
	if err := f.run(t, "d-abc123"); err != nil {
		t.Fatal(err)
	}
	wantTmux := [][]string{
		{"/tmux", "-L", "leo", "display-message", "-p", "-t", "%7", "#{window_id} #{window_panes}"},
		{"/tmux", "-L", "leo", "new-session", "-d", "-s", watchSession, "-P", "-F", "#{session_id} #{window_id}"},
		{"/tmux", "-L", "leo", "link-window", "-d", "-s", "@3", "-t", "=" + watchSession + ":"},
		{"/tmux", "-L", "leo", "kill-window", "-t", "@10"},
		{"/tmux", "-L", "leo", "set-option", "-t", "$9", "status", "off"},
		{"/tmux", "-L", "leo", "set-hook", "-t", "$9", "client-attached", "set-option destroy-unattached on"},
		{"/tmux", "-L", "leo", "kill-session", "-t", "$9"},
	}
	if !reflect.DeepEqual(f.tmux, wantTmux) {
		t.Fatalf("tmux calls:\n got %q\nwant %q", f.tmux, wantTmux)
	}
	wantExec := [][]string{{"/usr/bin/tmux", "-L", "leo", "attach", "-r", "-t", "=" + watchSession}}
	if !reflect.DeepEqual(f.execs, wantExec) {
		t.Fatalf("exec:\n got %q\nwant %q", f.execs, wantExec)
	}
}

func TestDispatchAttachRefusesWithoutTouchingTmux(t *testing.T) {
	cases := map[string]struct {
		mutate func(*attachFixture)
		want   string
	}{
		"unknown":  {func(f *attachFixture) { f.fetchErr = errors.New("daemon: not found") }, "d-abc123: daemon: not found"},
		"headless": {func(f *attachFixture) { f.record.Mode, f.record.PaneID = consult.ModeHeadless, "" }, "headless"},
		"ended":    {func(f *attachFixture) { f.record.Status = consult.StatusDone }, "has ended"},
		"closed":   {func(f *attachFixture) { f.record.Status = consult.StatusClosed }, "has ended"},
		"no pane":  {func(f *attachFixture) { f.record.PaneID = "" }, "no pane"},
		"settling": {func(f *attachFixture) { f.record.Status = consult.StatusSettling }, "settling"},
		"split":    {func(f *attachFixture) { f.record.ViewerKind = "split" }, "split pane"},
		"hidden":   {func(f *attachFixture) { f.record.ViewerKind = "hidden" }, "hidden pane"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newAttachFixture(t)
			tc.mutate(f)
			err := f.run(t, "d-abc123")
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "\n") {
				t.Fatalf("err=%v, want one line containing %q", err, tc.want)
			}
			if ExitCode(err) != 1 {
				t.Fatalf("exit code %d", ExitCode(err))
			}
			if len(f.tmux) != 0 || len(f.execs) != 0 {
				t.Fatalf("touched tmux: %q %q", f.tmux, f.execs)
			}
		})
	}
}

func TestDispatchAttachRefusesAWindowThatNowHasSeveralPanes(t *testing.T) {
	f := newAttachFixture(t)
	f.probe = "@3 2\n"
	err := f.run(t, "d-abc123")
	if err == nil || !strings.Contains(err.Error(), "shares its tmux window") {
		t.Fatalf("err=%v", err)
	}
	if len(f.tmux) != 1 || f.tmux[0][3] != "display-message" || len(f.execs) != 0 {
		t.Fatalf("only the probe may run: %q %q", f.tmux, f.execs)
	}
}

func TestDispatchAttachRefusesAGonePane(t *testing.T) {
	f := newAttachFixture(t)
	f.failing = "display-message"
	err := f.run(t, "d-abc123")
	if err == nil || !strings.Contains(err.Error(), "%7 is gone") {
		t.Fatalf("err=%v", err)
	}
	if len(f.tmux) != 1 {
		t.Fatalf("tmux=%q", f.tmux)
	}
}

func TestDispatchAttachKillsWatchSessionOnAnyFailureBeforeAttach(t *testing.T) {
	for _, failing := range []string{"link-window", "kill-window", "set-option", "set-hook"} {
		t.Run(failing, func(t *testing.T) {
			f := newAttachFixture(t)
			f.failing = failing
			if err := f.run(t, "d-abc123"); err == nil {
				t.Fatal("want error")
			}
			last := f.tmux[len(f.tmux)-1]
			if want := []string{"/tmux", "-L", "leo", "kill-session", "-t", "$9"}; !reflect.DeepEqual(last, want) {
				t.Fatalf("last tmux call %q, want %q", last, want)
			}
			if len(f.execs) != 0 {
				t.Fatalf("attached after a setup failure: %q", f.execs)
			}
		})
	}
}

func TestDispatchAttachKillsWatchSessionWhenTheClientFails(t *testing.T) {
	// Outside tmux the client is a child process, so a tmux that cannot start
	// (stdin not a TTY) comes back here and the session is not leaked.
	f := newAttachFixture(t)
	f.attachCmd = func() *exec.Cmd { return exec.Command("sh", "-c", "exit 3") }
	err := f.run(t, "d-abc123")
	if ExitCode(err) != 3 {
		t.Fatalf("err=%v exit=%d, want the client's status 3", err, ExitCode(err))
	}
	if last := f.tmux[len(f.tmux)-1]; !reflect.DeepEqual(last, []string{"/tmux", "-L", "leo", "kill-session", "-t", "$9"}) {
		t.Fatalf("last tmux call %q", last)
	}
}

func TestDispatchAttachKillsWatchSessionWhenTheClientCannotBeSpawned(t *testing.T) {
	f := newAttachFixture(t)
	f.attachCmd = func() *exec.Cmd { return exec.Command("/nonexistent/tmux") }
	if err := f.run(t, "d-abc123"); err == nil {
		t.Fatal("want error")
	}
	if last := f.tmux[len(f.tmux)-1]; !reflect.DeepEqual(last, []string{"/tmux", "-L", "leo", "kill-session", "-t", "$9"}) {
		t.Fatalf("last tmux call %q", last)
	}
}

func TestDispatchAttachNeverCreatesAnUnarmedSession(t *testing.T) {
	// destroy-unattached destroys a never-attached session at once, so it
	// must only be armed by the attach itself (the client-attached hook).
	f := newAttachFixture(t)
	if err := f.run(t, "d-abc123"); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.tmux {
		if c[3] == "set-option" && containsString(c, "destroy-unattached") {
			t.Fatalf("destroy-unattached set before any client attached: %q", c)
		}
	}
}

func TestDispatchAttachUsesAPopupInsideTmux(t *testing.T) {
	f := newAttachFixture(t)
	tmuxEnv = func() string { return "/tmp/tmux-501/default,1,0" }
	stub := withStubExec(t)
	withStubStdio(t)
	if err := f.run(t, "d-abc123"); err != nil {
		t.Fatal(err)
	}
	if len(stub.calls) != 1 || stub.calls[0][0] != "/usr/bin/tmux" || stub.calls[0][1] != "display-popup" {
		t.Fatalf("popup calls=%q", stub.calls)
	}
	inner := stub.calls[0][len(stub.calls[0])-1]
	if want := "'/usr/bin/tmux' -L leo attach -r -t '=" + watchSession + "'"; inner != want {
		t.Fatalf("popup command %q, want %q", inner, want)
	}
	if last := f.tmux[len(f.tmux)-1]; !reflect.DeepEqual(last, []string{"/tmux", "-L", "leo", "kill-session", "-t", "$9"}) {
		t.Fatalf("a popup returns, so the session must be cleaned up: last tmux call %q", last)
	}
}

func TestDispatchAttachRejectsUnsafeIDs(t *testing.T) {
	f := newAttachFixture(t)
	for _, id := range []string{"", "d-a b", "d-a:b", "d-a.b", "../x", "d-a;b"} {
		if err := f.run(t, id); err == nil {
			t.Fatalf("id %q accepted", id)
		}
	}
	if len(f.tmux) != 0 {
		t.Fatalf("tmux=%q", f.tmux)
	}
}

func TestDispatchAttachRemoteRunsRemoteLeoOverTTY(t *testing.T) {
	path := newAgentCLITestConfig(t)
	stub := withStubExec(t)
	withStubStdio(t)
	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "dispatch", "attach", "--host", "prod", "d-abc123"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	want := []string{"ssh", "-tt", "user@prod.example.com", "-p", "2222"}
	want = append(want, ctlOpts(homeFromConfigPath(path))...)
	want = append(want, config.DefaultRemoteLeoPath, "dispatch", "attach", "'d-abc123'")
	if len(stub.calls) != 1 || !reflect.DeepEqual(stub.calls[0], want) {
		t.Fatalf("ssh calls:\n got %q\nwant %q", stub.calls, [][]string{want})
	}
}

func TestDispatchAttachRemoteRejectsUnsafeIDsBeforeSSH(t *testing.T) {
	path := newAgentCLITestConfig(t)
	stub := withStubExec(t)
	withStubStdio(t)
	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "dispatch", "attach", "--host", "prod", "x'; rm -rf ~; '"})
	_ = root.Execute()
	if len(stub.calls) != 0 {
		t.Fatalf("an unsafe id reached ssh: %q", stub.calls)
	}
}

func TestDispatchAttachRemoteFailureKeepsExitStatusWithoutDoubleReporting(t *testing.T) {
	path := newAgentCLITestConfig(t)
	old := agentExecCommand
	agentExecCommand = func(string, ...string) *exec.Cmd { return exec.Command("sh", "-c", "exit 1") }
	t.Cleanup(func() { agentExecCommand = old })
	oldTI := ensureRemoteTerminfoFn
	ensureRemoteTerminfoFn = func(config.HostResolution) string { return "" }
	t.Cleanup(func() { ensureRemoteTerminfoFn = oldTI })
	withStubStdio(t)
	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "dispatch", "attach", "--host", "prod", "d-abc123"})
	err := root.Execute()
	if err == nil || ExitCode(err) != 1 || err.Error() != "" {
		t.Fatalf("err=%q code=%d; the remote leo already printed its one line", err, ExitCode(err))
	}
}

func TestDispatchAttachCommandShape(t *testing.T) {
	cmd := newDispatchAttachCmd()
	if cmd.Flags().Lookup("host") == nil || !cmd.SilenceUsage || !cmd.SilenceErrors {
		t.Fatal("want a --host flag and silenced usage/errors")
	}
	if cmd.Args(cmd, nil) == nil || cmd.Args(cmd, []string{"a", "b"}) == nil || cmd.Args(cmd, []string{"a"}) != nil {
		t.Fatal("want exactly one <id> argument")
	}
}

func TestAttachTmuxSessionReadOnlyAddsFlag(t *testing.T) {
	stubTmuxLookPath(t, "/usr/bin/tmux", nil)
	stubOutsideTmux(t)
	var got []string
	old := agentSyscallExec
	agentSyscallExec = func(_ string, argv []string, _ []string) error { got = argv; return nil }
	t.Cleanup(func() { agentSyscallExec = old })
	if err := attachTmuxSession(config.HostResolution{Localhost: true}, "leo-x", attachOptions{readOnly: true}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"tmux", "-L", "leo", "attach", "-r", "-t", "=leo-x"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("argv=%q want %q", got, want)
	}
	stub := withStubExec(t)
	withStubStdio(t)
	res := config.HostResolution{Name: "prod", Host: config.HostConfig{SSH: "u@prod"}}
	if err := attachTmuxSession(res, "leo-x", attachOptions{readOnly: true}); err != nil {
		t.Fatal(err)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("ssh calls=%q", stub.calls)
	}
	want := []string{"ssh", "-t", "u@prod", config.DefaultRemoteTmuxPath, "-L", "leo", "attach", "-r", "-t", "'=leo-x'"}
	if !reflect.DeepEqual(stub.calls[0], want) {
		t.Fatalf("remote attach:\n got %q\nwant %q", stub.calls[0], want)
	}
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
