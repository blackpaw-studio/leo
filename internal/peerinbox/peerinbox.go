// Package peerinbox delivers messages to Claude Code's per-session inbox.
package peerinbox

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/blackpaw-studio/leo/internal/tmux"
)

// ExecFunc supplies commands for resolving a tmux pane's Claude process.
type ExecFunc func(ctx context.Context, name string, args ...string) *exec.Cmd

// Process ancestry depth: shell → claude wrapper → claude.
const descendantDepth = 3

const deliverTimeout = 5 * time.Second

// socketDirs is package-scoped so tests can use short temporary socket paths.
var socketDirs = []string{
	"/tmp/cc-socks",
	func() string {
		xdg := os.Getenv("XDG_RUNTIME_DIR")
		if xdg == "" {
			return ""
		}
		return filepath.Join(xdg, "cc-socks")
	}(),
	fmt.Sprintf("/tmp/cc-socks-%d", os.Getuid()),
}

// ResolveSocket finds the first Claude inbox socket owned by tmuxSession's
// pane process or one of its descendants (up to three process levels).
func ResolveSocket(ctx context.Context, execFn ExecFunc, tmuxSession string) (string, error) {
	if execFn == nil {
		return "", fmt.Errorf("resolve peer inbox socket: exec function is required")
	}
	if strings.TrimSpace(tmuxSession) == "" {
		return "", fmt.Errorf("resolve peer inbox socket: tmux session is required")
	}
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("resolve peer inbox socket: %w", err)
	}

	panePID, err := execFn(ctx, "tmux", tmux.Args("display-message", "-p", "-t", tmuxSession, "#{pane_pid}")...).Output()
	if err != nil {
		return "", fmt.Errorf("resolve peer inbox socket: pane PID: %w", err)
	}
	root := strings.TrimSpace(string(panePID))
	if root == "" {
		return "", fmt.Errorf("resolve peer inbox socket: empty pane PID")
	}

	pids := []string{root}
	frontier := []string{root}
	for depth := 0; depth < descendantDepth; depth++ {
		next := make([]string, 0)
		for _, pid := range frontier {
			if err := ctx.Err(); err != nil {
				return "", fmt.Errorf("resolve peer inbox socket: %w", err)
			}
			out, err := execFn(ctx, "pgrep", "-P", pid).Output()
			if err != nil {
				continue // pgrep exits 1 when a process has no children.
			}
			for _, child := range strings.Fields(string(out)) {
				next = append(next, child)
				pids = append(pids, child)
			}
		}
		frontier = next
	}

	for _, pid := range pids {
		for _, dir := range socketDirs {
			if dir == "" {
				continue
			}
			path := filepath.Join(dir, pid+".sock")
			// Note: os.Stat checks existence but not liveness; a stale socket
			// will fail at dial time, triggering the tmux fallback.
			if _, err := os.Stat(path); err == nil {
				return path, nil
			}
		}
	}
	return "", fmt.Errorf("resolve peer inbox socket: no socket for pane PID %s or descendants", root)
}

// Deliver writes one newline-delimited Claude Code user-message envelope.
func Deliver(ctx context.Context, socketPath, text string) error {
	if strings.TrimSpace(socketPath) == "" {
		return fmt.Errorf("deliver peer inbox message: socket path is required")
	}
	if text == "" {
		return fmt.Errorf("deliver peer inbox message: text is required")
	}
	line, err := json.Marshal(struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	}{
		Type: "user",
		Message: struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}{Role: "user", Content: text},
	})
	if err != nil {
		return fmt.Errorf("deliver peer inbox message: encode: %w", err)
	}

	dialer := net.Dialer{Timeout: deliverTimeout}
	conn, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return fmt.Errorf("deliver peer inbox message: dial %q: %w", socketPath, err)
	}
	defer func(c net.Conn) { _ = c.Close() }(conn)

	_ = conn.SetDeadline(time.Now().Add(deliverTimeout))
	if _, err := conn.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("deliver peer inbox message: write: %w", err)
	}
	return nil
}
