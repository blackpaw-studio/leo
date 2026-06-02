package service

import (
	"os/exec"
	"sync"

	"github.com/blackpaw-studio/leo/internal/agent"
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

// rename swaps the name and rewrites the value following --name in args. The
// caller (RenameAgent) holds this under the supervisor lock so the tmux session
// rename and this swap are observed atomically by the watcher's RLock.
func (p *procIdentity) rename(newName string) {
	p.mu.Lock()
	defer p.mu.Unlock()
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
var tmuxRenameSession = func(tmuxPath, old, new string) error {
	return exec.Command(tmuxPath, tmux.Args("rename-session", "-t", old, new)...).Run()
}

var tmuxHasSession = func(tmuxPath, session string) bool {
	return exec.Command(tmuxPath, tmux.Args("has-session", "-t", session)...).Run() == nil
}
