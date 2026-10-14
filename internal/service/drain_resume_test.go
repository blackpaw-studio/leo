package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/drain"
)

type resumeLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *resumeLog) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

type countingNotifier struct {
	mu   sync.Mutex
	sent []string
}

func (n *countingNotifier) Message(_ context.Context, name, text, _ string) (drain.Delivery, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.sent = append(n.sent, name+": "+text)
	return drain.Delivery{}, nil
}

func (*countingNotifier) Holds(string, string) (bool, error) { return false, nil }

func pendingResumeFixture(t *testing.T, homePath string, n *countingNotifier, log *resumeLog) pendingResumeDeps {
	t.Helper()
	return pendingResumeDeps{
		store: drain.NewFileStore(filepath.Join(homePath, "state")),
		resume: drain.ResumeDeps{
			Exists:   agentRecordExists(homePath),
			Notifier: n,
			RunTasks: func([]string) {},
			Logf:     log.logf,
		},
		logf: log.logf,
	}
}

func TestRunPendingResumeWithoutFileDoesNothing(t *testing.T) {
	home := t.TempDir()
	n, log := &countingNotifier{}, &resumeLog{}
	runPendingResume(context.Background(), pendingResumeFixture(t, home, n, log))
	if len(n.sent) != 0 || len(log.lines) != 0 {
		t.Fatalf("sent %q, logged %q; want nothing", n.sent, log.lines)
	}
}

func TestRunPendingResumeCorruptFileLogsAndBoots(t *testing.T) {
	home := t.TempDir()
	state := filepath.Join(home, "state")
	if err := os.MkdirAll(state, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "pending-resume.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	n, log := &countingNotifier{}, &resumeLog{}
	runPendingResume(context.Background(), pendingResumeFixture(t, home, n, log))
	if len(n.sent) != 0 {
		t.Fatalf("sent %q from a corrupt file", n.sent)
	}
	if !strings.Contains(strings.Join(log.lines, "\n"), "pending-resume.json") {
		t.Fatalf("corrupt file not logged: %q", log.lines)
	}
}

func TestRunPendingResumeMessagesRecordedAgents(t *testing.T) {
	home := t.TempDir()
	if err := agentstore.Save(home, agentstore.Record{Name: "alpha"}); err != nil {
		t.Fatal(err)
	}
	store := drain.NewFileStore(filepath.Join(home, "state"))
	if err := store.Write(drain.PendingResume{DrainID: "d1", Agents: []string{"alpha", "gone"}}); err != nil {
		t.Fatal(err)
	}
	n, log := &countingNotifier{}, &resumeLog{}
	runPendingResume(context.Background(), pendingResumeFixture(t, home, n, log))
	if want := []string{"alpha: " + drain.ResumeText}; strings.Join(n.sent, "|") != strings.Join(want, "|") {
		t.Fatalf("sent %q, want %q", n.sent, want)
	}
	if _, ok, _ := store.Take(); ok {
		t.Fatal("pending-resume.json survived the resume")
	}
}
