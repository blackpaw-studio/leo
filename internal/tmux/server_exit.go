package tmux

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// serverExitInfo describes how a foreground server process ended. Signal is
// zero for a normal exit (Code is then the exit code); for a signal death
// Code is -1.
type serverExitInfo struct {
	Pid    int
	Ran    time.Duration
	Code   int
	Signal syscall.Signal
	Core   bool
}

// serverRun tracks one foreground server this process started. done closes
// once the reaper goroutine has stored info.
type serverRun struct {
	done chan struct{}
	info *serverExitInfo
}

var (
	serverRunMu sync.Mutex
	lastRun     *serverRun
)

// currentServerRun returns the most recent server started by this process, or
// nil when none was (e.g. an adopted server).
func currentServerRun() *serverRun {
	serverRunMu.Lock()
	defer serverRunMu.Unlock()
	return lastRun
}

// trackServerExit reaps cmd in the background and records how it ended. The
// server survives this process's death regardless: Wait only observes the
// child, Setpgid is what isolates it.
func trackServerExit(cmd *exec.Cmd, started time.Time) {
	run := &serverRun{done: make(chan struct{})}
	pid := cmd.Process.Pid
	serverRunMu.Lock()
	lastRun = run
	serverRunMu.Unlock()
	go func() {
		defer close(run.done)
		_ = cmd.Wait()
		info := &serverExitInfo{Pid: pid, Ran: time.Since(started).Round(time.Second), Code: -1}
		if st := cmd.ProcessState; st != nil {
			info.Code = st.ExitCode()
			if ws, ok := st.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				info.Signal = ws.Signal()
				info.Core = ws.CoreDump()
			}
		}
		run.info = info
	}()
}

// formatServerExit renders a server exit for the log. A nil info means this
// process never started the server, so no status is available.
func formatServerExit(info *serverExitInfo) string {
	if info == nil {
		return "tmux: foreground server exited; exit status unavailable (server was adopted, not started by this daemon)"
	}
	how := fmt.Sprintf("exit code %d", info.Code)
	if info.Signal != 0 {
		how = "signal " + info.Signal.String()
		if info.Core {
			how += " (core dumped)"
		}
	}
	return fmt.Sprintf("tmux: foreground server (pid %d) exited: %s, ran %s", info.Pid, how, info.Ran)
}

// describeServerDown reports why the supervisor saw the server missing.
func describeServerDown() string {
	run := currentServerRun()
	if run == nil {
		return formatServerExit(nil)
	}
	select {
	case <-run.done:
		return formatServerExit(run.info)
	default:
		return "tmux: foreground server process still running but not answering probes"
	}
}

// probeError carries tmux's output alongside the exec error.
type probeError struct {
	err    error
	output string
}

func (e *probeError) Error() string {
	if out := strings.TrimSpace(e.output); out != "" {
		return fmt.Sprintf("%v: %s", e.err, out)
	}
	return e.err.Error()
}

func (e *probeError) Unwrap() error { return e.err }

// probeServer is ServerRunning with the failure reason kept.
func probeServer(tmuxPath string) error {
	out, err := serverExecCommand(tmuxPath, Args("display-message", "-p", "#{pid}")...).CombinedOutput()
	if err != nil {
		return &probeError{err: err, output: string(out)}
	}
	return nil
}
