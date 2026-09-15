package tmux

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// CallerPaneFromEnv returns TMUX_PANE only when the environment proves that
// the caller is running inside Leo's dedicated tmux server.
func CallerPaneFromEnv(environ []string) (string, bool) {
	getenv := func(key string) string {
		prefix := key + "="
		for i := len(environ) - 1; i >= 0; i-- {
			if strings.HasPrefix(environ[i], prefix) {
				return strings.TrimPrefix(environ[i], prefix)
			}
		}
		return ""
	}
	pane := getenv("TMUX_PANE")
	parts := strings.Split(getenv("TMUX"), ",")
	if pane == "" || len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", false
	}
	tmpDir := getenv("TMUX_TMPDIR")
	if tmpDir == "" {
		tmpDir = "/tmp"
	}
	want := filepath.Join(tmpDir, "tmux-"+strconv.Itoa(os.Getuid()), SocketName)
	if parts[0] == want || sameResolvedPath(parts[0], want) {
		return pane, true
	}
	return "", false
}

func sameResolvedPath(a, b string) bool {
	resolvedA, err := filepath.EvalSymlinks(a)
	if err != nil {
		return false
	}
	resolvedB, err := filepath.EvalSymlinks(b)
	return err == nil && resolvedA == resolvedB
}
