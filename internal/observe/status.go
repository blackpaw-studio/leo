package observe

// MapStatus maps a raw supervisor/agentstore status string onto one of the
// four observe.Status wire values. It is the single source of truth for this
// mapping: both internal/service (the event-stream path) and internal/web
// (the snapshot path) call it, so a status can never map differently
// depending on which transport reports it.
//
// "restarting" is a purely internal crash-loop-backoff state with no wire
// equivalent of its own; it folds into "starting", the closest lifecycle
// state a consumer can act on. Anything else unrecognized becomes
// StatusStopped so the API never emits a status a consumer wasn't told to
// expect.
func MapStatus(raw string) Status {
	switch raw {
	case "running":
		return StatusRunning
	case "starting", "restarting":
		return StatusStarting
	case "suspended":
		return StatusSuspended
	case "stopped":
		return StatusStopped
	default:
		return StatusStopped
	}
}
