package consult

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"syscall"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

const (
	// attachClockSlack covers tmux reporting client_created in whole seconds:
	// a client that attached just after a registration can read as created
	// up to a second before it.
	attachClockSlack = time.Second
	// maxAttachRegistrations bounds the registry: past it the oldest
	// registration is evicted.
	maxAttachRegistrations = 256
)

type attachEntry struct {
	session      string
	placement    string
	registeredAt time.Time
}

// AttachPlacements remembers which dispatch viewer placement each `leo attach
// --dispatch-placement` asked for. An entry is keyed by the attaching
// process's pid, which exec hands on to the tmux client. It only counts while
// a live client in the session being resolved has that pid and attached no
// earlier than the registration, so a reused pid never inherits it.
type AttachPlacements struct {
	mu       sync.Mutex
	now      func() time.Time
	pidAlive func(pid int) bool
	entries  map[int]attachEntry
}

// NewAttachPlacements returns an empty registry reading time from now and
// asking pidAlive whether a registered process still exists.
func NewAttachPlacements(now func() time.Time, pidAlive func(int) bool) *AttachPlacements {
	return &AttachPlacements{now: now, pidAlive: pidAlive, entries: map[int]attachEntry{}}
}

// ProcessAlive reports whether pid names an existing process.
func ProcessAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// Register records that the process pid, about to become a tmux client of
// session, wants dispatch viewers placed as placement. session is the tmux session
// name it attaches to; it is kept for diagnosis, since matching goes through
// the live client list of the session being resolved.
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
	a.pruneDeadLocked()
	if _, replacing := a.entries[pid]; !replacing && len(a.entries) >= maxAttachRegistrations {
		a.evictOldestLocked()
	}
	a.entries[pid] = attachEntry{session: session, placement: placement, registeredAt: a.now()}
	return nil
}

func (a *AttachPlacements) pruneDeadLocked() {
	for pid := range a.entries {
		if !a.pidAlive(pid) {
			delete(a.entries, pid)
		}
	}
}

func (a *AttachPlacements) evictOldestLocked() {
	oldest, found := 0, false
	for pid, e := range a.entries {
		if !found || e.registeredAt.Before(a.entries[oldest].registeredAt) {
			oldest, found = pid, true
		}
	}
	if found {
		delete(a.entries, oldest)
	}
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
// unchanged. Registrations whose process is gone are dropped.
func (a *AttachPlacements) Resolve(ctx context.Context, session string, base ViewerOverrides, cfg *config.Config, listClients func(context.Context, string) ([]tmux.Client, error)) ViewerOverrides {
	return a.ResolveWithFallback(ctx, session, base, func() ViewerOverrides { return base }, cfg, listClients)
}

// ResolveWithFallback is Resolve where an unregistered client contributes the
// placement of fallback() (read only once clients exist) instead of base's:
// the session overrides of the session whose clients are listed.
func (a *AttachPlacements) ResolveWithFallback(ctx context.Context, session string, base ViewerOverrides, fallback func() ViewerOverrides, cfg *config.Config, listClients func(context.Context, string) ([]tmux.Client, error)) ViewerOverrides {
	if a == nil || session == "" {
		return base
	}
	// A client may have attached, and registered, after tmux answered; the
	// listing says nothing about registrations made after this instant.
	listedAt := a.now()
	clients, err := listClients(ctx, session)
	a.mu.Lock()
	a.pruneDeadLocked()
	a.mu.Unlock()
	if err != nil || len(clients) == 0 {
		return base
	}
	defaultPlacement := cfg.DispatchViewerPlacement()
	if o := fallback(); config.IsDispatchViewerPlacement(o.Placement) {
		defaultPlacement = o.Placement
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	best := ""
	for _, c := range clients {
		placement := defaultPlacement
		if e, ok := a.entries[c.PID]; ok && c.Created.Add(attachClockSlack).Before(e.registeredAt) {
			// The listed process predates the registration. If the
			// registration came first, a different process now owns the pid;
			// if it came after the listing began, this is the previous owner
			// and the registration belongs to a client the listing missed.
			if !e.registeredAt.After(listedAt) {
				delete(a.entries, c.PID)
			}
		} else if ok {
			placement = e.placement
		}
		if visibility(placement) > visibility(best) {
			best = placement
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
//
// base holds the session overrides of rec's own caller session. An unflagged
// client of the root caller's session contributes that session's overrides, so
// for a nested dispatch (whose root session differs) they are read with
// readOverrides; nil falls back to base.
func (d *Dispatcher) ApplyAttachPlacement(ctx context.Context, rec Record, base ViewerOverrides, cfg *config.Config, readOverrides func(context.Context, string) ViewerOverrides) ViewerOverrides {
	d.mu.Lock()
	reg, list := d.attachPlacements, d.listClients
	d.mu.Unlock()
	if reg == nil || list == nil {
		return base
	}
	root := d.rootCallerSession(rec)
	fallback := func() ViewerOverrides {
		if root == rec.CallerSessionID || readOverrides == nil {
			return base
		}
		return readOverrides(ctx, root)
	}
	return reg.ResolveWithFallback(ctx, root, base, fallback, cfg, list)
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
