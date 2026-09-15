package tmux

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestCallerPaneFromEnvValidatesLeoSocketOrigin(t *testing.T) {
	tmp := t.TempDir()
	leoSocket := filepath.Join(tmp, fmt.Sprintf("tmux-%d", os.Getuid()), SocketName)
	tests := []struct {
		name string
		env  []string
		want string
	}{
		{name: "leo socket", env: []string{"TMUX_TMPDIR=" + tmp, "TMUX=" + leoSocket + ",123,0", "TMUX_PANE=%7"}, want: "%7"},
		{name: "another socket with colliding pane id", env: []string{"TMUX_TMPDIR=" + tmp, "TMUX=" + filepath.Join(tmp, fmt.Sprintf("tmux-%d", os.Getuid()), "other") + ",123,0", "TMUX_PANE=%7"}},
		{name: "missing tmux", env: []string{"TMUX_TMPDIR=" + tmp, "TMUX_PANE=%7"}},
		{name: "empty tmux", env: []string{"TMUX_TMPDIR=" + tmp, "TMUX=", "TMUX_PANE=%7"}},
		{name: "malformed tmux", env: []string{"TMUX_TMPDIR=" + tmp, "TMUX=" + leoSocket, "TMUX_PANE=%7"}},
		{name: "missing pane", env: []string{"TMUX_TMPDIR=" + tmp, "TMUX=" + leoSocket + ",123,0"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pane, ok := CallerPaneFromEnv(tt.env)
			if pane != tt.want || ok != (tt.want != "") {
				t.Fatalf("CallerPaneFromEnv() = %q, %v; want %q, %v", pane, ok, tt.want, tt.want != "")
			}
		})
	}
}

func TestCallerPaneFromEnvAcceptsSymlinkedSocketPath(t *testing.T) {
	realTmp := t.TempDir()
	linkTmp := filepath.Join(t.TempDir(), "tmux-tmp")
	socketDir := filepath.Join(realTmp, fmt.Sprintf("tmux-%d", os.Getuid()))
	if err := os.MkdirAll(socketDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(socketDir, SocketName), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realTmp, linkTmp); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(realTmp, fmt.Sprintf("tmux-%d", os.Getuid()), SocketName)
	env := []string{"TMUX_TMPDIR=" + linkTmp, "TMUX=" + socket + ",123,0", "TMUX_PANE=%9"}
	if pane, ok := CallerPaneFromEnv(env); pane != "%9" || !ok {
		t.Fatalf("CallerPaneFromEnv() = %q, %v", pane, ok)
	}
}
