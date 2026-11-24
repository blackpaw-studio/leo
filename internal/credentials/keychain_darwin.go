//go:build darwin

package credentials

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"time"
)

// waitDelay bounds the wait on a wedged child after its context ends.
const waitDelay = time.Second

// NewSystemTokenSource reads Claude's login from the macOS Keychain through
// /usr/bin/security, then falls back to the file.
func NewSystemTokenSource(home, user string) TokenSource {
	return TokenSource{Run: execRunner, User: user, Home: home, UseKeychain: true}
}

// execRunner is the real Runner: no shell, stderr discarded, stdout returned.
func execRunner(ctx context.Context, name string, args ...string) ([]byte, int, error) {
	var stdout bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = waitDelay
	cmd.Stdout = &stdout
	err := cmd.Run()
	if err == nil {
		return stdout.Bytes(), 0, nil
	}
	if ctx.Err() != nil {
		return nil, 0, ctx.Err()
	}
	var exit interface{ ExitCode() int }
	if errors.As(err, &exit) {
		return nil, exit.ExitCode(), nil
	}
	return nil, 0, errors.New("could not run " + name)
}
