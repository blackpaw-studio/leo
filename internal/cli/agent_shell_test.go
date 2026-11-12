package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
)

func TestSelectShell(t *testing.T) {
	tests := []struct {
		name   string
		env    string
		passwd string
		want   string
	}{
		{"SHELL wins", "/bin/zsh", "/bin/bash", "/bin/zsh"},
		{"passwd when SHELL unset", "", "/bin/bash", "/bin/bash"},
		{"passwd when SHELL is not absolute", "zsh", "/bin/bash", "/bin/bash"},
		{"sh when both unusable", "", "", "/bin/sh"},
		{"sh when passwd shell is not absolute", "", "nologin", "/bin/sh"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := selectShell(tt.env, func() string { return tt.passwd })
			if got != tt.want {
				t.Errorf("selectShell = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSelectShellSkipsPasswdLookupWhenShellSet(t *testing.T) {
	selectShell("/bin/zsh", func() string {
		t.Fatal("passwd consulted although $SHELL is usable")
		return ""
	})
}

func TestShellFromPasswdLine(t *testing.T) {
	tests := []struct{ line, want string }{
		{"evan:x:501:20:Evan:/home/evan:/bin/zsh", "/bin/zsh"},
		{"evan:x:501:20:Evan:/home/evan:/bin/zsh\n", "/bin/zsh"},
		{"evan:x:501:20:Evan:/home/evan", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := shellFromPasswdLine(tt.line); got != tt.want {
			t.Errorf("shellFromPasswdLine(%q) = %q, want %q", tt.line, got, tt.want)
		}
	}
}

func TestShellFromDscl(t *testing.T) {
	if got := shellFromDscl("UserShell: /bin/zsh\n"); got != "/bin/zsh" {
		t.Errorf("shellFromDscl = %q, want /bin/zsh", got)
	}
	if got := shellFromDscl("garbage"); got != "" {
		t.Errorf("shellFromDscl(garbage) = %q, want empty", got)
	}
}

func TestShellEnvDropsTmuxAndPinsPWD(t *testing.T) {
	in := []string{"PATH=/bin", "TMUX=/tmp/t,1,0", "TMUX_PANE=%3", "PWD=/elsewhere", "LANG=en_US.UTF-8", "TMUXISH=keep"}
	got := shellEnv(in, "/work/space")
	want := []string{"PATH=/bin", "LANG=en_US.UTF-8", "TMUXISH=keep", "PWD=/work/space"}
	if !slices.Equal(got, want) {
		t.Errorf("shellEnv = %v, want %v", got, want)
	}
}

// shellFixture stubs the daemon lookups and the exec seams for the local path.
type shellFixture struct {
	session daemon.AgentSessionResponse
	sessErr error
	records []agent.Record
	chdir   []string
	exec    struct {
		argv0 string
		argv  []string
		env   []string
	}
	execCalls int
}

func withShellFixture(t *testing.T, f *shellFixture) {
	t.Helper()
	oldLookup, oldList, oldChdir, oldExec := lookupAgentSession, agentListFn, agentChdir, agentSyscallExec
	lookupAgentSession = func(context.Context, string, string) (daemon.AgentSessionResponse, error) {
		return f.session, f.sessErr
	}
	agentListFn = func(context.Context, string) ([]agent.Record, error) { return f.records, nil }
	agentChdir = func(dir string) error { f.chdir = append(f.chdir, dir); return nil }
	agentSyscallExec = func(argv0 string, argv, env []string) error {
		f.execCalls++
		f.exec.argv0, f.exec.argv, f.exec.env = argv0, argv, env
		return nil
	}
	t.Cleanup(func() {
		lookupAgentSession, agentListFn, agentChdir, agentSyscallExec = oldLookup, oldList, oldChdir, oldExec
	})
}

func runAgentShell(t *testing.T, args ...string) error {
	t.Helper()
	path := newAgentCLITestConfig(t)
	withStubStdio(t)
	root := newRootCmd()
	root.SetArgs(append([]string{"--config", path, "agent", "shell", "--host", "localhost"}, args...))
	return root.Execute()
}

func TestAgentShellLocalExecsLoginShellInWorkspace(t *testing.T) {
	ws := t.TempDir()
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("TMUX", "/tmp/tmux-1/default,1,0")
	t.Setenv("TMUX_PANE", "%1")
	f := &shellFixture{
		session: daemon.AgentSessionResponse{Name: "leo-scratch", Session: "leo-leo-scratch"},
		records: []agent.Record{{Name: "other", Workspace: "/nope"}, {Name: "leo-scratch", Workspace: ws}},
	}
	withShellFixture(t, f)

	if err := runAgentShell(t, "--", "scratch"); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if f.execCalls != 1 {
		t.Fatalf("exec calls = %d, want 1", f.execCalls)
	}
	if f.exec.argv0 != "/bin/zsh" || !slices.Equal(f.exec.argv, []string{"/bin/zsh", "-l"}) {
		t.Errorf("exec = %q %v, want /bin/zsh [/bin/zsh -l]", f.exec.argv0, f.exec.argv)
	}
	if !slices.Equal(f.chdir, []string{ws}) {
		t.Errorf("chdir = %v, want [%s]", f.chdir, ws)
	}
	for _, kv := range f.exec.env {
		if strings.HasPrefix(kv, "TMUX=") || strings.HasPrefix(kv, "TMUX_PANE=") {
			t.Errorf("shell env leaks %q", kv)
		}
	}
	if !slices.Contains(f.exec.env, "PWD="+ws) {
		t.Errorf("env lacks PWD=%s: %v", ws, f.exec.env)
	}
}

func TestAgentShellLocalFallsBackToBinSh(t *testing.T) {
	t.Setenv("SHELL", "")
	old := userShellFn
	userShellFn = func() string { return "" }
	t.Cleanup(func() { userShellFn = old })
	f := &shellFixture{
		session: daemon.AgentSessionResponse{Name: "a"},
		records: []agent.Record{{Name: "a", Workspace: t.TempDir()}},
	}
	withShellFixture(t, f)
	if err := runAgentShell(t, "a"); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if f.exec.argv0 != "/bin/sh" || !slices.Equal(f.exec.argv, []string{"/bin/sh", "-l"}) {
		t.Errorf("exec = %q %v, want /bin/sh -l", f.exec.argv0, f.exec.argv)
	}
}

func TestAgentShellLocalRefusals(t *testing.T) {
	ws := t.TempDir()
	file := filepath.Join(ws, "f")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	moved := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		f       shellFixture
		wantMsg string
		wantIs  error
	}{
		{
			name:    "unknown agent",
			f:       shellFixture{sessErr: &agent.ErrNotFound{Query: "ghost"}},
			wantMsg: "ghost",
		},
		{
			name:    "empty session response",
			f:       shellFixture{},
			wantMsg: `no agent named "ghost"`,
		},
		{
			name: "moved agent",
			f: shellFixture{session: daemon.AgentSessionResponse{
				Name: "ghost", Session: "s", Stopped: true, MovedTo: "laptop", MovedAt: &moved,
			}},
			wantMsg: "moved to laptop",
			wantIs:  agent.ErrAgentMoved,
		},
		{
			name: "no workspace",
			f: shellFixture{
				session: daemon.AgentSessionResponse{Name: "ghost", Session: "s"},
				records: []agent.Record{{Name: "ghost"}},
			},
			wantMsg: "no workspace",
		},
		{
			name: "record missing from list",
			f: shellFixture{
				session: daemon.AgentSessionResponse{Name: "ghost", Session: "s"},
			},
			wantMsg: "no workspace",
		},
		{
			name: "workspace gone",
			f: shellFixture{
				session: daemon.AgentSessionResponse{Name: "ghost", Session: "s"},
				records: []agent.Record{{Name: "ghost", Workspace: filepath.Join(ws, "missing")}},
			},
			wantMsg: "does not exist",
		},
		{
			name: "workspace is a file",
			f: shellFixture{
				session: daemon.AgentSessionResponse{Name: "ghost", Session: "s"},
				records: []agent.Record{{Name: "ghost", Workspace: file}},
			},
			wantMsg: "not a directory",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := tt.f
			withShellFixture(t, &f)
			err := runAgentShell(t, "ghost")
			if err == nil {
				t.Fatal("expected a refusal")
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantMsg)
			}
			if tt.wantIs != nil && !errors.Is(err, tt.wantIs) {
				t.Errorf("error %v is not %v", err, tt.wantIs)
			}
			if ExitCode(err) == 0 {
				t.Error("refusal must exit nonzero")
			}
			if f.execCalls != 0 || len(f.chdir) != 0 {
				t.Errorf("refused shell still ran: exec=%d chdir=%v", f.execCalls, f.chdir)
			}
		})
	}
}

func TestAgentShellRemoteArgvQuotesName(t *testing.T) {
	path := newAgentCLITestConfig(t)
	stub := withStubExec(t)
	withStubStdio(t)
	// A daemon lookup on the remote path would be a bug: the remote leo owns it.
	old := lookupAgentSession
	lookupAgentSession = func(context.Context, string, string) (daemon.AgentSessionResponse, error) {
		t.Error("remote shell must not consult the local daemon")
		return daemon.AgentSessionResponse{}, nil
	}
	t.Cleanup(func() { lookupAgentSession = old })

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "shell", "--", "-it's a name"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("ssh calls = %d, want 1: %v", len(stub.calls), stub.calls)
	}
	want := []string{"ssh", "-tt", "user@prod.example.com", "-p", "2222"}
	want = append(want, ctlOpts(homeFromConfigPath(path))...)
	want = append(want, quotedDefaultRemoteLeo, "agent", "shell", "--", `'-it'\''s a name'`)
	if !slices.Equal(stub.calls[0], want) {
		t.Errorf("ssh argv = %q\nwant      %q", stub.calls[0], want)
	}
}

func TestAgentShellRemotePassesTermFallbackBeforeLeo(t *testing.T) {
	path := newAgentCLITestConfig(t)
	stub := withStubExec(t)
	withStubStdio(t)
	ensureRemoteTerminfoFn = func(_ config.HostResolution) string { return "xterm-256color" }

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "shell", "scratch"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	i := slices.Index(stub.calls[0], "env")
	if i < 0 || stub.calls[0][i+1] != "TERM=xterm-256color" || stub.calls[0][i+2] != quotedDefaultRemoteLeo {
		t.Errorf("ssh argv = %q, want env TERM=... directly before the leo path", stub.calls[0])
	}
}

func TestAgentShellRemoteExitStatusPassesThrough(t *testing.T) {
	path := newAgentCLITestConfig(t)
	withStubStdio(t)
	old, oldTI := agentExecCommand, ensureRemoteTerminfoFn
	agentExecCommand = func(string, ...string) *exec.Cmd { return exec.Command("sh", "-c", "exit 3") }
	ensureRemoteTerminfoFn = func(_ config.HostResolution) string { return "" }
	t.Cleanup(func() { agentExecCommand, ensureRemoteTerminfoFn = old, oldTI })

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "shell", "scratch"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected a nonzero exit")
	}
	if got := ExitCode(err); got != 3 {
		t.Errorf("ExitCode = %d, want 3", got)
	}
	if err.Error() != "" {
		t.Errorf("error text %q must be empty: the remote already reported", err)
	}
}
