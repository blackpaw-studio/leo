package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

func remoteRes() config.HostResolution {
	return config.HostResolution{
		Localhost: false,
		Host: config.HostConfig{
			SSH:     "user@host.example.com",
			SSHArgs: []string{"-p", "2222"},
		},
	}
}

func withTerminfoStubs(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	oldDir := terminfoCacheDir
	terminfoCacheDir = func() string { return dir }
	t.Cleanup(func() { terminfoCacheDir = oldDir })

	// Silence the diagnostic line so failed-install tests don't pollute
	// `go test` output.
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open devnull: %v", err)
	}
	oldStderr := terminfoStderr
	terminfoStderr = func() *os.File { return devnull }
	t.Cleanup(func() { terminfoStderr = oldStderr; _ = devnull.Close() })

	return dir
}

func TestEnsureRemoteTerminfoSkipsLocalhost(t *testing.T) {
	withTerminfoStubs(t)
	t.Setenv("TERM", "xterm-ghostty")
	got := ensureRemoteTerminfo(config.HostResolution{Localhost: true})
	if got != "" {
		t.Errorf("ensureRemoteTerminfo(localhost) = %q, want \"\"", got)
	}
}

func TestEnsureRemoteTerminfoSkipsSafeTerm(t *testing.T) {
	withTerminfoStubs(t)
	t.Setenv("TERM", "xterm-256color")

	calls := 0
	old := terminfoInfocmp
	terminfoInfocmp = func(string) *exec.Cmd { calls++; return exec.Command("true") }
	t.Cleanup(func() { terminfoInfocmp = old })

	if got := ensureRemoteTerminfo(remoteRes()); got != "" {
		t.Errorf("override = %q, want \"\"", got)
	}
	if calls != 0 {
		t.Errorf("infocmp invoked %d times for safe TERM; want 0", calls)
	}
}

func TestEnsureRemoteTerminfoSkipsEmptyTerm(t *testing.T) {
	withTerminfoStubs(t)
	t.Setenv("TERM", "")
	if got := ensureRemoteTerminfo(remoteRes()); got != "" {
		t.Errorf("override = %q, want \"\"", got)
	}
}

func TestEnsureRemoteTerminfoUsesSentinel(t *testing.T) {
	dir := withTerminfoStubs(t)
	t.Setenv("TERM", "xterm-ghostty")
	// Pre-write a sentinel for this host+TERM — install should be skipped.
	sentinel := terminfoSentinelPath(dir, remoteRes().Host.SSH, "xterm-ghostty")
	if err := os.MkdirAll(filepath.Dir(sentinel), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(sentinel, nil, 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	called := 0
	old := terminfoInfocmp
	terminfoInfocmp = func(string) *exec.Cmd { called++; return nil }
	t.Cleanup(func() { terminfoInfocmp = old })

	if got := ensureRemoteTerminfo(remoteRes()); got != "" {
		t.Errorf("cached install: override = %q, want \"\"", got)
	}
	if called != 0 {
		t.Errorf("infocmp called %d times despite sentinel; want 0", called)
	}
}

func TestEnsureRemoteTerminfoInstallsAndCaches(t *testing.T) {
	dir := withTerminfoStubs(t)
	t.Setenv("TERM", "xterm-ghostty")

	// Pretend infocmp emits some terminfo source.
	oldIC := terminfoInfocmp
	terminfoInfocmp = func(term string) *exec.Cmd {
		if term != "xterm-ghostty" {
			t.Errorf("infocmp called with %q, want xterm-ghostty", term)
		}
		return exec.Command("printf", "xterm-ghostty|fake,\n")
	}
	t.Cleanup(func() { terminfoInfocmp = oldIC })

	// Pretend `ssh ... tic -x -` succeeds.
	oldExec := agentExecCommand
	var seen [][]string
	agentExecCommand = func(name string, args ...string) *exec.Cmd {
		seen = append(seen, append([]string{name}, args...))
		return exec.Command("true")
	}
	t.Cleanup(func() { agentExecCommand = oldExec })

	got := ensureRemoteTerminfo(remoteRes())
	if got != "" {
		t.Errorf("override on successful install = %q, want \"\"", got)
	}
	if len(seen) != 1 {
		t.Fatalf("expected 1 ssh call, got %d (%v)", len(seen), seen)
	}
	want := []string{"ssh", "user@host.example.com", "-p", "2222", "tic", "-x", "-"}
	if !equalStrings(seen[0], want) {
		t.Errorf("ssh install args = %v, want %v", seen[0], want)
	}
	sentinel := terminfoSentinelPath(dir, remoteRes().Host.SSH, "xterm-ghostty")
	if _, err := os.Stat(sentinel); err != nil {
		t.Errorf("sentinel not written: %v", err)
	}
}

func TestEnsureRemoteTerminfoFallsBackWhenInstallFails(t *testing.T) {
	dir := withTerminfoStubs(t)
	t.Setenv("TERM", "xterm-ghostty")

	oldIC := terminfoInfocmp
	terminfoInfocmp = func(string) *exec.Cmd {
		return exec.Command("printf", "xterm-ghostty|fake,\n")
	}
	t.Cleanup(func() { terminfoInfocmp = oldIC })

	oldExec := agentExecCommand
	agentExecCommand = func(name string, args ...string) *exec.Cmd {
		// Remote has no `tic`. Use `false` so .Run() returns non-zero.
		return exec.Command("false")
	}
	t.Cleanup(func() { agentExecCommand = oldExec })

	got := ensureRemoteTerminfo(remoteRes())
	if got != terminfoFallback {
		t.Errorf("override on failed install = %q, want %q", got, terminfoFallback)
	}
	sentinel := terminfoSentinelPath(dir, remoteRes().Host.SSH, "xterm-ghostty")
	if _, err := os.Stat(sentinel); err == nil {
		t.Errorf("sentinel was written despite failed install")
	}
}

func TestEnsureRemoteTerminfoNoInfocmpFallsBack(t *testing.T) {
	withTerminfoStubs(t)
	t.Setenv("TERM", "xterm-ghostty")

	old := terminfoInfocmp
	terminfoInfocmp = func(string) *exec.Cmd { return nil }
	t.Cleanup(func() { terminfoInfocmp = old })

	got := ensureRemoteTerminfo(remoteRes())
	if got != terminfoFallback {
		t.Errorf("override with no infocmp = %q, want %q", got, terminfoFallback)
	}
}

func TestApplyRemoteTermFallback(t *testing.T) {
	args := []string{"-t", "user@host", "-p", "22", "tmux", "-L", "leo", "attach", "-t", "leo-x"}
	t.Run("noop on empty override", func(t *testing.T) {
		got := applyRemoteTermFallback(args, 4, "")
		if !equalStrings(got, args) {
			t.Errorf("got %v, want %v", got, args)
		}
	})
	t.Run("inserts env shim", func(t *testing.T) {
		got := applyRemoteTermFallback(args, 4, "xterm-256color")
		want := []string{"-t", "user@host", "-p", "22", "env", "TERM=xterm-256color", "tmux", "-L", "leo", "attach", "-t", "leo-x"}
		if !equalStrings(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
		// Original slice must be untouched — applyRemoteTermFallback mutates
		// nothing the caller can see.
		if strings.Join(args, " ") != "-t user@host -p 22 tmux -L leo attach -t leo-x" {
			t.Errorf("input slice mutated: %v", args)
		}
	})
}
