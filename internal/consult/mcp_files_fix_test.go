package consult

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
)

func newSweepRuntime(t *testing.T) (*TmuxInteractiveRuntime, string) {
	t.Helper()
	home := t.TempDir()
	cfg := &config.Config{HomePath: home}
	return NewInteractiveRuntime("", func() (*config.Config, error) { return cfg, nil }, nil, "tmux", "leo"), home
}

// A headless dispatch accepted while the startup sweep runs is non-terminal
// and owns its MCP file; only interactive runs used to be protected.
func TestSweepRunFilesKeepsFilesOfLiveHeadlessRuns(t *testing.T) {
	rt, home := newSweepRuntime(t)
	d := NewDispatcher(newFakeRecorder())
	d.SetInteractiveRuntime(rt)
	d.runs["d-headless"] = &runState{record: Record{ID: "d-headless", Kind: "dispatch", Mode: ModeHeadless, Status: StatusRunning}, handle: nopHandle{}, done: make(chan struct{})}
	d.runs["d-queued"] = &runState{record: Record{ID: "d-queued", Kind: "dispatch", Mode: ModeHeadless, Status: StatusQueued}, handle: nopHandle{}, done: make(chan struct{})}
	d.runs["d-ended"] = &runState{record: Record{ID: "d-ended", Kind: "dispatch", Mode: ModeHeadless, Status: StatusDone}, handle: nopHandle{}, done: make(chan struct{})}
	live := writeMCPConfig(t, home, "d-headless")
	queued := writeMCPConfig(t, home, "d-queued")
	ended := writeMCPConfig(t, home, "d-ended")

	summary := d.SweepRunFiles()

	for _, p := range []string{live, queued} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("live headless run's file removed: %v", err)
		}
	}
	assertGone(t, ended)
	if summary.Kept != 2 {
		t.Fatalf("summary = %+v, want 2 kept", summary)
	}
}

func TestContinuationMCPFileSurvivesSweepBeforeOwnershipIsPublished(t *testing.T) {
	f := newMCPDispatchFixture(t, map[string]any{"mcp_servers": []any{"chrome"}})
	d := NewDispatcher(NewFileRecorder(t.TempDir()))
	rt := NewInteractiveRuntime("", func() (*config.Config, error) { return f.cfg, nil }, nil, "tmux", "leo")
	d.SetInteractiveRuntime(rt)
	var calls []capturedRun
	var mu sync.Mutex
	d.ExecCommandContext = capturingExec(t, &calls, &mu)
	started, err := d.Start(context.Background(), f.cfg, Request{Template: "claude", Prompt: "go", Cwd: t.TempDir(), Kind: "dispatch"})
	if err != nil {
		t.Fatal(err)
	}
	d.Wait(context.Background(), []string{started.ID}, 5*time.Second)
	d.afterContinuationMCPWrite = func() { d.SweepRunFiles() }

	sent, err := d.SendWithConfig(context.Background(), f.cfg, started.ID, "again")
	if err != nil {
		t.Fatal(err)
	}
	d.Wait(context.Background(), []string{sent.TurnID}, 5*time.Second)

	mu.Lock()
	second := calls[1]
	mu.Unlock()
	if second.file == nil {
		t.Fatal("the startup sweep removed the continuation's MCP file before claude read it")
	}
}

// A cancel that finishes its cleanup before an in-flight launch writes the
// MCP file must not leave that file behind when the pane is then rejected.
func TestCanceledInteractiveLaunchRemovesMCPFileWrittenAfterCleanup(t *testing.T) {
	d := NewDispatcher(newFakeRecorder())
	dir := t.TempDir()
	rt := &fakeInteractiveRuntime{arm: true, empty: true}
	var path string
	rt.launchHook = func() {
		d.mu.Lock()
		var id string
		for k := range d.runs {
			id = k
		}
		d.mu.Unlock()
		if _, err := d.Cancel(id); err != nil {
			t.Error(err)
		}
		path = writeSpill(t, dir, id) // the launch writes its file after the cancel's cleanup
	}
	d.SetInteractiveRuntime(spillRuntime{fakeInteractiveRuntime: rt, dir: dir})

	_, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "x", Cwd: t.TempDir(), Mode: ModeInteractive})

	if err == nil {
		t.Fatal("start should report the cancel")
	}
	if path == "" {
		t.Fatal("launch hook never ran")
	}
	assertGone(t, path)
}

func TestSweepRunFilesRemovesStaleMCPTempFilesOnly(t *testing.T) {
	rt, home := newSweepRuntime(t)
	now := time.Now()
	rt.now = func() time.Time { return now }
	dir := filepath.Join(home, "state", "mcp")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	mk := func(name string, age time.Duration) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		at := now.Add(-age)
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
		return p
	}
	stale := mk(claudeharness.PrivateFileTempPrefix+"123.json", time.Hour)
	fresh := mk(claudeharness.PrivateFileTempPrefix+"456.json", time.Second)
	other := mk("unrelated.txt", time.Hour)

	rt.SweepRunFiles(func(string) bool { return false })

	assertGone(t, stale)
	for _, p := range []string{fresh, other} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s removed: %v", p, err)
		}
	}
}
