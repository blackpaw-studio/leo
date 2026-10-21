//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

const managedClaudeTemplate = "managedclaude"

// writeManagedMarker lays out what Leo.app would: a stable bin/leo symlink to
// the built leo, a tmux/<ver>/tmux copy, and state/managed.json naming both.
// The path contains a space ("Application Support") like the real one, so
// quoting regressions surface. Returns the self_path.
func writeManagedMarker(t *testing.T, home string) string {
	t.Helper()
	support := filepath.Join(home, "Application Support", "Leo")
	selfPath := filepath.Join(support, "bin", "leo")
	tmuxVersion := tmuxVersionDir(t)
	tmuxPath := filepath.Join(support, "tmux", tmuxVersion, "tmux")
	for target, link := range map[string]string{leoBin: selfPath, faketmux: tmuxPath} {
		if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	marker, err := json.Marshal(map[string]any{
		"version":       1,
		"manager":       "Leo.app",
		"bundle_id":     "studio.blackpaw.leo.macos",
		"launchd_label": "studio.blackpaw.leo.e2e",
		"self_path":     selfPath,
		"tmux_path":     tmuxPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(home, "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "managed.json"), marker, 0o600); err != nil {
		t.Fatal(err)
	}
	return selfPath
}

// tmuxVersionDir is `tmux -V` minus its "tmux " prefix, the <ver> directory
// name the marker's tmux_path uses.
func tmuxVersionDir(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("tmux", "-V").Output()
	if err != nil {
		t.Skipf("tmux -V: %v", err)
	}
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(out)), "tmux "))
}

// A managed daemon records the marker's stable self_path (not the binary it
// runs from) everywhere it persists leo's path, and refuses a second daemon on
// the same home without disturbing the first.
func TestManagedDaemonUsesStableSelfPathAndRefusesSecondDaemon(t *testing.T) {
	// managed.Load rejects the marker off darwin, so the daemon would ignore it.
	if runtime.GOOS != "darwin" {
		t.Skip("managed installs are darwin-only")
	}
	var selfPath string
	s := newInteractiveE2EWithOptions(t, interactiveOptions{
		beforeStart: func(ws string) { selfPath = writeManagedMarker(t, ws) },
		extraConfig: func(ws string) string {
			return fmt.Sprintf("  %s:\n    harness: claude\n    model: haiku\n    workspace: %s\n", managedClaudeTemplate, ws)
		},
	})

	t.Run("codex agent argv names self_path", func(t *testing.T) {
		session := agent.SessionName(s.spawnAgent(t, "managedcodex"))
		s.waitForSession(t, session)
		s.waitFor(t, func() bool {
			args, _ := os.ReadFile(filepath.Join(s.ws, "caller-args.json"))
			return strings.Contains(string(args), selfPath)
		})
	})

	t.Run("claude agent mcp config command is self_path", func(t *testing.T) {
		var out struct {
			Name string `json:"name"`
		}
		s.request(t, "POST", "/api/agent/spawn", map[string]string{"template": managedClaudeTemplate, "name": "managedclaude", "prompt": "stay alive"}, &out)
		var command string
		s.waitFor(t, func() bool {
			raw, err := os.ReadFile(filepath.Join(s.ws, "state", "leo-mcp.json"))
			if err != nil {
				return false
			}
			var parsed struct {
				MCPServers map[string]struct {
					Command string `json:"command"`
				} `json:"mcpServers"`
			}
			if json.Unmarshal(raw, &parsed) != nil {
				return false
			}
			command = parsed.MCPServers["leo"].Command
			return command != ""
		})
		if command != selfPath {
			t.Fatalf("leo-mcp.json command = %q, want self_path %q", command, selfPath)
		}
	})

	t.Run("prefix+L binding runs self_path", func(t *testing.T) {
		keys, err := exec.Command(s.tmux, tmux.Args("list-keys", "-T", "prefix", "L")...).CombinedOutput()
		if err != nil {
			t.Fatalf("list-keys: %v: %s", err, keys)
		}
		if !strings.Contains(string(keys), selfPath) {
			t.Fatalf("prefix+L binding does not run self_path %q:\n%s", selfPath, keys)
		}
	})

	t.Run("second daemon on the same home is refused", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, leoBin, "service", "--supervised", "-c", s.cfgPath)
		cmd.Dir = s.ws
		cmd.Env = append(append(s.daemonEnv(), "PATH="+filepath.Dir(fakeclaude)+":"+os.Getenv("PATH")), s.extraEnv...)
		out, err := cmd.CombinedOutput()

		if _, isExit := err.(*exec.ExitError); !isExit {
			t.Fatalf("second daemon err = %v, want a non-zero exit\n%s", err, out)
		}
		want := fmt.Sprintf("(pid %d)", s.service.Process.Pid)
		if !strings.Contains(string(out), "another leo daemon owns") || !strings.Contains(string(out), want) {
			t.Fatalf("second daemon output lacks the refusal naming %s:\n%s", want, out)
		}
		if !daemon.IsRunning(s.ws) {
			t.Fatal("the first daemon stopped answering on its socket")
		}
	})
}
