package consult

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/tmux"
)

// bgRuntime is movingRuntime that also moves panes to and from the
// background session, tracking where each pane lives so the dispatcher's
// probes see the effect of every move. Panes get unique ids.
type bgRuntime struct {
	*movingRuntime
	locMu  sync.Mutex
	seq    int
	locs   map[string]PaneLocation
	locErr map[string]error
}

const (
	callerSessionID   = "$1"
	callerSessionName = "leo-orch"
	backgroundSession = "$9"
)

func newBgRuntime() *bgRuntime {
	return &bgRuntime{
		movingRuntime: &movingRuntime{fakeInteractiveRuntime: &fakeInteractiveRuntime{arm: true, empty: true}},
		locs:          map[string]PaneLocation{},
		locErr:        map[string]error{},
	}
}

func inCallerWindow(panes int) PaneLocation {
	return PaneLocation{SessionID: callerSessionID, SessionName: callerSessionName, WindowID: "@1", WindowPanes: panes, SessionWindows: 3}
}

func inBackground(window string) PaneLocation {
	return PaneLocation{SessionID: backgroundSession, SessionName: dispatchViewerSession, WindowID: window, WindowPanes: 1, SessionWindows: 4}
}

func (r *bgRuntime) Launch(_ context.Context, req LaunchRequest) (string, string, error) {
	f := r.fakeInteractiveRuntime
	if f.launchHook != nil {
		f.launchHook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	r.locMu.Lock()
	defer r.locMu.Unlock()
	r.seq++
	pane := fmt.Sprintf("%%%d", r.seq)
	f.alive = true
	f.placements = append(f.placements, req.Placement.Kind)
	f.callers = append(f.callers, req.Caller)
	switch {
	case req.Placement.Background:
		r.locs[pane] = inBackground("@" + fmt.Sprint(100+r.seq))
	case req.Placement.Kind == "split":
		r.locs[pane] = inCallerWindow(2)
	default:
		loc := inCallerWindow(1)
		loc.WindowID = "@" + fmt.Sprint(100+r.seq)
		r.locs[pane] = loc
	}
	return pane, "w", nil
}

func (r *bgRuntime) locate(pane string) (PaneLocation, error) {
	r.locMu.Lock()
	defer r.locMu.Unlock()
	if err := r.locErr[pane]; err != nil {
		return PaneLocation{}, err
	}
	if loc, ok := r.locs[pane]; ok {
		return loc, nil
	}
	return inCallerWindow(2), nil // an unlaunched pane: the caller's own
}

func (r *bgRuntime) place(pane string, loc PaneLocation) {
	r.locMu.Lock()
	r.locs[pane] = loc
	r.locMu.Unlock()
}

func (r *bgRuntime) PaneLocation(_ context.Context, pane string) (PaneLocation, error) {
	return r.locate(pane)
}

func (r *bgRuntime) BackgroundPane(_ context.Context, pane, name string) (string, error) {
	r.record("background " + pane + " " + name)
	window := "@2" + strings.TrimPrefix(pane, "%")
	r.place(pane, inBackground(window))
	return window, nil
}

func (r *bgRuntime) ForegroundPane(_ context.Context, pane, name, session string) (string, error) {
	r.record("foreground " + pane + " " + session)
	loc := inCallerWindow(1)
	loc.WindowID = "@3" + strings.TrimPrefix(pane, "%")
	r.place(pane, loc)
	return loc.WindowID, nil
}

func (r *bgRuntime) HidePane(ctx context.Context, pane, name string) (string, error) {
	window, err := r.movingRuntime.HidePane(ctx, pane, name)
	loc := inCallerWindow(1)
	loc.WindowID = window
	r.place(pane, loc)
	return window, err
}

func (r *bgRuntime) ShowPane(ctx context.Context, pane, target, window string) error {
	err := r.movingRuntime.ShowPane(ctx, pane, target, window)
	if err == nil {
		joined, _ := r.locate(target) // it lands in the target pane's window
		joined.WindowPanes = 2
		r.place(pane, joined)
	}
	return err
}

func (r *bgRuntime) record(event string) {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
}

// moves are the log entries that relocate a pane, in order.
func (r *bgRuntime) moves() []string {
	var moves []string
	for _, e := range r.log() {
		if !strings.HasPrefix(e, "inject") {
			moves = append(moves, e)
		}
	}
	return moves
}

func (r *bgRuntime) sessionOf(t *testing.T, pane string) string {
	t.Helper()
	loc, err := r.locate(pane)
	if err != nil {
		t.Fatal(err)
	}
	return loc.SessionName
}

// liveClients is a settable tmux client list that counts how often it is read.
type liveClients struct {
	mu    sync.Mutex
	list  []tmux.SessionClient
	reads int
}

func (c *liveClients) set(clients ...tmux.SessionClient) {
	c.mu.Lock()
	c.list = clients
	c.mu.Unlock()
}

func (c *liveClients) all(context.Context) ([]tmux.SessionClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reads++
	return append([]tmux.SessionClient(nil), c.list...), nil
}

func (c *liveClients) ofSession(_ context.Context, session string) ([]tmux.Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []tmux.Client
	for _, sc := range c.list {
		if sc.SessionID == session {
			out = append(out, sc.Client)
		}
	}
	return out, nil
}

func (c *liveClients) readCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reads
}

func clientOf(session string, pid int) tmux.SessionClient {
	return tmux.SessionClient{Client: clientAt(pid, attachEpoch.Add(time.Second)), SessionID: session, SessionName: callerSessionName}
}

type liveFixture struct {
	t       *testing.T
	d       *Dispatcher
	rt      *bgRuntime
	reg     *AttachPlacements
	clients *liveClients
}

func newLiveFixture(t *testing.T) *liveFixture {
	t.Helper()
	now := attachEpoch
	f := &liveFixture{t: t, d: NewDispatcher(newFakeRecorder()), rt: newBgRuntime(), reg: newTestAttachPlacements(&now), clients: &liveClients{}}
	f.d.SetInteractiveRuntime(f.rt)
	f.d.SetAttachPlacements(f.reg, f.clients.ofSession)
	f.d.SetAllClientsLister(f.clients.all)
	return f
}

// attach makes pid a client of the caller session asking for placement.
func (f *liveFixture) attach(placement string, pid int) tmux.SessionClient {
	f.t.Helper()
	mustRegister(f.t, f.reg, callerSessionID, pid, placement)
	return clientOf(callerSessionID, pid)
}

func (f *liveFixture) request(mod func(*Request)) Request {
	req := Request{
		Template: "codex", Name: "impl", Prompt: "x", Cwd: f.t.TempDir(), Mode: ModeInteractive, Kind: "dispatch",
		CallerPaneID: "%0", CallerSessionID: callerSessionID, CallerWindowID: "@1",
	}
	if mod != nil {
		mod(&req)
	}
	return req
}

func (f *liveFixture) start(mod func(*Request)) Started {
	f.t.Helper()
	started, err := f.d.Start(context.Background(), testConfig(), f.request(mod))
	if err != nil {
		f.t.Fatal(err)
	}
	return started
}

// idle drives a started run's opening turn to idle and waits for its pane to
// be hidden.
func (f *liveFixture) idle(id string) {
	f.t.Helper()
	waitForInjection(f.t, f.rt.fakeInteractiveRuntime)
	_ = f.d.Report(id, hook(f.t, "UserPromptSubmit", "a"))
	_ = f.d.Report(id, hook(f.t, "Stop", "a"))
	waitForHidden(f.t, f.d, id)
}

func (f *liveFixture) poll(n int) {
	for range n {
		f.d.PollPlacement(context.Background())
	}
}

// flush waits for every pane op queued so far on run id, such as the
// reconcile a publish nudges.
func (f *liveFixture) flush(id string) {
	f.t.Helper()
	_, state, err := f.d.lookup(id)
	if err != nil || state == nil {
		f.t.Fatalf("flush %s: %v", id, err)
	}
	f.d.mu.Lock()
	done := f.d.enqueuePaneOpLocked(state, paneOpPublish, func() {})
	f.d.mu.Unlock()
	<-done
}

func (f *liveFixture) kind(id string) string {
	rec, err := f.d.Get(id)
	if err != nil {
		f.t.Fatal(err)
	}
	return rec.ViewerKind
}

func (f *liveFixture) pane(id string) string {
	rec, _ := f.d.Get(id)
	return rec.PaneID
}

func TestPlacementWatchBackgroundsViewersAfterTwoAgreeingPolls(t *testing.T) {
	f := newLiveFixture(t)
	started := f.start(nil)
	f.clients.set(f.attach("background", 10))

	f.poll(1)
	if got := f.rt.moves(); len(got) != 0 {
		t.Fatalf("moved after one poll: %v", got)
	}
	f.poll(1)
	rec, _ := f.d.Get(started.ID)
	if got := f.rt.moves(); len(got) != 1 || got[0] != "background %1 "+viewerWindowName(rec) {
		t.Fatalf("moves = %v, want one background move", got)
	}
	if rec.ViewerKind != "background" || rec.PaneID != started.Pane || rec.ViewerWindowID != "@21" {
		t.Fatalf("record kind=%q pane=%q window=%q, want background in @21 keeping pane %s", rec.ViewerKind, rec.PaneID, rec.ViewerWindowID, started.Pane)
	}
}

func TestPlacementWatchIsEdgeTriggered(t *testing.T) {
	f := newLiveFixture(t)
	f.start(nil)
	f.clients.set(f.attach("background", 10))
	f.poll(2)
	f.poll(5)
	if got := f.rt.moves(); len(got) != 1 {
		t.Fatalf("moves = %v, want the one flip only", got)
	}
}

func TestPlacementWatchIgnoresAFlappingPoll(t *testing.T) {
	f := newLiveFixture(t)
	f.start(nil)
	bg, fg := f.attach("background", 10), f.attach("pane", 11)
	f.clients.set(bg)
	f.poll(1)
	f.clients.set(bg, fg)
	f.poll(1)
	f.clients.set(bg)
	f.poll(1)
	if got := f.rt.moves(); len(got) != 0 {
		t.Fatalf("moves = %v, want none: the background state never held for two polls", got)
	}
}

func TestPlacementWatchNoClientsIsNeverAFlip(t *testing.T) {
	f := newLiveFixture(t)
	started := f.start(nil)
	f.poll(4) // no clients from the start
	f.clients.set(f.attach("background", 10))
	f.poll(2)
	before := f.rt.moves()
	f.clients.set()
	f.poll(5)
	if got := f.rt.moves(); !slices.Equal(got, before) {
		t.Fatalf("moves = %v after the last client left, want %v", got, before)
	}
	if f.kind(started.ID) != "background" {
		t.Fatalf("kind = %q, want the viewer left where it was", f.kind(started.ID))
	}
}

func TestPlacementWatchSkipsTmuxWithoutLiveViewers(t *testing.T) {
	f := newLiveFixture(t)
	f.poll(3)
	if n := f.clients.readCount(); n != 0 {
		t.Fatalf("read the client list %d times with nothing to place", n)
	}
	f.start(nil)
	f.poll(1)
	if n := f.clients.readCount(); n != 1 {
		t.Fatalf("read the client list %d times, want one call per tick with a live viewer", n)
	}
}

func TestPlacementWatchPaneWindowFlipMovesNothing(t *testing.T) {
	f := newLiveFixture(t)
	f.start(nil)
	f.clients.set(f.attach("pane", 10))
	f.poll(2)
	f.clients.set(f.attach("window", 11))
	f.poll(4)
	f.clients.set(f.attach("pane", 12))
	f.poll(4)
	if got := f.rt.moves(); len(got) != 0 {
		t.Fatalf("moves = %v, want none for pane <-> window", got)
	}
}

func TestBackgroundedWorkingPaneComesBackAsASplitWithTheSamePaneId(t *testing.T) {
	f := newLiveFixture(t)
	started := f.start(nil)
	bg := f.attach("background", 10)
	f.clients.set(bg)
	f.poll(2)
	if f.rt.sessionOf(t, started.Pane) != dispatchViewerSession {
		t.Fatalf("pane not backgrounded: %v", f.rt.moves())
	}
	f.clients.set(bg, f.attach("pane", 11))
	f.poll(2)
	moves := f.rt.moves()
	if len(moves) != 2 || moves[1] != "show "+started.Pane+" %0 @1" {
		t.Fatalf("moves = %v, want it rejoined below the caller", moves)
	}
	if f.kind(started.ID) != "split" || f.pane(started.ID) != started.Pane {
		t.Fatalf("kind=%q pane=%q, want split in the same pane %s", f.kind(started.ID), f.pane(started.ID), started.Pane)
	}
}

func TestBackgroundedIdlePaneComesBackHiddenAndSendStillRejoinsIt(t *testing.T) {
	f := newLiveFixture(t)
	started := f.start(nil)
	f.idle(started.ID)
	bg := f.attach("background", 10)
	f.clients.set(bg)
	f.poll(2)
	if f.kind(started.ID) != "background" {
		t.Fatalf("kind = %q, want the idle hidden pane backgrounded", f.kind(started.ID))
	}

	f.clients.set(f.attach("pane", 11))
	f.poll(2)
	rec, _ := f.d.Get(started.ID)
	if rec.ViewerKind != "hidden" || rec.PaneID != started.Pane || rec.ViewerWindowID != "@3"+strings.TrimPrefix(started.Pane, "%") {
		t.Fatalf("record kind=%q pane=%q window=%q, want hidden in a caller-session window", rec.ViewerKind, rec.PaneID, rec.ViewerWindowID)
	}
	if _, err := f.d.Send(context.Background(), started.ID, "next"); err != nil {
		t.Fatal(err)
	}
	events := f.rt.log()
	if tail := events[len(events)-2:]; !slices.Equal(tail, []string{"show " + started.Pane + " %0 @1", "inject " + started.Pane}) {
		t.Fatalf("events = %v, want the usual rejoin then inject into the same pane", events)
	}
}

func TestSendWhileBackgroundedInjectsTheSamePaneAndDoesNotRejoinIt(t *testing.T) {
	f := newLiveFixture(t)
	started := f.start(nil)
	f.idle(started.ID)
	f.clients.set(f.attach("background", 10))
	f.poll(2)
	hiddenOnce := f.rt.count("hide")

	if _, err := f.d.Send(context.Background(), started.ID, "next"); err != nil {
		t.Fatal(err)
	}
	_ = f.d.Report(started.ID, hook(t, "UserPromptSubmit", "b"))
	_ = f.d.Report(started.ID, hook(t, "Stop", "b"))
	events := f.rt.log()
	if f.rt.count("show") != 0 || f.rt.count("hide") != hiddenOnce || f.rt.count("foreground") != 0 {
		t.Fatalf("events = %v, a backgrounded pane must neither rejoin on a turn nor be hidden again", events)
	}
	if f.rt.count("inject "+started.Pane) != 2 {
		t.Fatalf("events = %v, want the follow-up injected into %s", events, started.Pane)
	}
	if f.kind(started.ID) != "background" {
		t.Fatalf("kind = %q", f.kind(started.ID))
	}
}

func TestWindowViewerRoundTripsThroughBackground(t *testing.T) {
	f := newLiveFixture(t)
	started := f.start(func(r *Request) { r.CallerPaneID = "" }) // no caller pane: a window of its own
	if f.kind(started.ID) != "window" {
		t.Fatalf("kind = %q, want window", f.kind(started.ID))
	}
	bg := f.attach("background", 10)
	f.clients.set(bg)
	f.poll(2)
	if f.kind(started.ID) != "background" {
		t.Fatalf("kind = %q after the flip", f.kind(started.ID))
	}
	f.clients.set(bg, f.attach("pane", 11))
	f.poll(2)
	if f.kind(started.ID) != "window" || f.rt.count("foreground "+started.Pane+" "+callerSessionID) != 1 {
		t.Fatalf("kind=%q moves=%v, want it back as a window of the caller session", f.kind(started.ID), f.rt.moves())
	}
	if f.pane(started.ID) != started.Pane {
		t.Fatalf("pane id changed: %q", f.pane(started.ID))
	}
}

func TestLaunchedBackgroundViewerReturnsWhenAPaneClientAttaches(t *testing.T) {
	f := newLiveFixture(t)
	bg := f.attach("background", 10)
	f.clients.set(bg)
	started := f.start(nil)
	if f.kind(started.ID) != "background" || f.rt.sessionOf(t, started.Pane) != dispatchViewerSession {
		t.Fatalf("kind = %q, want a viewer launched into the background recorded as background", f.kind(started.ID))
	}
	f.poll(3)
	if len(f.rt.moves()) != 0 {
		t.Fatalf("moves = %v, want none while still background", f.rt.moves())
	}
	f.clients.set(bg, f.attach("pane", 11))
	f.poll(2)
	if k := f.kind(started.ID); k != "split" {
		t.Fatalf("kind = %q, want split after a pane client attached", k)
	}
}

func TestPlacementWatchNeverTouchesAPinnedViewer(t *testing.T) {
	for name, locErr := range map[string]error{"another session": nil, "linked into two sessions": ErrPaneAmbiguous} {
		t.Run(name, func(t *testing.T) {
			f := newLiveFixture(t)
			started := f.start(nil)
			if locErr != nil {
				f.rt.locErr[started.Pane] = locErr
			} else {
				f.rt.place(started.Pane, PaneLocation{SessionID: "$7", SessionName: "elsewhere", WindowID: "@70", WindowPanes: 1, SessionWindows: 2})
			}
			bg := f.attach("background", 10)
			f.clients.set(bg)
			f.poll(3)
			f.clients.set(bg, f.attach("pane", 11))
			f.poll(3)
			f.clients.set(f.attach("background", 12))
			f.poll(3)
			if got := f.rt.moves(); len(got) != 0 {
				t.Fatalf("moves = %v, want a viewer the user parked elsewhere left alone", got)
			}
			if k := f.kind(started.ID); k != "split" {
				t.Fatalf("kind = %q, want the record untouched", k)
			}
		})
	}
}

func TestPlacementWatchMovesNestedViewersParentFirst(t *testing.T) {
	f := newLiveFixture(t)
	parent := f.start(nil)
	child := f.start(func(r *Request) {
		r.ParentDispatchID, r.Name = parent.ID, "sub"
		r.CallerPaneID, r.CallerSessionID = parent.Pane, "$7" // the subagent reports its own session
	})
	bg := f.attach("background", 10)
	f.clients.set(bg)
	f.poll(2)
	moves := f.rt.moves()
	if len(moves) != 2 || !strings.HasPrefix(moves[0], "background "+parent.Pane) || !strings.HasPrefix(moves[1], "background "+child.Pane) {
		t.Fatalf("moves = %v, want parent then child", moves)
	}

	f.clients.set(bg, f.attach("pane", 11))
	f.poll(2)
	moves = f.rt.moves()[2:]
	if len(moves) != 2 || !strings.Contains(moves[0], parent.Pane) || !strings.Contains(moves[1], child.Pane) {
		t.Fatalf("return moves = %v, want parent then child", moves)
	}
}

func TestLaunchRaceKeepsTheFlipThatLandedWhileResolving(t *testing.T) {
	f := newLiveFixture(t)
	first := f.start(nil)
	bg := f.attach("background", 10)
	// While the second launch is still reading the client list, a flip to
	// background commits. The launch read the pre-flip list, so it must not
	// overwrite the committed state with it.
	stale := f.clients.ofSession
	f.d.SetAttachPlacements(f.reg, func(ctx context.Context, session string) ([]tmux.Client, error) {
		clients, err := stale(ctx, session)
		f.clients.set(bg)
		f.poll(2)
		return clients, err
	})
	second := f.start(nil)
	f.flush(second.ID)
	if k := f.kind(second.ID); k != "background" {
		t.Fatalf("second viewer kind = %q, want it to follow the flip that landed mid-launch", k)
	}
	if k := f.kind(first.ID); k != "background" {
		t.Fatalf("first viewer kind = %q", k)
	}
}

func TestLaunchRaceIsCorrectedWhenTheFlipLandsAfterTheDecision(t *testing.T) {
	f := newLiveFixture(t)
	first := f.start(nil)
	f.rt.launchHook = func() {
		f.clients.set(f.attach("background", 10))
		f.poll(2)
	}
	second := f.start(nil)
	f.flush(second.ID)
	f.rt.launchHook = nil
	// The pane launched as a split (decided before the flip) and is moved as
	// soon as it is published.
	if k := f.kind(second.ID); k != "background" {
		t.Fatalf("second viewer kind = %q, want background (moves %v)", k, f.rt.moves())
	}
	if f.rt.count("background "+second.Pane) != 1 || f.rt.count("background "+first.Pane) != 1 {
		t.Fatalf("moves = %v, want each viewer backgrounded once", f.rt.moves())
	}
}

func TestLiveViewersSurviveConcurrentStartsAndFlips(t *testing.T) {
	f := newLiveFixture(t)
	const runs = 3
	bg, fg := f.attach("background", 10), f.attach("pane", 11)

	var wg sync.WaitGroup
	started := make([]Started, runs)
	for i := range runs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := f.d.Start(context.Background(), testConfig(), f.request(func(r *Request) { r.Name = fmt.Sprintf("run%d", i) }))
			if err != nil {
				t.Error(err)
			}
			started[i] = s
		}()
	}
	for i := range 6 {
		if i%2 == 0 {
			f.clients.set(bg)
		} else {
			f.clients.set(bg, fg)
		}
		f.poll(2)
	}
	wg.Wait()
	for _, s := range started {
		f.flush(s.ID)
	}

	f.clients.set(bg)
	f.poll(3)
	for _, s := range started {
		if f.kind(s.ID) != "background" || f.rt.sessionOf(t, s.Pane) != dispatchViewerSession {
			t.Fatalf("run %s kind=%q session=%q after settling on background", s.ID, f.kind(s.ID), f.rt.sessionOf(t, s.Pane))
		}
	}
	f.clients.set(bg, fg)
	f.poll(3)
	for _, s := range started {
		if f.kind(s.ID) == "background" || f.pane(s.ID) != s.Pane {
			t.Fatalf("run %s kind=%q pane=%q after a pane client attached; want visible in pane %s", s.ID, f.kind(s.ID), f.pane(s.ID), s.Pane)
		}
	}
}

func TestAttachableIncludesBackgroundViewers(t *testing.T) {
	rec := Record{Mode: ModeInteractive, PaneID: "%5", Status: StatusIdle}
	for kind, want := range map[string]bool{"window": true, "background": true, "split": false, "hidden": false} {
		rec.ViewerKind = kind
		if got := Attachable(rec); got != want {
			t.Errorf("Attachable(%s) = %v, want %v", kind, got, want)
		}
	}
}

func TestBackgroundedViewerComesBackAsAWindowWhenItsCallerPaneIsGone(t *testing.T) {
	f := newLiveFixture(t)
	started := f.start(nil)
	bg := f.attach("background", 10)
	f.clients.set(bg)
	f.poll(2)
	f.rt.locErr["%0"] = fmt.Errorf("locate pane %%0: %w", ErrPaneNotFound)

	f.clients.set(bg, f.attach("pane", 11))
	f.poll(2)
	if f.kind(started.ID) != "window" || f.rt.count("show") != 0 || f.rt.count("foreground "+started.Pane+" "+callerSessionID) != 1 {
		t.Fatalf("kind=%q moves=%v, want a window of the caller session, not a rejoin", f.kind(started.ID), f.rt.moves())
	}
}

func TestBackgroundedViewerComesBackAsAWindowWhenTheCallerWindowIsFull(t *testing.T) {
	f := newLiveFixture(t)
	var runs []Started
	for i := range 4 { // the default cap is three splits per caller window
		runs = append(runs, f.start(func(r *Request) { r.Name = fmt.Sprintf("run%d", i) }))
	}
	if f.kind(runs[3].ID) != "window" {
		t.Fatalf("fourth viewer kind = %q, want the cap to have made it a window", f.kind(runs[3].ID))
	}
	bg := f.attach("background", 10)
	f.clients.set(bg)
	f.poll(2)
	f.clients.set(bg, f.attach("pane", 11))
	f.poll(2)
	splits := 0
	for _, r := range runs {
		if f.kind(r.ID) == "split" {
			splits++
		}
	}
	if splits != 3 {
		t.Fatalf("%d viewers came back as splits (moves %v), want the cap of 3 kept", splits, f.rt.moves())
	}
}

func TestFailedBackgroundMoveLeavesTheRecordAndPaneAlone(t *testing.T) {
	f := newLiveFixture(t)
	started := f.start(nil)
	f.rt.locErr[started.Pane] = fmt.Errorf("locate pane: tmux wedged")
	f.clients.set(f.attach("background", 10))
	f.poll(2)
	if f.kind(started.ID) != "split" || len(f.rt.moves()) != 0 {
		t.Fatalf("kind=%q moves=%v, want nothing moved when the pane cannot be located", f.kind(started.ID), f.rt.moves())
	}
	// The next flip to background tries again once tmux answers.
	delete(f.rt.locErr, started.Pane)
	bg := f.attach("background", 10)
	f.clients.set(bg, f.attach("pane", 11))
	f.poll(2)
	f.clients.set(bg)
	f.poll(2)
	if f.kind(started.ID) != "background" {
		t.Fatalf("kind=%q moves=%v, want the retry to have moved it", f.kind(started.ID), f.rt.moves())
	}
}

func TestCancelingABackgroundedViewerKillsItsPane(t *testing.T) {
	f := newLiveFixture(t)
	started := f.start(nil)
	f.clients.set(f.attach("background", 10))
	f.poll(2)
	if _, err := f.d.Cancel(started.ID); err != nil {
		t.Fatal(err)
	}
	if got := f.rt.killCount(); got != 1 {
		t.Fatalf("kills = %d, want the backgrounded pane killed once", got)
	}
}

func TestUserTurnInABackgroundedPaneDoesNotSendItBackAfterItReturns(t *testing.T) {
	f := newLiveFixture(t)
	started := f.start(nil)
	f.idle(started.ID)
	bg := f.attach("background", 10)
	f.clients.set(bg)
	f.poll(2)
	// Someone attaches to the backgrounded pane and types into it.
	_ = f.d.Report(started.ID, hook(t, "UserPromptSubmit", "u"))
	f.clients.set(bg, f.attach("pane", 11))
	f.poll(2)
	if f.kind(started.ID) == "background" {
		t.Fatalf("kind = %q, want the viewer back once a pane client attached", f.kind(started.ID))
	}
	before := f.rt.moves()
	_ = f.d.Report(started.ID, hook(t, "Stop", "u"))
	f.flush(started.ID)
	if got := f.rt.moves(); !slices.Equal(got, before) || f.kind(started.ID) == "background" {
		t.Fatalf("moves = %v (kind %q) after the user's turn ended, want %v and still visible", got, f.kind(started.ID), before)
	}
}

func TestRejoinedNestedViewersRecordTheCallersLiveWindow(t *testing.T) {
	f := newLiveFixture(t)
	parent := f.start(nil)
	child := f.start(func(r *Request) {
		r.ParentDispatchID, r.Name = parent.ID, "sub"
		r.CallerPaneID, r.CallerSessionID, r.CallerWindowID = parent.Pane, "$7", "@1" // the subagent's own coordinates
	})
	bg := f.attach("background", 10)
	f.clients.set(bg)
	f.poll(2)
	// While they were away the root agent's pane moved to another window.
	f.rt.place("%0", PaneLocation{SessionID: callerSessionID, SessionName: callerSessionName, WindowID: "@5", WindowPanes: 1, SessionWindows: 3})

	f.clients.set(bg, f.attach("pane", 11))
	f.poll(2)
	for name, id := range map[string]string{"parent": parent.ID, "child": child.ID} {
		rec, _ := f.d.Get(id)
		if rec.ViewerKind != "split" || rec.CallerSessionID != callerSessionID || rec.CallerWindowID != "@5" {
			t.Fatalf("%s record kind=%q caller session=%q window=%q, want a split recorded against %s/@5", name, rec.ViewerKind, rec.CallerSessionID, rec.CallerWindowID, callerSessionID)
		}
	}
	if n := LiveViewerPaneCount(f.d.Records(), callerSessionID, "@5"); n != 2 {
		t.Fatalf("live splits counted in the caller's window = %d, want both viewers", n)
	}
}

func (f *liveFixture) pinned(id string) bool {
	f.t.Helper()
	_, state, err := f.d.lookup(id)
	if err != nil || state == nil {
		f.t.Fatalf("pinned %s: %v", id, err)
	}
	f.d.mu.Lock()
	defer f.d.mu.Unlock()
	return state.pinned
}

var elsewhere = PaneLocation{SessionID: "$7", SessionName: "elsewhere", WindowID: "@70", WindowPanes: 1, SessionWindows: 2}

func TestShowPaneDoesNotJoinAViewerTheUserMovedToAnotherSession(t *testing.T) {
	f := newLiveFixture(t)
	started := f.start(nil)
	f.idle(started.ID)
	f.rt.place(started.Pane, elsewhere)

	if _, err := f.d.Send(context.Background(), started.ID, "next"); err != nil {
		t.Fatal(err)
	}
	if f.rt.count("show") != 0 || f.rt.count("inject "+started.Pane) != 2 {
		t.Fatalf("events = %v, want the follow-up injected without joining the pane", f.rt.log())
	}
	if !f.pinned(started.ID) {
		t.Fatal("a viewer parked in another session must be pinned")
	}
}

func TestOrchestratorStopDoesNotHideAManuallyRelocatedSplit(t *testing.T) {
	f := newLiveFixture(t)
	started := f.start(nil)
	waitForInjection(t, f.rt.fakeInteractiveRuntime)
	_ = f.d.Report(started.ID, hook(t, "UserPromptSubmit", "a"))
	f.rt.place(started.Pane, elsewhere)
	_ = f.d.Report(started.ID, hook(t, "Stop", "a"))
	f.flush(started.ID)

	if f.rt.count("hide") != 0 || f.kind(started.ID) != "split" {
		t.Fatalf("events = %v, kind = %q, want the relocated split left alone", f.rt.log(), f.kind(started.ID))
	}
	if !f.pinned(started.ID) {
		t.Fatal("a split moved to another session must be pinned")
	}
}
