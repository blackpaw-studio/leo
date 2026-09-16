package consult

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

type ViewerPlacementCoordinator struct {
	mu       sync.Mutex
	reserved map[string]Record
}

func NewViewerPlacementCoordinator() *ViewerPlacementCoordinator {
	return &ViewerPlacementCoordinator{reserved: map[string]Record{}}
}

func (d *Dispatcher) PlacementCoordinator() *ViewerPlacementCoordinator { return d.placement }
func (c *ViewerPlacementCoordinator) Decide(rec Record, overrides ViewerOverrides, cfg *config.Config, records func() []Record) ViewerPlacement {
	c.mu.Lock()
	defer c.mu.Unlock()
	live := 0
	var published []Record
	if records != nil {
		published = records()
		live = LiveViewerPaneCount(published, rec.CallerSessionID, rec.CallerWindowID)
	}
	for id, r := range c.reserved {
		if r.CallerSessionID == rec.CallerSessionID && r.CallerWindowID == rec.CallerWindowID {
			panePublished := false
			for _, existing := range published {
				if existing.ID == id && (existing.PaneID != "" || existing.ViewerPaneID != "") {
					panePublished = true
					break
				}
			}
			if panePublished {
				continue
			}
			live++
		}
	}
	p := ResolveViewerPlacement(rec, overrides, cfg, live)
	if p.Kind == "split" {
		c.reserved[rec.ID] = rec
	}
	return p
}
func (c *ViewerPlacementCoordinator) Cancel(id string) {
	c.mu.Lock()
	delete(c.reserved, id)
	c.mu.Unlock()
}

// Publish keeps the reservation visible until publish has made the pane visible
// to future counters. Both paths use coordinator -> dispatcher lock ordering.
func (c *ViewerPlacementCoordinator) Publish(id string, publish func() bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	published := publish == nil || publish()
	if published {
		delete(c.reserved, id)
	}
	return published
}

type ViewerPlacement struct {
	Kind, Target             string
	MaxPanes, MainPaneHeight int
}
type ViewerOverrides struct {
	Placement string
	MaxPanes  int
}

func ResolveViewerPlacement(rec Record, overrides ViewerOverrides, cfg *config.Config, live int) ViewerPlacement {
	p := ViewerPlacement{Kind: "window", MaxPanes: cfg.DispatchViewerMaxPanes(), MainPaneHeight: cfg.DispatchViewerMainPaneHeight()}
	if rec.CallerPaneID == "" || rec.CallerSessionID == "" || rec.CallerSessionID == dispatchViewerSession {
		return p
	}
	placement := cfg.DispatchViewerPlacement()
	if overrides.Placement == "pane" || overrides.Placement == "window" {
		placement = overrides.Placement
	}
	if overrides.MaxPanes >= 1 && overrides.MaxPanes <= 6 {
		p.MaxPanes = overrides.MaxPanes
	}
	if placement == "pane" && live < p.MaxPanes {
		p.Kind, p.Target = "split", rec.CallerPaneID
	}
	return p
}

func LiveViewerPaneCount(records []Record, sessionID, windowID string) int {
	n := 0
	for _, r := range records {
		if r.CallerSessionID != sessionID || r.CallerWindowID != windowID || r.ViewerKind != "split" {
			continue
		}
		pane := r.ViewerPaneID
		if r.Mode == ModeInteractive {
			pane = r.PaneID
		}
		if pane == "" || r.Status == StatusReleased {
			continue
		}
		n++
	}
	return n
}

func ReadViewerSessionOverrides(ctx context.Context, tmuxPath, session string, command func(context.Context, string, ...string) *exec.Cmd) ViewerOverrides {
	out, err := command(ctx, tmuxPath, tmux.Args("show-options", "-t", session)...).Output()
	if err != nil {
		return ViewerOverrides{}
	}
	var o ViewerOverrides
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "@leo_viewer_placement":
			o.Placement = f[1]
		case "@leo_viewer_max_panes":
			o.MaxPanes, _ = strconv.Atoi(f[1])
		}
	}
	return o
}
