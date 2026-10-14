package drain

import (
	"context"
	"slices"
)

// ResumeDeps are what Resume needs from the new daemon.
type ResumeDeps struct {
	// Store holds the obligation: Resume rewrites it as agents are handled
	// and removes it once nothing is left.
	Store Store
	// Exists reports whether an agent of that name is still known.
	Exists func(name string) bool
	// AwaitBridge waits, bounded, for name's mod to reconnect so the
	// message goes over the bridge rather than a tmux paste. It returns
	// either way.
	AwaitBridge func(ctx context.Context, name string)
	Notifier    Notifier
	// RunTasks runs the cron tasks the previous daemon deferred.
	RunTasks func(names []string)
	Logf     func(format string, args ...any)
}

// Resume tells each drained agent that still exists to pick up where it
// left off, then runs the deferred cron tasks. The store is rewritten after
// each agent is handled — resumed, or dropped because it no longer exists —
// so a crash part-way leaves exactly the unfinished work for the next boot
// (an agent may then get a second resume message, which is harmless). An
// agent whose message fails stays in the file; the file is removed once no
// agent and no task is left.
func Resume(ctx context.Context, p PendingResume, d ResumeDeps) {
	logf := d.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	remaining := slices.Clone(p.Agents)
	tasks := p.DeferredTasks
	persist := func() {
		next := PendingResume{DrainID: p.DrainID, Agents: remaining, DeferredTasks: tasks, WrittenAt: p.WrittenAt}
		if err := d.Store.Write(next); err != nil {
			logf("drain %s: updating pending resume: %v", p.DrainID, err)
		}
	}
	for _, name := range p.Agents {
		if !d.Exists(name) {
			logf("drain %s: resume skipped %s: the agent no longer exists", p.DrainID, name)
			remaining = slices.DeleteFunc(remaining, func(n string) bool { return n == name })
			persist()
			continue
		}
		if d.AwaitBridge != nil {
			d.AwaitBridge(ctx, name)
		}
		if _, err := d.Notifier.Message(ctx, name, ResumeText, ""); err != nil {
			logf("drain %s: resume message to %s: %v (kept for the next daemon start)", p.DrainID, name, err)
			continue
		}
		logf("drain %s: resumed %s", p.DrainID, name)
		remaining = slices.DeleteFunc(remaining, func(n string) bool { return n == name })
		persist()
	}
	ranTasks := false
	if len(tasks) > 0 && d.RunTasks != nil {
		logf("drain %s: running deferred task(s) %v", p.DrainID, tasks)
		d.RunTasks(tasks)
		tasks, ranTasks = nil, true
	}
	switch {
	case len(remaining) == 0:
		if err := d.Store.Remove(); err != nil {
			logf("drain %s: removing pending resume: %v", p.DrainID, err)
		}
	case ranTasks:
		persist()
	}
}
