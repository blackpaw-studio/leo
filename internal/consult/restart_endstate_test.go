package consult

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// endStateRuntime is an adopting, viewer-finding, run-file-sweeping fake
// whose panes each have their own presence, for the restart end-state
// fixture.
type endStateRuntime struct {
	*fakeInteractiveRuntime
	mu       sync.Mutex
	presence map[string]PanePresence
	probeErr map[string]bool
	owned    map[string]bool
	viewers  map[string]string
	killed   []string
	spillDir string
}

func (r *endStateRuntime) presenceOf(pane string) (PanePresence, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.probeErr[pane] {
		return PaneAbsent, errors.New("tmux timed out")
	}
	return r.presence[pane], nil
}

func (r *endStateRuntime) PanePresence(pane string) (PanePresence, error) { return r.presenceOf(pane) }

func (r *endStateRuntime) PaneAlive(pane string) (bool, error) {
	p, err := r.presenceOf(pane)
	return p == PanePresentAlive, err
}

func (r *endStateRuntime) Alive(pane string) bool { alive, _ := r.PaneAlive(pane); return alive }

func (r *endStateRuntime) Kill(pane string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.killed = append(r.killed, pane)
	r.presence[pane] = PaneAbsent
	return nil
}

func (r *endStateRuntime) FindPaneByDispatchID(_, id string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.viewers[id], nil
}

func (r *endStateRuntime) ProbeAdoption(pane, _ string) (AdoptProbe, error) {
	p, err := r.presenceOf(pane)
	r.mu.Lock()
	defer r.mu.Unlock()
	return AdoptProbe{Presence: p, Owned: r.owned[pane]}, err
}

func (r *endStateRuntime) AdoptPane(string, string, string)                 {}
func (r *endStateRuntime) AdoptBridge(string, string, string, string) error { return nil }
func (r *endStateRuntime) PaneSnapshot(string) (string, bool, error)        { return "", false, nil }

func (r *endStateRuntime) ReleaseRunFiles(id string) {
	_ = os.Remove(filepath.Join(r.spillDir, dispatchSpillPrefix+id+".json"))
}

func (r *endStateRuntime) SweepRunFiles(keep map[string]bool) int {
	return sweepGlob(filepath.Join(r.spillDir, dispatchSpillPrefix+"*.json"), dispatchSpillPrefix, ".json", "settings spill", keep)
}

func (r *endStateRuntime) killedPanes() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Sorted(slices.Values(r.killed))
}

// endStateFixture lays out one record of every shape a restart reconciles,
// over a real repository, and returns the dispatcher about to reconcile them.
func endStateFixture(t *testing.T) (*Dispatcher, *endStateRuntime, string, string) {
	t.Helper()
	repo, stateDir := gitTestRepo(t), t.TempDir()
	caller := func(r *Record) { r.Notify, r.CallerPaneID, r.CallerHarness = true, "%1", "claude" }
	released := func(r *Record) { r.Status = StatusReleased }

	// Records that were terminal when the previous daemon stopped.
	seedIsolated(t, repo, stateDir, "d-t-clean", released)
	dirty := seedIsolated(t, repo, stateDir, "d-t-dirty", released)
	if err := os.WriteFile(filepath.Join(dirty.Worktree, "scratch.txt"), []byte("wip\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	unmerged := seedIsolated(t, repo, stateDir, "d-t-unmerged", released)
	runGit(t, unmerged.Worktree, "commit", "--allow-empty", "-m", "unmerged work")
	seedIsolated(t, repo, stateDir, "d-t-livepane", func(r *Record) {
		r.PaneID, r.ViewerKind, r.CallerWindowID = "%21", "split", "@3"
	})
	seedIsolated(t, repo, stateDir, "d-t-uncertain", func(r *Record) { r.PaneID = "%22" })
	seedIsolated(t, repo, stateDir, "d-t-notify", func(r *Record) {
		caller(r)
		r.Mode = ModeHeadless
		r.Notifications = map[string]Notification{"d-t-notify#1": {Disposition: NotificationPending, PendingAt: adoptNow}}
	})
	restartRecord(t, stateDir, Record{ID: "d-t-viewer-found", Kind: "dispatch", Template: "worker", Mode: ModeHeadless,
		Status: StatusDone, StartedAt: adoptNow, ViewerKind: "split", ViewerTitle: "worker", CallerWindowID: "@7"})
	restartRecord(t, stateDir, Record{ID: "d-t-viewer-gone", Kind: "dispatch", Template: "worker", Mode: ModeInteractive,
		Status: StatusDone, StartedAt: adoptNow, ViewerKind: "split", ViewerTitle: "worker", CallerWindowID: "@7"})

	// Records the previous daemon left in flight.
	present := seedIsolated(t, repo, stateDir, "d-c-present", func(r *Record) {
		r.Mode, r.Status, r.WorktreeState = ModeHeadless, StatusRunning, WorktreeCreating
	})
	restartRecord(t, stateDir, Record{ID: "d-c-absent", Kind: "dispatch", Template: "worker", Mode: ModeHeadless,
		Status: StatusRunning, StartedAt: adoptNow, Isolation: "worktree", WorktreeState: WorktreeCreating,
		RepositoryRoot: repo, SourceCwd: repo, BaseCommit: present.BaseCommit,
		Worktree: filepath.Join(stateDir, worktreesDirName, "d-c-absent"), Branch: "leo/d-c-absent"})
	seedIsolated(t, repo, stateDir, "d-r-headless", func(r *Record) {
		r.Mode, r.Status, r.WorktreeState = ModeHeadless, StatusRunning, WorktreePresent
	})
	seedIsolated(t, repo, stateDir, "d-r-nopane", func(r *Record) {
		caller(r)
		r.Status, r.WorktreeState = StatusRunning, WorktreePresent
		r.Turns = []Turn{{TurnID: "d-r-nopane#1", Source: TurnSourceOrchestrator, StartedAt: adoptNow, Delivered: true, SlotHeld: true}}
	})
	adopt := liveInteractiveRecord("d-r-adopt", Turn{TurnID: "d-r-adopt#1", Source: TurnSourceOrchestrator, StartedAt: adoptNow, Delivered: true, SlotHeld: true})
	adopt.Harness, adopt.PaneID, adopt.StartedAt = "codex", "%41", adoptNow
	restartRecord(t, stateDir, adopt)

	spillDir := filepath.Join(stateDir, "settings")
	if err := os.MkdirAll(spillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"d-r-adopt", "d-t-clean", "d-dead"} {
		if err := os.WriteFile(filepath.Join(spillDir, dispatchSpillPrefix+id+".json"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rt := &endStateRuntime{
		fakeInteractiveRuntime: &fakeInteractiveRuntime{},
		presence:               map[string]PanePresence{"%21": PanePresentAlive, "%41": PanePresentAlive},
		probeErr:               map[string]bool{"%22": true},
		owned:                  map[string]bool{"%41": true},
		viewers:                map[string]string{"d-t-viewer-found": "%31", "d-t-viewer-gone": ""},
		spillDir:               spillDir,
	}
	d := NewDispatcher(NewFileRecorder(stateDir))
	d.now = func() time.Time { return adoptNow }
	d.SetInteractiveRuntime(rt)
	return d, rt, repo, stateDir
}

// restartEndState renders what a restart left behind, one line per record
// plus the worktrees, branches, run files and live runs, with timestamps and
// commit hashes dropped.
func restartEndState(t *testing.T, d *Dispatcher, rt *endStateRuntime, repo, stateDir string) string {
	t.Helper()
	records, err := Load(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })
	var b strings.Builder
	for _, rec := range records {
		notes := make([]string, 0, len(rec.Notifications))
		for key, n := range rec.Notifications {
			notes = append(notes, key+":"+string(n.Disposition))
		}
		sort.Strings(notes)
		fmt.Fprintf(&b, "%s status=%s wt=%s tip=%t pane=%s viewer_pane=%s viewer_kind=%s error=%q notes=%s\n",
			rec.ID, rec.Status, rec.WorktreeState, rec.BranchTip != "", rec.PaneID, rec.ViewerPaneID, rec.ViewerKind, rec.Error, strings.Join(notes, ","))
	}
	entries, _ := os.ReadDir(filepath.Join(stateDir, worktreesDirName))
	var dirs []string
	for _, e := range entries {
		dirs = append(dirs, e.Name())
	}
	branches := strings.Fields(strings.ReplaceAll(runGit(t, repo, "branch", "--list", "leo/*", "--format=%(refname:short)"), "\n", " "))
	spills, _ := filepath.Glob(filepath.Join(rt.spillDir, "*"))
	for i := range spills {
		spills[i] = filepath.Base(spills[i])
	}
	d.mu.Lock()
	runs := slices.Sorted(func(yield func(string) bool) {
		for id := range d.runs {
			if !yield(id) {
				return
			}
		}
	})
	d.mu.Unlock()
	rt.fakeInteractiveRuntime.mu.Lock()
	layouts := slices.Sorted(slices.Values(rt.layouts))
	rt.fakeInteractiveRuntime.mu.Unlock()
	fmt.Fprintf(&b, "worktrees=%s\nbranches=%s\nspills=%s\nruns=%s\nkilled=%s\nlayouts=%s\n",
		strings.Join(dirs, ","), strings.Join(branches, ","), strings.Join(spills, ","), strings.Join(runs, ","),
		strings.Join(rt.killedPanes(), ","), strings.Join(layouts, ","))
	return b.String()
}

// TestRestartReconcileEndState pins what a restart leaves behind for every
// record shape. It characterizes the synchronous reconciliation as it was
// before the startup-stall fix: moving work off the critical path must
// reach exactly this end state.
func TestRestartReconcileEndState(t *testing.T) {
	d, rt, repo, stateDir := endStateFixture(t)

	d.MarkInterrupted()
	d.SweepRunFiles()

	got := restartEndState(t, d, rt, repo, stateDir)
	want := `d-c-absent status=failed wt=removed tip=false pane= viewer_pane= viewer_kind= error="daemon restarted" notes=d-c-absent#1:suppressed
d-c-present status=failed wt=deferred tip=false pane= viewer_pane= viewer_kind= error="daemon restarted" notes=d-c-present#1:suppressed
d-r-adopt status=running wt= tip=false pane=%41 viewer_pane= viewer_kind= error="" notes=
d-r-headless status=failed wt=deferred tip=false pane= viewer_pane= viewer_kind= error="daemon restarted" notes=d-r-headless#1:suppressed
d-r-nopane status=failed wt=removed tip=true pane= viewer_pane= viewer_kind= error="daemon restarted" notes=d-r-nopane#1:pending,d-r-nopane#closed:pending
d-t-clean status=released wt=removed tip=true pane= viewer_pane= viewer_kind= error="" notes=
d-t-dirty status=released wt=kept tip=false pane= viewer_pane= viewer_kind= error="" notes=
d-t-livepane status=done wt=removed tip=true pane= viewer_pane= viewer_kind= error="" notes=
d-t-notify status=done wt=deferred tip=false pane= viewer_pane= viewer_kind= error="" notes=d-t-notify#1:pending
d-t-uncertain status=done wt=deferred tip=false pane=%22 viewer_pane= viewer_kind= error="" notes=
d-t-unmerged status=released wt=kept tip=false pane= viewer_pane= viewer_kind= error="" notes=
d-t-viewer-found status=done wt= tip=false pane= viewer_pane=%31 viewer_kind=split error="" notes=
d-t-viewer-gone status=done wt= tip=false pane= viewer_pane= viewer_kind= error="" notes=
worktrees=d-c-present,d-r-headless,d-t-dirty,d-t-notify,d-t-uncertain,d-t-unmerged
branches=leo/d-c-present,leo/d-r-headless,leo/d-t-dirty,leo/d-t-notify,leo/d-t-uncertain,leo/d-t-unmerged
spills=dispatch-d-r-adopt.json
runs=d-r-adopt,d-r-nopane,d-t-notify
killed=%21
layouts=@3
`
	if got != want {
		t.Fatalf("restart end state:\n%s\nwant:\n%s", got, want)
	}
}
