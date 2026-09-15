package consult

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/peerinbox"
)

func TestNotificationReadyUsesExactPaneAndSessionArgvWithoutDraftKeys(t *testing.T) {
	var calls [][]string
	command := func(_ context.Context, name string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string{name}, args...))
		if len(calls) == 1 {
			return exec.Command("printf", "$9")
		}
		return exec.Command("printf", "› existing draft")
	}
	d := NewTmuxNotificationDelivery("/opt/tmux", command)
	r := Record{CallerPaneID: "%7", CallerSessionID: "$9", CallerHarness: "codex"}
	if d.Ready(context.Background(), r) {
		t.Fatal("draft reported ready")
	}
	want := []string{"/opt/tmux", "-L", "leo", "display-message", "-p", "-t", "%7", "#{session_id}"}
	if !reflect.DeepEqual(calls[0], want) {
		t.Fatalf("argv=%q want=%q", calls[0], want)
	}
	wantCapture := []string{"/opt/tmux", "-L", "leo", "capture-pane", "-p", "-t", "%7"}
	if len(calls) != 2 || !reflect.DeepEqual(calls[1], wantCapture) {
		t.Fatalf("calls=%q want capture=%q", calls, wantCapture)
	}
}

func TestClaudeNotificationDeliveryWritesExactSocketEnvelope(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "leo-inbox-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "inbox.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	bytes := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			bytes <- "accept: " + err.Error()
			return
		}
		defer func() { _ = conn.Close() }()
		raw, _ := io.ReadAll(conn)
		bytes <- string(raw)
	}()
	d := NewTmuxNotificationDelivery("tmux", func(context.Context, string, ...string) *exec.Cmd { return exec.Command("printf", "$1") })
	d.ResolveSocket = func(context.Context, peerinbox.ExecFunc, string) (string, error) { return path, nil }
	line := "[leo] dispatch d-x (job) done · active 0:01 — collect with leo_wait"
	if err := d.Deliver(context.Background(), Record{CallerPaneID: "%7", CallerSessionID: "$1", CallerHarness: "claude"}, line); err != nil {
		t.Fatal(err)
	}
	want := "{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":\"[leo] dispatch d-x (job) done · active 0:01 — collect with leo_wait\"}}\n"
	if got := <-bytes; got != want {
		t.Fatalf("socket bytes=%q want=%q", got, want)
	}
}

func TestNotificationDeliverComposerRaceIsProvenNotSent(t *testing.T) {
	call := 0
	d := NewTmuxNotificationDelivery("tmux", func(context.Context, string, ...string) *exec.Cmd {
		call++
		if call == 1 {
			return exec.Command("printf", "$1")
		}
		return exec.Command("printf", "• Working · esc to interrupt\n›")
	})
	err := d.Deliver(context.Background(), Record{CallerPaneID: "%1", CallerSessionID: "$1", CallerHarness: "codex"}, "line")
	if !errors.Is(err, ErrNotificationNotSent) {
		t.Fatalf("error=%v", err)
	}
}

func TestOpenCodeNotificationDeliverySubmitsWithExactEnterArgv(t *testing.T) {
	var calls [][]string
	captures := 0
	d := NewTmuxNotificationDelivery("/opt/tmux", func(_ context.Context, name string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string{name}, args...))
		subcommand := args[2]
		switch subcommand {
		case "display-message":
			return exec.Command("printf", "$1")
		case "capture-pane":
			captures++
			if captures == 1 {
				return exec.Command("printf", "┃\\n┃ Ask anything...\\n┃\\n┃ Build · model")
			}
			return exec.Command("printf", "┃\\n┃ notification line\\n┃\\n┃ Build · model")
		default:
			return exec.Command("true")
		}
	})
	if err := d.Deliver(context.Background(), Record{CallerPaneID: "%7", CallerSessionID: "$1", CallerHarness: "opencode"}, "notification line"); err != nil {
		t.Fatal(err)
	}
	want := []string{"/opt/tmux", "-L", "leo", "send-keys", "-t", "%7", "Enter"}
	if !reflect.DeepEqual(calls[len(calls)-1], want) {
		t.Fatalf("last argv=%q want=%q", calls[len(calls)-1], want)
	}
}

func TestNotificationReadinessCommandHasDeadline(t *testing.T) {
	d := NewTmuxNotificationDelivery("tmux", func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "sleep 5")
	})
	d.CommandTimeout = 20 * time.Millisecond
	started := time.Now()
	if d.Ready(context.Background(), Record{CallerPaneID: "%1", CallerSessionID: "$1", CallerHarness: "codex"}) {
		t.Fatal("blocked command ready")
	}
	if time.Since(started) > time.Second {
		t.Fatal("readiness command was not bounded")
	}
}

func TestNotificationReadyRejectsStaleCallerPaneBeforeCapture(t *testing.T) {
	var calls int
	d := NewTmuxNotificationDelivery("tmux", func(context.Context, string, ...string) *exec.Cmd { calls++; return exec.Command("printf", "$new") })
	if d.Ready(context.Background(), Record{CallerPaneID: "%7", CallerSessionID: "$old", CallerHarness: "codex"}) {
		t.Fatal("stale pane ready")
	}
	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
}
