package service

import (
	"os/exec"
	"sync"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/bridge"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

// procIdentity is the single source of truth for a supervised process's mutable
// identity: its name (which drives the tmux session name and supervisor map
// keys) and its claude args (which carry --name). superviseProcess reads from
// it on every poll/iteration so a live RenameAgent is picked up without a
// process restart.
type procIdentity struct {
	mu   sync.RWMutex
	name string
	args []string
	// bridge is how the live launch uses the leo bridge; see BridgeRoute.
	bridge BridgeRoute
	// harness is the launch's harness adapter name ("" means claude); fixed
	// at spawn.
	harness string
}

func newProcIdentity(name string, args []string) *procIdentity {
	cp := make([]string, len(args))
	copy(cp, args)
	return &procIdentity{name: name, args: cp}
}

func (p *procIdentity) Name() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.name
}

// SessionName returns the tmux session name for the current identity name.
func (p *procIdentity) SessionName() string {
	return agent.SessionName(p.Name())
}

// Args returns a copy of the current claude args.
func (p *procIdentity) Args() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	cp := make([]string, len(p.args))
	copy(cp, p.args)
	return cp
}

// setArgs replaces the stored args (used by the quick-exit strip-resume path).
func (p *procIdentity) setArgs(args []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := make([]string, len(args))
	copy(cp, args)
	p.args = cp
}

// rename swaps the name and rewrites the value following --name in args, taking
// the write lock. Use renameLocked when already holding p.mu.
func (p *procIdentity) rename(newName string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.renameLocked(newName)
}

// renameLocked performs the name + --name-arg swap. The caller MUST hold p.mu.
// RenameAgent uses this so the swap stays inside the lock it already holds
// across the tmux rename.
func (p *procIdentity) renameLocked(newName string) {
	p.name = newName
	for i := 0; i+1 < len(p.args); i++ {
		if p.args[i] == "--name" {
			p.args[i+1] = newName
			break
		}
	}
}

// tmuxRenameSession and tmuxHasSession are package-level seams so RenameAgent
// and waitForSessionEnd can be unit-tested without a real tmux. They default to
// real exec, mirroring supervisedExecFn.
var tmuxRenameSession = func(tmuxPath, oldName, newName string) error {
	return exec.Command(tmuxPath, tmux.Args("rename-session", "-t", tmux.Target(oldName), newName)...).Run()
}

var tmuxHasSession = func(tmuxPath, session string) bool {
	return exec.Command(tmuxPath, tmux.Args("has-session", "-t", tmux.Target(session))...).Run() == nil
}

// BridgeRoute is how one launch of an identity uses the leo bridge.
type BridgeRoute struct {
	// Target is the generation of the bridge key the launch's claude mod
	// connects under; the zero Target for a legacy launch. Its mod has
	// settled once it connected (bridge.State.HasConnected).
	Target bridge.Target
	// Planned is set once a launch has decided on the bridge: a
	// just-spawned agent's first launch is planned on the supervise
	// goroutine, after the spawn call returns.
	Planned bool
}

// BridgeKey returns the key the live launch's claude mod connects under
// ("" when the launch did not load the bridge). Fixed per launch: a rename
// cannot change the environment of a running claude.
func (p *procIdentity) BridgeKey() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.bridge.Target.Key
}

// BridgeRoute returns how the live launch uses the bridge.
func (p *procIdentity) BridgeRoute() BridgeRoute {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.bridge
}

// setBridge records a bridged launch of target.
func (p *procIdentity) setBridge(target bridge.Target) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bridge = BridgeRoute{Target: target, Planned: true}
}

// setLegacy records a launch without the bridge.
func (p *procIdentity) setLegacy() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bridge = BridgeRoute{Planned: true}
}
