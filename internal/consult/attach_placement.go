package consult

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

const (
	// attachClockSlack covers tmux reporting client_created in whole seconds:
	// a client that attached just after a registration can read as created
	// up to a second before it.
	attachClockSlack = time.Second
	// attachPruneGrace keeps an entry whose client has not shown up yet: leo
	// registers just before it execs tmux, so a dispatch resolving in between
	// must not discard it.
	attachPruneGrace = time.Minute
)

type attachKey struct {
	session string
	pid     int
}

type attachEntry struct {
	placement    string
	registeredAt time.Time
}

// AttachPlacements remembers which dispatch viewer placement each `leo attach
// --dispatch-placement` asked for. An entry is keyed by the attaching
// process's pid, which exec hands on to the tmux client, and only counts
// while a live client of that session still has the pid and attached no
// earlier than the registration (a reused pid fails that check).
type AttachPlacements struct {
	mu      sync.Mutex
	now     func() time.Time
	entries map[attachKey]attachEntry
}

// NewAttachPlacements returns an empty registry reading time from now.
func NewAttachPlacements(now func() time.Time) *AttachPlacements {
	return &AttachPlacements{now: now, entries: map[attachKey]attachEntry{}}
}

// Register records that the process pid, about to become a tmux client of
// session, wants dispatch viewers placed as placement.
func (a *AttachPlacements) Register(session string, pid int, placement string) error {
	if session == "" {
		return fmt.Errorf("session is required")
	}
	if pid <= 0 {
		return fmt.Errorf("pid must be positive")
	}
	if !config.IsDispatchViewerPlacement(placement) {
		return fmt.Errorf("placement must be pane, window or background, got %q", placement)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries[attachKey{session, pid}] = attachEntry{placement: placement, registeredAt: a.now()}
	return nil
}

// Len reports how many registrations are held.
func (a *AttachPlacements) Len() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.entries)
}

// Resolve returns base with Placement replaced by the most visible placement
// among session's live tmux clients (pane > window > background). A client
// without a live registration contributes base's placement, else the config
// default. With no clients, or when they cannot be listed, base is returned
// unchanged. Registrations that no live client matches and that are old
// enough are dropped.
func (a *AttachPlacements) Resolve(ctx context.Context, session string, base ViewerOverrides, cfg *config.Config, listClients func(context.Context, string) ([]tmux.Client, error)) ViewerOverrides {
	if a == nil || session == "" {
		return base
	}
	clients, err := listClients(ctx, session)
	if err != nil || len(clients) == 0 {
		return base
	}
	fallback := cfg.DispatchViewerPlacement()
	if config.IsDispatchViewerPlacement(base.Placement) {
		fallback = base.Placement
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	matched := map[attachKey]bool{}
	best := ""
	for _, c := range clients {
		placement := fallback
		key := attachKey{session, c.PID}
		if e, ok := a.entries[key]; ok && !c.Created.Add(attachClockSlack).Before(e.registeredAt) {
			matched[key] = true
			placement = e.placement
		}
		if visibility(placement) > visibility(best) {
			best = placement
		}
	}
	for key, e := range a.entries {
		if key.session == session && !matched[key] && now.Sub(e.registeredAt) > attachPruneGrace {
			delete(a.entries, key)
		}
	}
	base.Placement = best
	return base
}

// visibility ranks placements, most visible first; unknown ranks lowest.
func visibility(placement string) int {
	switch placement {
	case "pane":
		return 3
	case "window":
		return 2
	case "background":
		return 1
	}
	return 0
}

// maxDispatchAncestry bounds the walk up a nested dispatch's parents.
const maxDispatchAncestry = 32

// SetAttachPlacements enables per-attach placement: placements for viewers
// are decided by the clients attached to the root caller's session, as
// listed by listClients.
func (d *Dispatcher) SetAttachPlacements(reg *AttachPlacements, listClients func(context.Context, string) ([]tmux.Client, error)) {
	d.mu.Lock()
	d.attachPlacements, d.listClients = reg, listClients
	d.mu.Unlock()
}

// ApplyAttachPlacement returns base with the placement the root caller's
// attached clients ask for. Without a registry it returns base unchanged.
func (d *Dispatcher) ApplyAttachPlacement(ctx context.Context, rec Record, base ViewerOverrides, cfg *config.Config) ViewerOverrides {
	d.mu.Lock()
	reg, list := d.attachPlacements, d.listClients
	d.mu.Unlock()
	if reg == nil || list == nil {
		return base
	}
	return reg.Resolve(ctx, d.rootCallerSession(rec), base, cfg, list)
}

// rootCallerSession is the tmux session of the agent at the top of rec's
// dispatch tree. A nested dispatch is requested from a subagent whose own
// session says nothing about who is watching; the root agent's does.
func (d *Dispatcher) rootCallerSession(rec Record) string {
	seen := map[string]bool{rec.ID: true}
	for i := 0; i < maxDispatchAncestry && rec.ParentDispatchID != "" && !seen[rec.ParentDispatchID]; i++ {
		parent, _, err := d.lookup(rec.ParentDispatchID)
		if err != nil {
			break
		}
		seen[parent.ID] = true
		rec = parent
	}
	return rec.CallerSessionID
}
