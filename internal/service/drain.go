package service

import (
	"fmt"
	"os"

	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/drain"
)

// drainLogf writes graceful-restart progress to the service log.
func drainLogf(format string, args ...any) {
	fmt.Fprintf(os.Stdout, format+"\n", args...)
}

// newDrainManager builds the graceful-restart drain over the supervisor's
// agents, the daemon's cron scheduler and agent messenger, and the
// pending-resume file under statePath.
func newDrainManager(srv *daemon.Server, sup *Supervisor, tmuxPath, statePath string) *drain.Manager {
	return drain.New(drain.Deps{
		Prober:   newDrainProber(sup, tmuxPath, drainLogf),
		Notifier: srv.DrainNotifier(),
		Cron:     srv.Scheduler(),
		Store:    drain.NewFileStore(statePath),
		Clock:    drain.SystemClock{},
		Logf:     drainLogf,
	})
}
