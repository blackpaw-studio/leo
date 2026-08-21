# One dormant agent state

Collapse `suspended` and `stopped` into a single dormant state named **stopped**,
make deletion an explicit verb rather than a side effect of stopping, and give
the attach picker start / stop / delete.

## Motivation

An ephemeral agent can currently be dormant in two different ways, and the
difference between them is not the one the names suggest.

`Manager.Suspend` (`internal/agent/manager.go:895`) kills the process and tmux
session, keeps the record, keeps `SessionID`, and sets `Suspended=true`. The
agent auto-resumes on the next inbound message.

`Manager.Stop` (`internal/agent/manager.go:820`) kills the process and tmux
session and then branches on workspace type:

- worktree agent (`Branch != ""`) — `Stopped=true`, record and `SessionID` kept,
  recoverable via `Restart`
- shared-workspace agent — `agentstore.Remove`, the record is gone

So `stop` already means two unrelated things. On a worktree agent it is roughly
"suspend without auto-wake"; on every other agent it is a delete. Meanwhile
`leo agent prune` (`manager.go:1407`) is the real delete verb but refuses shared
agents outright (`TestPruneRejectsSharedAgent`). There is no way to delete a
shared agent deliberately, and no way to stop one without deleting it.

The second cost is structural. `Stopped` and `Suspended` are independent booleans
that every lifecycle path must keep mutually exclusive by hand. That invariant is
what broke in #157 and #158: records ending up both stopped and suspended,
suspended agents unresolvable on every lifecycle route, failed-restore records
with no retry path and no way to delete them. Collapsing to one dormant flag
removes the bug class instead of patching instances of it.

## Non-goals

- **An MCP delete tool.** Agents must not be able to delete themselves or each
  other. `leo_stop_agent` keeps working unchanged and now means "go dormant".
- **Renaming `idle_suspend_after`.** The config key stays. "Suspend" survives as
  an English word describing auto-dormancy, not as a state name. Renaming would
  either churn a live config across every template or require a compat shim.
- **Changing `reset` or `restart`.** `reset` still clears the session id;
  `restart` still bounces a live agent. Neither is a dormancy transition.
- **Compat aliases.** `suspend`, `resume`, and `prune` are removed, not
  deprecated. Leo has one user.

## State model

`agentstore.Record` (`internal/agentstore/store.go`):

| Field | Change |
|---|---|
| `Stopped bool` | Unchanged name; becomes the single dormant flag |
| `StoppedReason string` | Unchanged — display detail and boot-retry signal |
| `Suspended bool` | **Deleted** |
| `WakeOnMessage bool` | **New** (`json:"wake_on_message,omitempty"`) |
| `NoResume bool` | Unchanged — unrelated crash-loop guard for `--resume` |
| `SessionID`, `Branch`, `IdleSuspendAfter` | Unchanged |

A dormant agent always keeps its record and its `SessionID`, whatever its
workspace type. Deletion is the only thing that removes a record.

`WakeOnMessage` carries intent, so it is a flag and not a state:

- idle sweep stops an agent → `WakeOnMessage=true`
- an operator stops an agent → `WakeOnMessage=false`
- `Start` clears it along with `Stopped` and `StoppedReason`

Only `WakeOnMessage=true` permits auto-wake on an inbound message. This is the
one behavior the merge must not lose: without it a cron-fired persistent task or
a routed channel message would resurrect an agent that was deliberately shut
down.

### Migration

One-way, on load, in `agentstore.Load`. A record with `suspended: true` becomes
`stopped: true, wake_on_message: true`; a record with `stopped: true` and no
`suspended` key keeps `wake_on_message` false. The `suspended` key is dropped on
the next save. No reverse path — an older binary reading a migrated store sees a
stopped agent, which is correct if pessimistic.

## Manager API

| Today | After |
|---|---|
| `Stop(name) error` | `Stop(name string, opts StopOptions) error` — always dormant, never removes the record. `StopOptions{WakeOnMessage bool}` |
| `Suspend(name) error` | removed — `Stop(name, StopOptions{WakeOnMessage: true})` |
| `Resume(name) error` | `Start(name) error` — clears `Stopped`, `StoppedReason`, `WakeOnMessage`; resolves `SessionID`; respawns |
| `Prune(ctx, name, PruneOptions)` | `Delete(ctx, name, DeleteOptions)` — accepts shared agents; removes the record, plus worktree and branch when `Branch != ""` |
| `Restart`, `Reset` | unchanged |

`Stop` on an already-dormant agent is idempotent and updates `WakeOnMessage` to
the requested value. `Start` on a live agent returns a new typed
`ErrAgentAlreadyRunning`, mapped to 409.

`Delete` keeps `Prune`'s refusal to act on a live agent: the operator stops it
first. This is deliberate — it makes deletion a two-step act on anything that is
currently doing work, and it keeps the confirmation honest about what is being
removed. The CLI and picker both say "stop it first" rather than offering a
force-delete.

Typed errors (`internal/agent/errors.go:55`): `ErrAgentSuspended` →
`ErrAgentStopped`, `ErrAgentNotSuspended` → `ErrAgentNotStopped`.
`ErrAgentNotRunning` is unchanged. HTTP mapping stays 409/404 as set in #158.

## Boot restore

`RestoreAgents` (`internal/service/agents.go:51`) loses its `Suspended` branch.
It skips any record with `Stopped=true` unless `IsFailedRestore()` — that is,
unless `StoppedReason` is non-empty — in which case it retries the spawn exactly
as it does today. `IsFailedRestore` keeps its current definition.

`sweepIdleAgents` (`internal/service/sweep.go:60`) calls
`Stop(name, StopOptions{WakeOnMessage: true})`.

## Surfaces

**CLI** (`internal/cli/agent.go`) — `newAgentSuspendCmd` (`:857`),
`newAgentResumeCmd` (`:899`), and `newAgentPruneCmd` (`:1180`) are removed.
`newAgentStopCmd` (`:753`) keeps `--force`/`--json`, loses `--prune` and
`--delete-branch`. New `newAgentStartCmd` and `newAgentDeleteCmd`; delete carries
`--delete-branch` and `--yes`, and without `--yes` prompts with the same text the
picker uses. `leo agent status` (`internal/cli/status.go:58`) drops its
`suspended` counter into the stopped one.

**Attach picker** (`internal/picker/`) — keys (`keys.go:20`):

```
enter  attach (starts it first if dormant)
s      stop
u      start
D      delete        ← y/n confirm
r      rename
t      template
/      filter    q  quit
x      (unbound)
```

`enter` on any dormant agent becomes start-then-attach, replacing today's
"stopped — press u to resume" hint (`model.go:261`). Stop loses its confirm — it
is reversible now. Delete reuses `updateConfirm` (`model.go:356`) and names
exactly what will be removed:

```
delete pretty-sky? removes worktree + branch feat/foo (y/n)
delete rocket? removes the agent record only (y/n)
```

`x` is deliberately left unbound so existing `x`-then-`y` muscle memory does
nothing rather than deleting an agent. Status glyphs (`rows.go:65`) collapse `◌`
and `✖` into one dormant glyph; `AttachOnly` remote fallback rows keep refusing
every lifecycle action, delete included.

**Daemon HTTP** (`internal/daemon/server.go:169`) — `POST /agents/{name}/suspend`
is removed (callers use `/stop` with a body flag for `WakeOnMessage`),
`POST /agents/{name}/resume` becomes `POST /agents/{name}/start`, `/stop` keeps
its path, and `POST /agents/{name}/prune` becomes `DELETE /agents/{name}`.
`internal/daemon/client.go` follows.

**Web UI** (`internal/web/handlers_agents.go`, `templates/agents.html:54`) — the
suspend/resume pair becomes stop/start plus delete with a confirm dialog carrying
the same text as the picker. The `AgentService` interface loses `Suspend`/`Resume`
and gains `Start`/`Delete`; `ResolveRecoverable` is unchanged.

**Docs** — `docs/configuration/config-reference.md`,
`docs/configuration/persistent-tasks.md`, `docs/configuration/permissions.md`,
and any agent-lifecycle page lose the suspended/stopped distinction. The
`config-reference.md` claim that stop "kills workspace and conversation" was
already wrong and goes away with it.

## Testing

Test-first, per repo discipline. The load-bearing cases:

- `agentstore`: migration of a `suspended: true` record; a `stopped: true` record
  gaining no wake flag; `suspended` key dropped on save.
- `Manager.Stop`: a shared-workspace agent stays in the store (the inversion of
  today's behavior — this is the regression guard for the whole change).
- `Manager.Stop`: manual stop leaves `WakeOnMessage=false`; idle sweep leaves it
  true; both leave `SessionID` intact.
- Auto-wake: an inbound message wakes a `WakeOnMessage=true` agent and does *not*
  wake a manually stopped one. Cover the persistent-task injection path as well
  as the channel path — they are separate code paths.
- `Manager.Delete`: succeeds on a shared agent (replacing
  `TestPruneRejectsSharedAgent`), still refuses a live agent, still removes
  worktree and branch.
- `RestoreAgents`: skips a manually stopped agent, retries a failed-restore
  record, no longer has a suspended case.
- Picker: `enter` on a dormant agent dispatches start-then-attach; `D` arms the
  confirm; `x` is inert.

`make test` and `make e2e` both run before push — config and argv changes are
exactly what the e2e build tag gates.

## Phasing

One PR. The rename cascade is mechanical once the store and manager change, and
splitting it would leave `main` in a state where the picker and the daemon
disagree about what states exist.
