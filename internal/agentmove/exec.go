// Package agentmove moves or copies an on-demand agent between Leo hosts:
// the manifest and destination preflight, the ssh/rsync/git transport, the
// move hooks, and the source-side job that runs them in order. See
// docs/specs/2026-10-27-leo-agent-move.md.
package agentmove

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Cmd is one external command a move runs. Every ssh, rsync and git call
// goes through a Runner as a Cmd, so tests can assert the exact argv.
type Cmd struct {
	Name  string
	Args  []string
	Dir   string
	Env   []string // added to the daemon's environment; later entries win
	Stdin []byte
}

func (c Cmd) String() string { return c.Name + " " + strings.Join(c.Args, " ") }

// Runner runs a Cmd and returns its stdout. A nonzero exit is an error
// carrying the tail of stderr.
type Runner interface {
	Run(ctx context.Context, c Cmd) ([]byte, error)
}

// ExecRunner runs commands with os/exec.
type ExecRunner struct{}

// maxErrTail is how much of a failed command's stderr an error carries.
const maxErrTail = 4096

func (ExecRunner) Run(ctx context.Context, c Cmd) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...) //nolint:gosec // argv built by this package's validated builders
	cmd.Dir = c.Dir
	if len(c.Env) > 0 {
		cmd.Env = append(os.Environ(), c.Env...)
	}
	if c.Stdin != nil {
		cmd.Stdin = bytes.NewReader(c.Stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), fmt.Errorf("%s: %w: %s", c.Name, err, tail(stderr.String(), maxErrTail))
	}
	return stdout.Bytes(), nil
}

// tail returns the last n bytes of s, trimmed.
func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return "…" + s[len(s)-n:]
	}
	return s
}
