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

	rt.ReleaseRunFiles("d-a")
	rt.ReleaseRunFiles("../../escape")
	rt.SweepRunFiles(map[string]bool{"d-b": true})

	assertGone(t, gone)
	assertGone(t, orphan)
	for _, p := range []string{kept, agentFile} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s removed: %v", p, err)
		}
	}
}
