package web

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/leomcp"
)

// delegationStateFromConfig is the delegation policy bridged agents get:
// the section the mod adds to the system prompt while delegation is on, and
// the native agents it hides then.
func delegationStateFromConfig(cfg *config.Config) bridge.DelegationState {
	section := leomcp.DelegationSection(cfg)
	if section == "" {
		return bridge.DelegationState{HideAgents: []string{}}
	}
	return bridge.DelegationState{Enabled: true, Section: section, HideAgents: cfg.Delegation.HiddenNativeAgents()}
}

// delegationSource serves the delegation policy from the config file,
// rereading it only when the file changed: the state pusher asks every
// second. A reload that fails keeps the last policy.
type delegationSource struct {
	path string
	load func() (*config.Config, error)

	mu      sync.Mutex
	modTime time.Time
	size    int64
	loaded  bool
	state   bridge.DelegationState
}

// Get returns the current delegation policy.
func (d *delegationSource) Get() bridge.DelegationState {
	d.mu.Lock()
	defer d.mu.Unlock()
	info, err := os.Stat(d.path)
	if err == nil && d.loaded && info.ModTime().Equal(d.modTime) && info.Size() == d.size {
		return d.state
	}
	cfg, loadErr := d.load()
	if loadErr != nil {
		fmt.Fprintf(os.Stderr, "bridge: reading delegation config: %v\n", loadErr)
	} else {
		d.state = delegationStateFromConfig(cfg)
	}
	if err == nil {
		d.modTime, d.size, d.loaded = info.ModTime(), info.Size(), true
	}
	return d.state
}
