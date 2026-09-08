package peerinbox

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDeliverWritesUserMessageLine(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "pi")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "inbox.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()

	line := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		buf := make([]byte, 1024)
		n, _ := conn.Read(buf)
		line <- string(buf[:n])
	}()
	if err := Deliver(context.Background(), path, "hello"); err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}
	select {
	case got := <-line:
		want := "{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":\"hello\"}}\n"
		if got != want {
			t.Errorf("received %q, want %q", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("did not receive socket message")
	}
}

func TestResolveSocket(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "pi")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	oldDirs := socketDirs
	socketDirs = []string{dir}
	defer func() { socketDirs = oldDirs }()

	for _, tt := range []struct {
		name      string
		children  map[string]string
		socketPID string
		wantErr   bool
	}{
		{name: "pane pid is claude", children: map[string]string{}, socketPID: "100"},
		{name: "claude is grandchild", children: map[string]string{"100": "101", "101": "102"}, socketPID: "102"},
		{name: "no socket anywhere", children: map[string]string{"100": "101"}, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.socketPID != "" {
				path := filepath.Join(dir, tt.socketPID+".sock")
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				defer os.Remove(path)
			}
			execFn := func(name string, args ...string) *exec.Cmd {
				if strings.Contains(strings.Join(args, " "), "display-message") {
					return exec.Command("printf", "100\\n")
				}
				pid := args[len(args)-1]
				return exec.Command("printf", tt.children[pid])
			}
			got, err := ResolveSocket(context.Background(), execFn, "leo-agent")
			if tt.wantErr {
				if err == nil {
					t.Fatal("ResolveSocket() error = nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveSocket() error = %v", err)
			}
			want := filepath.Join(dir, tt.socketPID+".sock")
			if got != want {
				t.Errorf("ResolveSocket() = %q, want %q", got, want)
			}
		})
	}
}
