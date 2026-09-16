package hosts

import (
	"bytes"
	"context"
	"os/exec"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
)

type Ticker interface {
	C() <-chan time.Time
	Stop()
}
type Clock interface {
	Now() time.Time
	After(time.Duration) <-chan time.Time
	NewTicker(time.Duration) Ticker
}
type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
func (realClock) NewTicker(d time.Duration) Ticker       { return realTicker{time.NewTicker(d)} }

type realTicker struct{ *time.Ticker }

func (t realTicker) C() <-chan time.Time { return t.Ticker.C }

type forwardProcess interface {
	Done() <-chan error
	Kill()
	Stderr() string
}
type commandProcess struct {
	cmd    *exec.Cmd
	done   chan error
	stderr *bytes.Buffer
}

func (p *commandProcess) Done() <-chan error { return p.done }
func (p *commandProcess) Kill() {
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}
func (p *commandProcess) Stderr() string { return p.stderr.String() }

type dependencies struct {
	clock    Clock
	discover func(context.Context, string, []string, string) (string, error)
	start    func(context.Context, config.HostConfig, string, string, string) (forwardProcess, error)
	health   func(string) bool
	exit     func(context.Context, config.HostConfig, string)
	remove   func(...string)
}

func realDependencies() dependencies {
	return dependencies{clock: realClock{}, discover: discover, health: health, remove: removeSockets, start: func(ctx context.Context, c config.HostConfig, ctl, local, remote string) (forwardProcess, error) {
		cmd := sshExecCommand(ctx, "ssh", forwardArgs(c.SSH, c.SSHArgs, ctl, local, remote)...)
		stderr := &bytes.Buffer{}
		cmd.Stderr = stderr
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		p := &commandProcess{cmd: cmd, done: make(chan error, 1), stderr: stderr}
		go func() { p.done <- cmd.Wait() }()
		return p, nil
	}, exit: func(ctx context.Context, c config.HostConfig, ctl string) {
		_ = sshExecCommand(ctx, "ssh", exitArgs(c.SSH, c.SSHArgs, ctl)...).Run()
	}}
}
