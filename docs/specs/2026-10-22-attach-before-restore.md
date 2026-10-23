# Attach/start before agent restore

**Status:** draft · 2026-10-22

## Problem
Between daemon start and `RestoreAgents` + `MarkReady` (`internal/service/process.go:1109-1117`),
three views of an agent disagree:
- `Manager.List` / `hydrate` report a non-live record as `stopped` (`manager.go:1090`, `resolve.go:190-205`).
- `AgentSession` reports `Stopped: true`, so `leo attach` offers to start it.
- `Manager.Start` checks the persisted `rec.Stopped` (false) and returns `ErrAgentNotStopped` (409).

The user is offered an action that cannot succeed. During the v0.58.0 update this window lasted ~11 min.

## Behavior
1. **One readiness signal.** Manager gets an injected `isReady func() bool`, wired to the daemon's
   existing `ready` flag (`MarkReady`). No new global.
2. **Status.** While not ready, a record that is not live and not `Stopped` reports status
   `restoring`. Records marked `Stopped` still report `stopped`. After ready, nothing changes.
3. **Start.** While not ready, `Start` on a not-live, not-`Stopped` record returns a new
   `ErrDaemonRestoring`, mapped to HTTP 503 with a distinct error code. Starting a record that is
   explicitly `Stopped` still works during restore, and so does `ErrAgentAlreadyRunning`.
4. **Attach.** `AgentSessionResponse` gains `Restoring bool`. When it is set, `leo attach` prints
   `daemon starting, agents not restored yet — waiting…`, polls until it clears, then attaches
   normally. It never calls start in that state. Timeout: 2 min (a package var, for tests). On timeout
   it exits non-zero with `daemon is still restoring agents; try again shortly`.
5. **List output.** `leo agent list` shows `restoring` verbatim. The web UI shows `restoring`.
   `/api/v1` keeps its four wire values: `observe.MapStatus` folds `restoring` into `stopped` (adding a
   wire value is an API contract change; follow-up pending Evan's sign-off). A Start from an API
   consumer during restore gets 503 `daemon_restoring`.
6. **Remote.** `runRemoteAttach` executes `leo attach` on the host, so it inherits this behavior. No
   separate change.

## Out of scope
- Speeding up startup or restore (spec: startup-stall diagnostics).
- Fixing the `ErrAgentNotStopped` record-stale mismatch after ready.

## Tests (write first, watch fail)
- Manager: `Start` before ready → `ErrDaemonRestoring`; after ready, existing behavior
  (`TestStartNotStoppedErrors` keeps its case with ready=true made explicit).
- Manager: `List` before ready → `restoring` for a non-stopped record, `stopped` for a stopped one.
- Handler: `ErrDaemonRestoring` → 503 with its code.
- CLI attach: `Restoring` → waits and attaches once it clears, and never calls `agentStartFn`;
  times out with the message above.
