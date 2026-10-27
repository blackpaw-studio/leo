package agentmove

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// hookWaitDelay bounds how long a killed hook's leftover children may hold
// its output pipe open.
const hookWaitDelay = 2 * time.Second

// hookSecretEnv never reaches a hook: the agent's own credentials.
var hookSecretEnv = map[string]bool{"LEO_API_TOKEN": true, "OPENCODE_CONFIG_CONTENT": true}

// HookEnv is what a move hook learns about the transfer. On a copy's
// post_receive, Agent, Workspace and SessionID are the new agent's.
type HookEnv struct {
	Agent     string
	Workspace string
	SessionID string
	// From and To are hostnames.
	From string
	To   string
	// Dir is the staging hooks/ dir Leo carries from source to destination.
	Dir string
	// Last is the previous move between this pair of hosts, zero for none.
	Last time.Time
	// Mode is "move" or "copy".
	Mode            string
	SourceAgent     string
	SourceWorkspace string
	SourceSessionID string
	// AgentEnv is the agent's resolved env on this host.
	AgentEnv map[string]string
}

// Environ is the hook's full environment: the daemon's, then the agent's,
// then the LEO_* move variables, never the agent's secrets.
func (h HookEnv) Environ() []string {
	last := ""
	if !h.Last.IsZero() && h.Mode != "copy" {
		last = h.Last.UTC().Format(time.RFC3339)
	}
	var out []string
	add := func(k, v string) {
		if !hookSecretEnv[k] {
			out = append(out, k+"="+v)
		}
	}
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		add(k, v)
	}
	for k, v := range h.AgentEnv {
		add(k, v)
	}
	for _, kv := range [][2]string{
		{"LEO_AGENT", h.Agent}, {"LEO_WORKSPACE", h.Workspace}, {"LEO_SESSION_ID", h.SessionID},
		{"LEO_MOVE_FROM", h.From}, {"LEO_MOVE_TO", h.To}, {"LEO_MOVE_DIR", h.Dir},
		{"LEO_MOVE_LAST", last}, {"LEO_MOVE_MODE", h.Mode},
		{"LEO_SOURCE_AGENT", h.SourceAgent}, {"LEO_SOURCE_WORKSPACE", h.SourceWorkspace},
		{"LEO_SOURCE_SESSION_ID", h.SourceSessionID},
	} {
		add(kv[0], kv[1])
	}
	return out
}

// HookRunner runs one phase's hooks (pre_send or post_receive) in order.
type HookRunner interface {
	RunHooks(ctx context.Context, phase string, paths []string, timeout time.Duration, env HookEnv) error
}

// ExecHooks execs each hook directly (no shell, no args) in the workspace,
// stopping at the first failure. Nothing is retried.
type ExecHooks struct{}

func (ExecHooks) RunHooks(ctx context.Context, phase string, paths []string, timeout time.Duration, env HookEnv) error {
	environ := env.Environ()
	for _, p := range paths {
		if err := runHook(ctx, p, timeout, env.Workspace, environ); err != nil {
			return fmt.Errorf("%s hook %w", phase, err)
		}
	}
	return nil
}

func runHook(ctx context.Context, path string, timeout time.Duration, dir string, environ []string) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path) //nolint:gosec // a validated absolute path from the user's own move config
	cmd.Dir = dir
	cmd.Env = environ
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	// Its own process group, so a timeout reaches whatever it spawned.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = hookWaitDelay
	err := cmd.Run()
	output := tail(out.String(), maxErrTail)
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return nil
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return fmt.Errorf("%s timed out after %s: %s", path, timeout, output)
	case errors.As(err, &exitErr):
		return fmt.Errorf("%s exited with status %d: %s", path, exitErr.ExitCode(), output)
	default:
		return fmt.Errorf("%s: %w", path, err)
	}
}
