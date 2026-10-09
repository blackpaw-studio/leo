package consult

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

// placementDebouncePolls is how many polls in a row must agree on a new
// background/visible state before it counts as a flip.
const placementDebouncePolls = 2

// rootPlacement is the live placement watch's state for one root caller
// session.
type rootPlacement struct {
	// gen counts committed flips, so a launch that resolved its placement
	// across one can tell (see settleLaunchPlacementLocked).
	gen uint64
	// background is the state the polls are converging on and polls how many
	// in a row have seen it.
	background bool
	polls      int
}

func isBackgroundPlacement(placement string) bool { return placement == "background" }

// SetAllClientsLister enables the live placement watch: PollPlacement reads
// every session's tmux clients through list.
func (d *Dispatcher) SetAllClientsLister(list func(context.Context) ([]tmux.SessionClient, error)) {
	d.mu.Lock()
	d.listAllClients = list
	d.mu.Unlock()
}

// placementEligibleLocked reports whether s is a run whose viewer the watch
// steers: an interactive run still going.
func placementEligibleLocked(s *runState) bool {
	return s.record.Mode == ModeInteractive && !s.record.Status.Terminal() && s.record.Status != StatusSettling &&
		!s.killRequested && !s.releasing
}

// liveTreesLocked groups the eligible runs by the root caller session their
// dispatch tree answers to. Runs with no caller session are left out: nobody
// attached to a session decides where they open.
func (d *Dispatcher) liveTreesLocked() map[string][]*runState {
	trees := map[string][]*runState{}
	for _, s := range d.runs {
		if !placementEligibleLocked(s) {
			continue
		}
		if root := d.rootCallerSessionLocked(s.record); root != "" {
			trees[root] = append(trees[root], s)
		}
	}
	return trees
}

func (d *Dispatcher) placementGenLocked(root string) uint64 {
	if rp := d.rootPlacements[root]; rp != nil {
		return rp.gen
	}
	return 0
}

// PollPlacement is one tick of the live placement watch: it lists every tmux
// client in one call and, for each root caller session with live dispatch
// viewers, works out the placement its clients ask for between them. When that
// flips between background and visible (and holds for placementDebouncePolls
// polls) the session's viewers are moved to match. A session with no clients
// is not a flip: viewers stay where they are. pane <-> window never moves
// anything. It does no tmux work at all while no viewer is live, and ticks
// never overlap.
func (d *Dispatcher) PollPlacement(ctx context.Context) {
	d.watchMu.Lock()
	defer d.watchMu.Unlock()
	d.mu.Lock()
	reg, listAll, rt, cfg := d.attachPlacements, d.listAllClients, d.interactiveRuntime, d.placementCfg
	trees := d.liveTreesLocked()
	viewers := 0
	for _, tree := range trees {
		for _, s := range tree {
			if viewerSteerableLocked(s) {
				viewers++
			}
		}
	}
	for root := range d.rootPlacements {
		if _, live := trees[root]; !live {
			delete(d.rootPlacements, root)
		}
	}
	d.mu.Unlock()
	if reg == nil || listAll == nil || cfg == nil || viewers == 0 {
		return
	}
	listedAt := reg.now()
	clients, err := listAll(ctx)
	if err != nil {
		return
	}
	roots := make([]string, 0, len(trees))
	for root := range trees {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	for _, root := range roots {
		effective := reg.Effective(clientsOfSession(clients, root), listedAt, func() string {
			if provider, ok := rt.(viewerOverridesRuntime); ok {
				if o := provider.ViewerOverrides(ctx, root); config.IsDispatchViewerPlacement(o.Placement) {
					return o.Placement
				}
			}
			return cfg.DispatchViewerPlacement()
		})
		d.observePlacement(root, effective)
	}
}

// viewerSteerableLocked reports whether s has a published viewer pane the
// watch may still move.
func viewerSteerableLocked(s *runState) bool {
	return s.record.PaneID != "" && !s.pinned
}

func clientsOfSession(all []tmux.SessionClient, session string) []tmux.Client {
	var out []tmux.Client
	for _, c := range all {
		if c.SessionID == session || c.SessionName == session {
			out = append(out, c.Client)
		}
	}
	return out
}

// observePlacement folds one poll's effective placement for root into the
// debounce, and commits a flip once it has held.
func (d *Dispatcher) observePlacement(root, effective string) {
	d.mu.Lock()
	rp := d.rootPlacements[root]
	if rp == nil {
		if d.rootPlacements == nil {
			d.rootPlacements = map[string]*rootPlacement{}
		}
		rp = &rootPlacement{}
		d.rootPlacements[root] = rp
	}
	background := isBackgroundPlacement(effective)
	if effective == "" || !d.placementMismatchLocked(root, background) {
		rp.polls = 0
		d.mu.Unlock()
		return
	}
	if rp.polls == 0 || rp.background != background {
		rp.background, rp.polls = background, 0
	}
	rp.polls++
	if rp.polls < placementDebouncePolls {
		d.mu.Unlock()
		return
	}
	rp.polls = 0
	rp.gen++
	steered := d.steerTreeLocked(root, effective)
	d.mu.Unlock()
	// Parents first: a nested viewer that returns below its parent needs the
	// parent back in the caller's window before it.
	for _, s := range steered {
		d.mu.Lock()
		done := d.nudgePaneLocked(s)
		d.mu.Unlock()
		d.awaitPaneOp(done)
	}
}

// placementMismatchLocked reports whether some viewer in root's tree is not
// where a background (or visible) session wants it.
func (d *Dispatcher) placementMismatchLocked(root string, background bool) bool {
	for _, s := range d.liveTreesLocked()[root] {
		if viewerSteerableLocked(s) && isBackgroundPlacement(s.wantPlacement) != background {
			return true
		}
	}
	return false
}

// steerTreeLocked points every run of root's tree (queued and launching ones
// included, so they open the right way) at placement, and returns the
// published viewers in tree-depth order.
func (d *Dispatcher) steerTreeLocked(root, placement string) []*runState {
	type steered struct {
		s     *runState
		depth int
	}
	var viewers []steered
	for _, s := range d.liveTreesLocked()[root] {
		if s.pinned {
			continue
		}
		s.wantPlacement = placement
		if s.record.PaneID != "" {
			viewers = append(viewers, steered{s, d.dispatchDepthLocked(s.record)})
		}
	}
	sort.Slice(viewers, func(i, j int) bool {
		if viewers[i].depth != viewers[j].depth {
			return viewers[i].depth < viewers[j].depth
		}
		return viewers[i].s.record.ID < viewers[j].s.record.ID
	})
	out := make([]*runState, len(viewers))
	for i, v := range viewers {
		out[i] = v.s
	}
	return out
}

// dispatchDepthLocked is how many parents rec has among the runs in memory.
func (d *Dispatcher) dispatchDepthLocked(rec Record) int {
	depth := 0
	seen := map[string]bool{rec.ID: true}
	for rec.ParentDispatchID != "" && !seen[rec.ParentDispatchID] && depth < maxDispatchAncestry {
		parent := d.runs[rec.ParentDispatchID]
		if parent == nil {
			break
		}
		seen[parent.record.ID] = true
		rec = parent.record
		depth++
	}
	return depth
}

// settleLaunchPlacementLocked finishes the placement a launching run resolved
// from a client listing that began at generation gen0. If a flip committed
// meanwhile, the listing may predate it, and the flip has already pointed the
// run at its placement (steerTreeLocked): that wins. Otherwise the resolved
// placement stands and becomes the run's.
func (d *Dispatcher) settleLaunchPlacementLocked(s *runState, root string, gen0 uint64, cfg *config.Config, resolved ViewerOverrides) ViewerOverrides {
	if root != "" && d.placementGenLocked(root) != gen0 && config.IsDispatchViewerPlacement(s.wantPlacement) {
		resolved.Placement = s.wantPlacement
		return resolved
	}
	s.wantPlacement = resolved.Placement
	if !config.IsDispatchViewerPlacement(s.wantPlacement) {
		s.wantPlacement = cfg.DispatchViewerPlacement()
	}
	return resolved
}

// pinViewer stops the placement watch from ever moving s's viewer again: the
// user put it somewhere other than its caller's session or leo-dispatch.
func (d *Dispatcher) pinViewer(s *runState, pane, why string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if s.record.PaneID != pane || s.pinned {
		return
	}
	s.pinned = true
	fmt.Fprintf(os.Stderr, "dispatch %s: leaving viewer pane %s where it is: %s\n", s.record.ID, pane, why)
}
