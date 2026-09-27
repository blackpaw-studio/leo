package consult

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

// spillRuntime is a fakeInteractiveRuntime whose run-file cleanup deletes
// dispatch-<id>.json under dir, like the tmux runtime's settings spill.
type spillRuntime struct {
	*fakeInteractiveRuntime
	dir string
}

func (r spillRuntime) ReleaseRunFiles(id string) {
	_ = os.Remove(filepath.Join(r.dir, "dispatch-"+id+".json"))
}

func (r spillRuntime) SweepRunFiles(keep map[string]bool) {
	matches, _ := filepath.Glob(filepath.Join(r.dir, "dispatch-*.json"))
	for _, m := range matches {
		id := filepath.Base(m)[len("dispatch-") : len(filepath.Base(m))-len(".json")]
		if !keep[id] {
			_ = os.Remove(m)
		}
	}
}

func writeSpill(t *testing.T, dir, id string) string {
	t.Helper()
	path := filepath.Join(dir, "dispatch-"+id+".json")
	if err := os.WriteFile(path, []byte(`{"env":{"K":"secret"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertGone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("spill file %s still exists (err %v)", path, err)
	}
}

func TestReleasedRunLeavesNoSpillFile(t *testing.T) {
	d, fake, id := releaseState(t, StatusIdle)
	dir := t.TempDir()
	d.SetInteractiveRuntime(spillRuntime{fakeInteractiveRuntime: fake, dir: dir})
	path := writeSpill(t, dir, id)

	if _, err := d.Release(id); err != nil {
		t.Fatal(err)
	}

	assertGone(t, path)
}

func TestCanceledRunLeavesNoSpillFile(t *testing.T) {
	d := NewDispatcher(newFakeRecorder())
	dir := t.TempDir()
	d.SetInteractiveRuntime(spillRuntime{fakeInteractiveRuntime: &fakeInteractiveRuntime{arm: true, empty: true, alive: true}, dir: dir})
	s, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "x", Cwd: t.TempDir(), Mode: ModeInteractive})
	if err != nil {
		t.Fatal(err)
	}
	path := writeSpill(t, dir, s.ID)

	if _, err := d.Cancel(s.ID); err != nil {
		t.Fatal(err)
	}

	assertGone(t, path)
}

func TestSweepRunFilesKeepsOnlyLiveRuns(t *testing.T) {
	d, fake, live := releaseState(t, StatusIdle)
	dir := t.TempDir()
	d.SetInteractiveRuntime(spillRuntime{fakeInteractiveRuntime: fake, dir: dir})
	d.runs["d-done"] = &runState{record: Record{ID: "d-done", Kind: "dispatch", Mode: ModeInteractive, Status: StatusClosed}, handle: nopHandle{}, done: make(chan struct{})}
	livePath := writeSpill(t, dir, live)
	donePath := writeSpill(t, dir, "d-done")
	orphan := writeSpill(t, dir, "d-forgotten")

	d.SweepRunFiles()

	if _, err := os.Stat(livePath); err != nil {
		t.Fatalf("live run's spill file removed: %v", err)
	}
	assertGone(t, donePath)
	assertGone(t, orphan)
}

func TestTmuxRuntimeRunFileCleanup(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home}
	rt := NewInteractiveRuntime("", func() (*config.Config, error) { return cfg, nil }, nil, "tmux", "leo")
	dir := filepath.Join(home, "state", "settings")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	gone := writeSpill(t, dir, "d-a")
	kept := writeSpill(t, dir, "d-b")
	orphan := writeSpill(t, dir, "d-c")
	agentFile := filepath.Join(dir, "leo-agent.json")
	if err := os.WriteFile(agentFile, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// A claude opening brief (see claudeDeliversPromptViaArgv) leaves a
	// second per-dispatch file behind, under its own directory; it must be
	// cleaned up on exactly the same schedule as the settings spill.
	briefGone := writeBrief(t, home, "d-a")
	briefKept := writeBrief(t, home, "d-b")
	briefOrphan := writeBrief(t, home, "d-c")

	rt.ReleaseRunFiles("d-a")
	rt.ReleaseRunFiles("../../escape")
	rt.SweepRunFiles(map[string]bool{"d-b": true})

	assertGone(t, gone)
	assertGone(t, orphan)
	assertGone(t, briefGone)
	assertGone(t, briefOrphan)
	for _, p := range []string{kept, agentFile, briefKept} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s removed: %v", p, err)
		}
	}
}

// TestDispatcherSweepRunFilesRemovesBriefFilesForTerminalAndUnknownRuns
// covers the safety net for a brief file written after its dispatch already
// went terminal (e.g. a cancel racing Launch): the per-dispatch
// ReleaseRunFiles call on the cancel path can miss a file that did not exist
// yet, but the periodic/startup Dispatcher.SweepRunFiles must still remove
// it once the dispatch reads back as terminal (or is not tracked at all —
// "unknown"), exactly like it already does for the settings spill.
func TestDispatcherSweepRunFilesRemovesBriefFilesForTerminalAndUnknownRuns(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home}
	rt := NewInteractiveRuntime("", func() (*config.Config, error) { return cfg, nil }, nil, "tmux", "leo")
	d := NewDispatcher(newFakeRecorder())
	d.SetInteractiveRuntime(rt)
	d.runs["d-live"] = &runState{record: Record{ID: "d-live", Kind: "dispatch", Mode: ModeInteractive, Status: StatusRunning}, handle: nopHandle{}, done: make(chan struct{})}
	d.runs["d-terminal"] = &runState{record: Record{ID: "d-terminal", Kind: "dispatch", Mode: ModeInteractive, Status: StatusClosed}, handle: nopHandle{}, done: make(chan struct{})}
	live := writeBrief(t, home, "d-live")
	terminal := writeBrief(t, home, "d-terminal")
	unknown := writeBrief(t, home, "d-unknown") // never in d.runs at all

	d.SweepRunFiles()

	if _, err := os.Stat(live); err != nil {
		t.Fatalf("live run's brief file removed: %v", err)
	}
	assertGone(t, terminal)
	assertGone(t, unknown)
}

// writeBrief writes a dispatch id's opening-brief file directly (bypassing
// Launch) so run-file cleanup tests can assert on it independently.
func writeBrief(t *testing.T, home, id string) string {
	t.Helper()
	path := dispatchBriefPath(home, id)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("brief for "+id), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
