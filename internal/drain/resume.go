package drain

import "context"

// ResumeDeps are what Resume needs from the new daemon.
type ResumeDeps struct {
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
// left off, then runs the deferred cron tasks. Failures are logged and
// never stop the rest.
func Resume(ctx context.Context, p PendingResume, d ResumeDeps) {
	logf := d.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	for _, name := range p.Agents {
		if !d.Exists(name) {
			logf("drain %s: resume skipped %s: the agent no longer exists", p.DrainID, name)
			continue
		}
		if d.AwaitBridge != nil {
			d.AwaitBridge(ctx, name)
		}
		if _, err := d.Notifier.Message(ctx, name, ResumeText, ""); err != nil {
			logf("drain %s: resume message to %s: %v", p.DrainID, name, err)
			continue
		}
		logf("drain %s: resumed %s", p.DrainID, name)
	}
	if len(p.DeferredTasks) > 0 && d.RunTasks != nil {
		logf("drain %s: running deferred task(s) %v", p.DrainID, p.DeferredTasks)
		d.RunTasks(p.DeferredTasks)
	}
}
