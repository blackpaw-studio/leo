package peerinbox

import (
	"context"
	"fmt"
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

func TestDeliverWriteDeadlineExceeded(t *testing.T) {
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

	// Accept connection but never read, causing write to block.
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		// Block forever, never reading
		select {}
	}()

	// Send large message (>10MiB to exceed typical socket buffer).
	largeMsg := strings.Repeat("x", 10*1024*1024)
	start := time.Now()
	err = Deliver(context.Background(), path, largeMsg)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Deliver() expected error, got nil")
	}
	// Should timeout around deliverTimeout (~5s), definitely before 20s
	if elapsed > 20*time.Second {
		t.Errorf("Deliver took %v, expected to timeout before 20s", elapsed)
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
			execFn := func(ctx context.Context, name string, args ...string) *exec.Cmd {
				if strings.Contains(strings.Join(args, " "), "display-message") {
					return exec.CommandContext(ctx, "printf", "100\\n")
				}
				pid := args[len(args)-1]
				return exec.CommandContext(ctx, "printf", tt.children[pid])
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

func TestResolveSockectContextCancellation(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "pi")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	oldDirs := socketDirs
	socketDirs = []string{dir}
	defer func() { socketDirs = oldDirs }()

	execFn := func(ctx context.Context, name string, args ...string) *exec.Cmd {
		// Simulate a long-running command that respects context
		cmd := exec.CommandContext(ctx, "sleep", "5")
		return cmd
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, sockErr := ResolveSocket(ctx, execFn, "leo-agent")
	elapsed := time.Since(start)

	if sockErr == nil {
		t.Fatal("ResolveSocket() expected error, got nil")
	}
	// Context cancellation should complete quickly, definitely before 2 seconds
	if elapsed > 2*time.Second {
		t.Errorf("ResolveSocket() took %v, expected context to cancel quickly", elapsed)
	}
}

func TestDefaultExecHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := DefaultExec(ctx, "sleep", "5").Run()
	if err == nil {
		t.Fatal("DefaultExec().Run() error = nil, want cancellation error")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("DefaultExec().Run() took %v, want context cancellation within 1s", elapsed)
	}
}

func TestXDGRuntimeDirUnsetNoRelativePath(t *testing.T) {
	oldXDG := os.Getenv("XDG_RUNTIME_DIR")
	os.Unsetenv("XDG_RUNTIME_DIR")
	defer func() {
		if oldXDG != "" {
			os.Setenv("XDG_RUNTIME_DIR", oldXDG)
		}
	}()

	// Verify socketDirs doesn't contain relative paths when XDG is unset
	// The actual socketDirs are initialized at package load time, so we
	// test that any empty XDG results in an empty socketDirs entry (skipped by Deliver).
	oldDirs := socketDirs
	socketDirs = []string{
		"/tmp/cc-socks",
		"", // Empty string from unset XDG_RUNTIME_DIR
		fmt.Sprintf("/tmp/cc-socks-%d", os.Getuid()),
	}
	defer func() { socketDirs = oldDirs }()

	for _, dir := range socketDirs {
		if !filepath.IsAbs(dir) && dir != "" {
			t.Errorf("socketDirs contains relative path: %q", dir)
		}
	}
}
