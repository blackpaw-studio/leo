package peercred

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/testenv"
)

const helperEnv = "PEERCRED_TEST_DIAL"

// TestMain doubles as the dialing child: re-executed with helperEnv set, the
// test binary connects to that socket and holds the connection until killed,
// so the listener sees a peer that is a different process.
func TestMain(m *testing.M) {
	if path := os.Getenv(helperEnv); path != "" {
		os.Exit(dialAndHold(path))
	}
	os.Exit(testenv.Main(m))
}

// dialAndHold connects to the socket at path and keeps the connection open,
// returning the child's exit code.
func dialAndHold(path string) int {
	conn, err := net.Dial("unix", path)
	if err != nil {
		return 2
	}
	defer func() { _ = conn.Close() }()
	time.Sleep(time.Minute)
	return 0
}

func listen(t *testing.T) (net.Listener, string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "pc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln, path
}

// The pid is the kernel's record of who connected, not anything the peer
// sent: a child process dialing is reported as that child.
func TestPIDIsTheDialingProcess(t *testing.T) {
	ln, path := listen(t)
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), helperEnv+"="+path)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	conn, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	pid, err := PID(conn)
	if err != nil {
		t.Fatalf("PID: %v", err)
	}
	if pid != cmd.Process.Pid {
		t.Fatalf("pid = %d, want the dialing child's %d (this process is %d)", pid, cmd.Process.Pid, os.Getpid())
	}
}

func TestPIDOfAConnectionThatIsNotAUnixSocketFailsClosed(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err == nil {
			defer func() { _ = c.Close() }()
			time.Sleep(time.Second)
		}
	}()
	conn, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	if pid, err := PID(conn); err == nil || pid != 0 {
		t.Fatalf("PID over tcp = %d, %v; want 0 and an error", pid, err)
	}
}

func TestConnContextRecordsThePeerOfAUnixConnectionOnly(t *testing.T) {
	ln, path := listen(t)
	go func() {
		c, err := net.Dial("unix", path)
		if err == nil {
			defer func() { _ = c.Close() }()
			time.Sleep(time.Second)
		}
	}()
	conn, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	ctx := ConnContext(context.Background(), conn)

	peer, ok := FromContext(ctx)
	if !ok || peer.Err != nil || peer.PID != os.Getpid() {
		t.Fatalf("peer = %+v, %v; want this process (it dialed) with no error", peer, ok)
	}
	if _, ok := FromContext(context.Background()); ok {
		t.Fatal("a context nobody recorded a peer in reported one")
	}
}

func TestVerifiedPIDRefusesWhatTheKernelDidNotSay(t *testing.T) {
	for name, ctx := range map[string]context.Context{
		"no peer recorded (tcp)": context.Background(),
		"peer lookup failed":     WithPeer(context.Background(), Peer{Err: errors.New("boom")}),
		"zero pid":               WithPeer(context.Background(), Peer{}),
	} {
		if pid, ok := VerifiedPID(ctx); ok || pid != 0 {
			t.Errorf("%s: VerifiedPID = %d, %v; want none", name, pid, ok)
		}
	}
	if pid, ok := VerifiedPID(WithPeer(context.Background(), Peer{PID: 4242})); !ok || pid != 4242 {
		t.Fatalf("VerifiedPID = %d, %v; want 4242", pid, ok)
	}
}
