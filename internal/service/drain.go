package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/bridge"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/drain"
)

// drainLogf writes graceful-restart progress to the service log.
func drainLogf(format string, args ...any) {
	fmt.Fprintf(os.Stdout, format+"\n", args...)
}

// newDrainManager builds the graceful-restart drain over the supervisor's
// agents, the daemon's cron scheduler and agent messenger, and the
// pending-resume file in homePath's state dir.
func newDrainManager(srv *daemon.Server, sup *Supervisor, tmuxPath, homePath string) *drain.Manager {
	return drain.New(drain.Deps{
		Prober:   newDrainProber(sup, tmuxPath, drainLogf),
		Notifier: srv.DrainNotifier(),
		Cron:     srv.Scheduler(),
		Store:    drain.NewFileStore(filepath.Join(homePath, "state")),
		Clock:    drain.SystemClock{},
		Logf:     drainLogf,
	})
}

// resumeBridgeWait bounds how long a resume waits for an agent's mod to
// reconnect before it falls back to the legacy delivery path.
const resumeBridgeWait = 60 * time.Second

// pendingResumeDeps are runPendingResume's collaborators.
type pendingResumeDeps struct {
	store interface {
		Read() (drain.PendingResume, bool, error)
	}
	resume drain.ResumeDeps
	logf   func(format string, args ...any)
}

// runPendingResume resumes the agents a graceful restart drained, if the
// previous daemon left a pending-resume file. The file is read, not taken:
// drain.Resume discharges it agent by agent as each resume goes out. A file
// that cannot be read is logged and the daemon boots normally.
func runPendingResume(ctx context.Context, d pendingResumeDeps) {
	p, ok, err := d.store.Read()
	if err != nil {
		d.logf("drain: pending resume: %v", err)
		return
	}
	if !ok {
		return
	}
	d.logf("drain %s: resuming %d agent(s) after the restart", p.DrainID, len(p.Agents))
	drain.Resume(ctx, p, d.resume)
}

// agentRecordExists reports whether homePath's agentstore holds a record
// for name: a dormant, wakeable agent counts, and messageAgent wakes it.
func agentRecordExists(homePath string) func(string) bool {
	return func(name string) bool {
		records, err := agentstore.Load(agentstore.FilePath(homePath))
		if err != nil {
			return false
		}
		_, ok := records[name]
		return ok
	}
}

// awaitAgentBridge waits up to settle for name's launch to settle (see
// awaitTaskBridge): its bridge route decided and, for a bridged launch,
// its mod connected. A restored agent decides its route on its supervise
// goroutine, after restore returns, so an undecided route is waited on,
// not read as "no bridge".
func awaitAgentBridge(route func(string) BridgeRoute, hub *bridge.Hub, settle time.Duration) func(context.Context, string) {
	return func(ctx context.Context, name string) {
		if hub == nil {
			return
		}
		target, settled, err := awaitTaskBridge(ctx, hub, route, name, settle)
		if settled || err != nil || (target.Key == "" && route(name).Planned) {
			return
		}
		drainLogf("drain: %s's bridge did not settle within %s; resuming it without the bridge", name, settle)
	}
}

// startPendingResume resumes drained agents in the background once agent
// restore has finished.
func startPendingResume(ctx context.Context, srv *daemon.Server, sup *Supervisor, hub *bridge.Hub, homePath string) {
	store := drain.NewFileStore(filepath.Join(homePath, "state"))
	go runPendingResume(ctx, pendingResumeDeps{
		store: store,
		resume: drain.ResumeDeps{
			Store:       store,
			Exists:      agentRecordExists(homePath),
			AwaitBridge: awaitAgentBridge(sup.BridgeRouteOf, hub, resumeBridgeWait),
			Notifier:    srv.DrainNotifier(),
			RunTasks:    srv.Scheduler().RunNow,
			Logf:        drainLogf,
		},
		logf: drainLogf,
	})
}
