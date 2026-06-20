# Idle-Suspend for Ephemeral Agents — Design

**Date:** 2026-06-19
**Status:** Approved (design)

## Summary

Add an opt-in **suspended** state for ephemeral agents. When an agent has had no
activity for a configured interval (e.g. `24h`), the daemon kills its claude
process and tmux session to free resources, while preserving the workspace and
`SessionID` so the conversation can be resumed exactly where it left off. Off by
default; enabled by setting an idle-suspend interval.

Resume is automatic on the next incoming message (with a manual path as well).

## Goals

- Free memory / process slots / tmux clutter for agents that have gone idle.
- Lose nothing: workspace, worktree, and conversation history are all preserved.
- Zero behavior change when no interval is configured (current behavior).

## Non-Goals

- Suspending supervised service processes or persistent task sessions — **ephemeral
  agents only**.
- Reducing API cost (idle claude sessions in tmux don't consume API tokens; the win
  is local resources and clutter).

## State Model & Semantics

A new resumable-dormant state between "running" and "stopped":

- **`suspended`**: no live process/tmux session, but the agentstore record is kept
  with `SessionID` preserved and a new `Suspended bool` flag — parallel to the
  existing worktree `Stopped` flag, but **distinct**.
- Listing (`leo agent list`, web UI) merges supervisor live-states with agentstore
  records:
  - record with `Suspended=true` and no live supervisor state → renders **suspended**
  - record with `Stopped=true` → gone / prunable (unchanged)
- Suspend ≠ stop:
  - **stop** is user-initiated, terminal, and worktree agents are eventually pruned.
  - **suspend** is automatic, non-destructive, and auto-recoverable.
- Pruning keys off `Stopped`, so suspended worktree agents are **never pruned**.

## Activity Signal & Suspend Sweep

### Signal
Use tmux's built-in per-session `session_activity` (last-activity epoch). This
single source of truth captures prompt injection, interactive typing in an attached
pane, and Claude's own output — with **no new hooks** anywhere. A long-running
claude turn producing output counts as active, which is the desired behavior.

### Sweep
A goroutine in the daemon (alongside the existing `session_router` janitor) runs on
a fixed tick (default **60s**). For each live ephemeral agent that has a resolved
idle interval:

1. Query tmux `#{session_activity}` and `#{session_attached}` for its session.
2. If `session_attached >= 1` → **skip** (attached guard — never suspend a session
   someone is attached to, even if the activity timer expired, e.g. reading
   scrollback). The idle clock effectively starts when they detach.
3. If `now - session_activity >= interval` → call `Suspend(name)`.

### Suspend(name)
- Mark agentstore record `Suspended=true`, keep `SessionID`, ensure `NoResume=false`.
- Cancel the supervise context + kill the tmux session (reuses `StopAgent`
  mechanics, **minus** the record removal). Cancelling the context cleanly exits the
  `superviseProcess` restart loop so it does not fight the suspend.

## Auto-Wake on Incoming Message

The message-delivery path to an agent (the `leo_send_message` / web inject path,
`internal/tmux/inject.go`) gains a pre-check:

- If the target agent is suspended → `Resume(name)` first, **readiness-probe** until
  claude is ready, **then** inject.
- The readiness probe is load-bearing: without it the first post-wake message is lost
  (the cold-start-injection bug fixed in PR #82). Reuse that readiness pattern.

### Resume(name)
Reuses `RestoreAgents`' `argsWithResume` logic:
- read the agentstore record, clear `Suspended`,
- append `--resume <SessionID>` to claude args,
- call `SpawnAgent`, updating `StartedAt`.

### Manual paths
- `leo agent resume <name>` (CLI) + web "resume" action.
- `leo agent suspend <name>` (CLI) — cheap, since `Suspend()` already exists.
- Suspend/resume is guarded so a message arriving mid-suspend resolves to a single
  resume (no double-spawn race).

## Config (full cascade, off by default)

- `defaults.idle_suspend_after: "24h"` — global; omitempty; `""`/unset = disabled.
- `templates.<name>.idle_suspend_after` — per-template override.
- `leo agent spawn … --idle-suspend 24h` — per-spawn override.

**Resolution** (spawn flag > template > defaults) happens **at spawn time**, and the
resolved duration is persisted on the agentstore record (new field
`IdleSuspendAfter string`). The sweep reads the record, not live config — so behavior
is stable across config edits and survives daemon restart / restore.

`Config.Validate` parses each value with `time.ParseDuration` (must be `> 0` when
set). Follows the existing `SessionConfig.IdleTimeout` string-duration precedent.

## Edge Cases & Error Handling

- **Daemon restart while suspended:** `RestoreAgents` skips `Suspended=true` records
  (just as it skips `Stopped=true`) — they stay suspended, not auto-spawned.
  Suspension survives restarts.
- **No interval configured:** agent never suspends (today's behavior).
- **Resume failure:** if `--resume` quick-exits on a stale session-id, fall back
  through the existing `--resume → fresh` degrade path (PR #84) rather than
  crash-looping.
- **Attached but idle:** never suspended (attached guard above).
- **Race on suspend/resume:** guarded to a single resolution.

## Testing

- **Unit:**
  - cascade resolution (spawn flag > template > defaults; unset = off).
  - idle decision (mock `session_activity` / `session_attached` via the existing
    `execCommand`-style seams): attached-guard skip, under-threshold no-op,
    over-threshold suspend.
  - `RestoreAgents` skips suspended records.
  - `Resume` rewrites args with `--resume <SessionID>`.
- **Integration:**
  - suspend → record state → incoming message → auto-wake → readiness probe →
    delivery succeeds (no lost first message).

## Key Touch Points (from lifecycle investigation)

| Concern | File |
|---|---|
| Agent record + new `Suspended` / `IdleSuspendAfter` fields | `internal/agentstore/store.go` |
| Suspend/Resume methods, list merge | `internal/agent/manager.go` |
| StopAgent mechanics reused by Suspend | `internal/service/process.go` |
| Restore skips suspended; `argsWithResume` reused by Resume | `internal/service/agents.go` |
| Sweep goroutine (next to janitor) | `internal/daemon/session_router.go` / daemon server |
| Auto-wake pre-check before inject | `internal/tmux/inject.go` + message-delivery handler |
| Config fields + validation | `internal/config/config.go` |
| CLI suspend/resume + `--idle-suspend` flag | `internal/cli/` |
