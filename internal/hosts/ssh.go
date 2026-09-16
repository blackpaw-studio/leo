package hosts

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

const remoteSockExpr = `printf %s "${LEO_HOME:-$HOME/.leo}/state/leo.sock"`

var sshExecCommand = exec.CommandContext

func discoverArgs(host string, extra []string, ctl string) []string {
	a := []string{host}
	a = append(a, extra...)
	a = append(a, "-o", "ControlMaster=auto", "-o", "ControlPath="+ctl, "-o", "BatchMode=yes", "sh", "-c", "'"+remoteSockExpr+"'")
	return a
}
func forwardArgs(host string, extra []string, ctl, local, remote string) []string {
	a := []string{"-N", "-T", "-o", "BatchMode=yes", "-o", "ExitOnForwardFailure=yes", "-o", "StreamLocalBindUnlink=yes", "-o", "ControlMaster=auto", "-o", "ControlPath=" + ctl, "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3"}
	a = append(a, extra...)
	return append(a, "-L", local+":"+remote, host)
}
func exitArgs(host string, extra []string, ctl string) []string {
	a := []string{"-o", "BatchMode=yes", "-o", "ControlPath=" + ctl, "-O", "exit"}
	a = append(a, extra...)
	return append(a, host)
}
func discover(ctx context.Context, host string, extra []string, ctl string) (string, error) {
	cmd := sshExecCommand(ctx, "ssh", discoverArgs(host, extra, ctl)...)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		he := ClassifySSHStderr(stderr.String())
		if he.Message == "" {
			he.Message = err.Error()
		}
		return "", he
	}
	p := strings.TrimSpace(out.String())
	if !strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("remote returned non-absolute socket path %q", p)
	}
	return p, nil
}
