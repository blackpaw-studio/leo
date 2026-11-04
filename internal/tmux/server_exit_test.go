package tmux

import (
	"errors"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestFormatServerExit(t *testing.T) {
	tests := []struct {
		name string
		info *serverExitInfo
		want []string
	}{
		{"exit code", &serverExitInfo{Pid: 42, Ran: 90 * time.Second, Code: 3}, []string{"pid 42", "exit code 3", "1m30s"}},
		{"signal", &serverExitInfo{Pid: 42, Ran: 5 * time.Second, Code: -1, Signal: syscall.SIGKILL}, []string{"pid 42", "signal killed", "5s"}},
		{"signal core", &serverExitInfo{Pid: 7, Code: -1, Signal: syscall.SIGSEGV, Core: true}, []string{"segmentation fault", "core dumped"}},
		{"adopted", nil, []string{"exit status unavailable", "adopted"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatServerExit(tt.info)
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("formatServerExit = %q, missing %q", got, w)
				}
			}
		})
	}
}

func TestFormatProbeError(t *testing.T) {
	err := &probeError{err: errors.New("exit status 1"), output: " no server running on /x\n"}
	if got := err.Error(); !strings.Contains(got, "exit status 1") || !strings.Contains(got, "no server running on /x") {
		t.Errorf("probeError = %q", got)
	}
}

func TestProbeServerReportsOutput(t *testing.T) {
	orig := serverExecCommand
	defer func() { serverExecCommand = orig }()
	serverExecCommand = func(string, ...string) *exec.Cmd {
		return exec.Command("sh", "-c", "echo no server running >&2; exit 1")
	}
	err := probeServer("tmux")
	if err == nil || !strings.Contains(err.Error(), "no server running") {
		t.Fatalf("probeServer err = %v, want stderr text", err)
	}
}

// A started server's exit signal is observed and recorded, and the server is
// still launched in its own process group (the daemon-restart survival
// contract).
func TestStartForegroundServerRecordsSignalExit(t *testing.T) {
	s := &stubServer{}
	withStubServer(t, s)
	var captured *exec.Cmd
	startForegroundCommand = func(_ string, args ...string) *exec.Cmd {
		s.startForeground("tmux", args...)
		captured = exec.Command("sh", "-c", "kill -KILL $$")
		return captured
	}

	if _, err := StartForegroundServer("tmux"); err != nil {
		t.Fatalf("StartForegroundServer: %v", err)
	}
	if captured.SysProcAttr == nil || !captured.SysProcAttr.Setpgid {
		t.Fatal("foreground server must keep Setpgid")
	}
	run := currentServerRun()
	if run == nil {
		t.Fatal("expected a recorded run")
	}
	select {
	case <-run.done:
	case <-time.After(5 * time.Second):
		t.Fatal("exit not observed")
	}
	got := formatServerExit(run.info)
	if !strings.Contains(got, "signal killed") {
		t.Errorf("exit = %q, want signal killed", got)
	}
}

func TestDescribeServerDownAdopted(t *testing.T) {
	serverRunMu.Lock()
	orig := lastRun
	lastRun = nil
	serverRunMu.Unlock()
	defer func() { serverRunMu.Lock(); lastRun = orig; serverRunMu.Unlock() }()
	if got := describeServerDown(); !strings.Contains(got, "adopted") {
		t.Errorf("describeServerDown = %q", got)
	}
}
