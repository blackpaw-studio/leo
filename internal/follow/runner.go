package follow

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// maxExportBytes caps the export read from the lead.
const maxExportBytes = 8 << 20

// waitDelay bounds how long Run waits for output pipes after the command is
// killed, so a child holding them open cannot hang a cancelled pull.
const waitDelay = 2 * time.Second

// errExportTooLarge reports stdout past maxExportBytes.
var errExportTooLarge = fmt.Errorf("output exceeds %d bytes", maxExportBytes)

// ExecRunner runs commands with os/exec, killing them when ctx ends and
// failing a run whose stdout passes maxExportBytes.
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args []string) ([]byte, []byte, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stdout := &capped{limit: maxExportBytes, onOverflow: cancel}
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // argv built by leadArgv, no shell
	cmd.Stdout, cmd.Stderr = stdout, &stderr
	cmd.WaitDelay = waitDelay
	err := cmd.Run()
	if stdout.overflow {
		return stdout.buf.Bytes(), stderr.Bytes(), errExportTooLarge
	}
	return stdout.buf.Bytes(), stderr.Bytes(), err
}

// capped buffers up to limit bytes and then fails writes, cancelling the run.
type capped struct {
	buf        bytes.Buffer
	limit      int
	overflow   bool
	onOverflow func()
}

func (c *capped) Write(p []byte) (int, error) {
	if c.buf.Len()+len(p) > c.limit {
		c.overflow = true
		c.onOverflow()
		return 0, errors.New("stdout cap reached")
	}
	return c.buf.Write(p)
}
