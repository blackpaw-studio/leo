package web

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/consult"
	"github.com/blackpaw-studio/leo/internal/startup"
)

// restartReconcileServer builds a server whose consult runtime restarts over
// one record that was released (so terminal) with a worktree when the last
// daemon stopped: its assessment is left to the background restart
// reconcile, and starts with a git call. configure, if set, adjusts the
// server's seams before its consult runtime is set up.
func restartReconcileServer(t *testing.T, logf func(string, ...any), configure func(*Server)) *Server {
	t.Helper()
	stateDir, root := t.TempDir(), t.TempDir()
	wt := filepath.Join(root, "d-old")
	if err := os.MkdirAll(wt, 0o700); err != nil {
		t.Fatal(err)
	}
	recorder := consult.NewFileRecorder(stateDir)
	h, err := recorder.Open(consult.Record{ID: "d-old", Kind: "dispatch", Template: "worker", Mode: consult.ModeInteractive,
		Status: consult.StatusReleased, StartedAt: time.Now(), Isolation: "worktree", WorktreeState: consult.WorktreePresent,
		RepositoryRoot: root, Worktree: wt, BaseCommit: "base", Branch: "leo/d-old"})
	if err != nil {
		t.Fatal(err)
	}
	_ = h.Close(consult.StatusReleased, nil)
	s := &Server{
		ConfigService:      &ConfigService{configPath: "test.yaml", leoPath: "leo", execCommand: func(string, ...string) *exec.Cmd { return exec.Command("true") }},
		execCommandContext: func(ctx context.Context, _ string, _ ...string) *exec.Cmd { return exec.CommandContext(ctx, "true") },
		updateRoster:       func([]consult.Record, time.Time) {},
	}
	if configure != nil {
		configure(s)
	}
	opts := Options{ParentContext: context.Background(), ConsultRecorder: recorder, Startup: startup.New(time.Now, logf)}
	s.setupConsultRuntime(opts, func(string) (string, bool) { return "", false })
	return s
}

// lineLog collects log lines.
type lineLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *lineLog) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *lineLog) find(sub string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.lines {
		if strings.Contains(line, sub) {
			return line
		}
	}
	return ""
}

func TestShutdownCancelsRestartReconcile(t *testing.T) {
	var log lineLog
	s := restartReconcileServer(t, log.logf, nil)
	entered := make(chan struct{}, 1)
	s.consults.GitCommand = func(string, ...string) *exec.Cmd {
		select {
		case entered <- struct{}{}:
		default:
		}
		return exec.Command("sleep", "30")
	}

	s.StartRestartReconcile()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the restart reconcile never ran a git call")
	}
	shut := make(chan error, 1)
	go func() { shut <- s.Shutdown() }()
	select {
	case err := <-shut:
		if err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Shutdown did not return while the restart reconcile held a git call")
	}

	line := log.find("startup: restart reconcile:")
	if line == "" || !strings.Contains(line, "canceled") {
		t.Fatalf("restart reconcile summary = %q; Shutdown must cancel the pass and wait for it to exit", line)
	}
}

func TestConsultLoopHoldsPruneUntilRestartReconciled(t *testing.T) {
	var log lineLog
	var pruned atomic.Int32
	ticks := make(chan time.Time)
	s := restartReconcileServer(t, log.logf, func(s *Server) {
		s.viewerTicks = ticks
		s.pruneConsults = func() { pruned.Add(1) }
	})
	t.Cleanup(func() { _ = s.Shutdown() })
	// The loop takes one tick at a time: a second tick is taken only once
	// the first one's sweep and prune are done.
	tick := func() {
		t.Helper()
		select {
		case ticks <- time.Now():
		case <-time.After(5 * time.Second):
			t.Fatal("the consult loop stopped taking viewer ticks")
		}
	}

	tick()
	tick()
	if n := pruned.Load(); n != 0 {
		t.Fatalf("pruned %d times before the restart reconcile ran; it could delete a record the pass is about to rewrite", n)
	}
	s.StartRestartReconcile()
	for deadline := time.Now().Add(5 * time.Second); pruned.Load() == 0; {
		if time.Now().After(deadline) {
			t.Fatal("prune never resumed after the restart reconcile finished")
		}
		tick()
	}
	if log.find("startup: restart reconcile:") == "" {
		t.Fatal("prune resumed before the restart reconcile logged its summary")
	}
}
