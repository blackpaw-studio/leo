package daemon

import (
	"context"

	"github.com/blackpaw-studio/leo/internal/observe"
)

// ObservePublisher is the production observe.Publisher wired into
// internal/run before every `leo run` invocation (see internal/cli/run.go).
// It relays task_run_* events to this workspace's daemon over the existing
// Unix-socket IPC via PublishTaskRun, since internal/run.Run only ever
// executes inside a `leo run` subprocess — never inside the daemon's own
// process — so it has no direct handle on the daemon's observe.RunLog.
//
// Best-effort and non-fatal by construction: Publish never returns an error
// (it satisfies observe.Publisher, whose Publish returns nothing) and any
// transport failure — no daemon running, a slow socket, whatever — is
// swallowed here. Task execution must never depend on the daemon being
// reachable.
type ObservePublisher struct {
	homePath string
}

// NewObservePublisher returns a Publisher that reports task-run events to
// the daemon at homePath's socket, if one is listening.
func NewObservePublisher(homePath string) *ObservePublisher {
	return &ObservePublisher{homePath: homePath}
}

// Publish forwards ev to the daemon if it carries a TaskRunPayload — the
// only payload internal/run's producers emit. Non-task payloads and
// transport errors are both silently dropped: this route only relays
// task-run events, and a run must never fail or stall because the daemon is
// unreachable or slow.
func (p *ObservePublisher) Publish(ev observe.Event) {
	payload, ok := ev.Payload.(*observe.TaskRunPayload)
	if !ok {
		return
	}
	// PublishTaskRun applies its own short timeout independent of any
	// deadline on ctx, so a background context here is intentional and safe.
	_ = PublishTaskRun(context.Background(), p.homePath, ev.Type, payload.Run) //nolint:errcheck // best-effort by design
}
