# Collapse "persistent sessions" into "agents"

**Date:** 2026-07-14
**Status:** Approved (design)

## Problem

After PR #112 collapsed processes into agents, Leo still carries a second
agent-shaped primitive: **persistent sessions**. A session is a long-running,
daemon-supervised harness instance in tmux (`leo-session-<name>`) with a
workspace, channels, and a restart loop — functionally an agent. Its only
distinguishing features are (a) it is declared in the `sessions:` config map
(the same static-declaration pattern the process collapse just removed) and
(b) scheduled tasks with `runtime: persistent` inject prompts into it through
the daemon's session router.

The router machinery — per-target FIFO queue, `queue_max`, readiness-probed
injection, invocation markers, Stop-hook completion reporting, timeouts,
`notify_on_fail` — is **task-delivery** machinery, not session machinery. It
is keyed on a tmux target and does not care what it injects into.

The sole user's live config contains zero `sessions:` entries and zero
`runtime: persistent` tasks, so there is no migration surface at all.

## Decision

Retire the session primitive. Scheduled tasks target **agents**. The delivery
pipeline survives nearly intact, repointed at agent tmux sessions. Config
becomes `defaults`, `web`, `templates`, `tasks` — two primitives (agents,
tasks), one daemon.

## Design

### 1. Config model

- The `sessions:` section and `SessionConfig` are deleted.
- `TaskConfig` gains one optional field: `template: <name>`.
  - `runtime: persistent` + `template: assistant` → the task's prompt is
    injected into the agent derived from that template — named after the
    template per the repo-less naming rule (PR #113). Multiple tasks naming
    the same template share one agent (and one FIFO queue).
  - `runtime: persistent` + no `template:` → the implicit case (old Topology
    A): the daemon synthesizes an in-memory `TemplateConfig` from the task's
    own fields (workspace, model, channels) and targets an agent **named
    after the task**. Nothing is written to `templates:` in config.
  - `session:` on a task becomes an unknown field (non-strict YAML loader
    ignores it); the `TaskConfig.Session` field is deleted.
- Division of responsibility: the **task** keeps schedule, prompt_file,
  queue_max, timeout, retries, channels, dev_channels, notify_on_fail,
  silent, enabled. The **template** owns the agent's identity and runtime
  config (workspace, model, env, add_dirs, mcp_config, harness,
  harness_options, channels, dev_channels, idle_suspend_after).
- Validation: for a persistent task with `template:`, the template must
  exist, and task channels must be a subset of the template's
  channels+dev_channels (ports today's task⊆session check). The implicit
  case needs no subset check (the synthesized template inherits the task's
  channels).

### 2. Delivery pipeline — kept, repointed

The daemon session router (`internal/daemon/session_router.go`) survives with
its semantics unchanged:

- Per-target FIFO queue with one in-flight slot; depth capped by the task's
  `queue_max` (default 5); enqueue rejected when full.
- Readiness-probed tmux injection; invocation markers correlate Stop-hook
  reports (`leo internal task-report`) for claude; timeout completion for
  non-claude (unchanged — see Follow-ups).
- `notify_on_fail` re-enqueue on rejection/failure.

Changes are addressing only: the queue key and tmux target become the **agent
name** (`leo-<agent>` via the agent session-name rule) instead of the session
name (`leo-session-<name>`). Rename router surfaces from "session" to task
delivery/target vocabulary where cheap, but renaming internal machinery is
not a goal in itself.

### 3. Ensure-exists semantics (the new seam)

When a persistent task fires, the daemon resolves the target agent and
guarantees it is injectable:

1. **Running** → inject.
2. **Suspended** → resume (same auto-wake the web message path performs in
   `handleWebAgentMessage`), wait for readiness, inject.
3. **Missing** (never spawned, or stopped/pruned by the user) → spawn from
   the task's template (explicit or synthesized), wait for readiness, inject.

Spawn/resume failures are task failures: they follow the existing failure
path (notify_on_fail, task history, non-zero result) rather than silently
dropping the prompt.

### 4. Idle-suspend interaction

Sessions ran 24/7. Task-target agents participate in the normal
idle-suspend sweep like any other agent and are woken (or respawned) by the
next cron fire. This is a deliberate improvement: no always-on requirement,
strictly better resource behavior. `idle_suspend_after` on the template (or
defaults cascade) governs.

### 5. Session-ID storage and reset

- The `session:<name>` entries in the session store die. Discovered claude
  session IDs are stored on the **agentstore record** (`SessionID` field
  agents already persist) via the same report path that discovers them.
- `leo session reset` is replaced by **`leo agent reset <name>`**: kill the
  agent's tmux, clear its stored SessionID (mark no-resume), and let the
  supervisor restart it fresh. This ports the one genuinely useful session
  command. It is CLI-only: the web sessions page dies with the primitive and
  no web reset action is added (unused by the sole user; add later if missed).

### 6. Deletions

- `SessionConfig`, the `sessions:` map, `TaskConfig.Session`,
  `SessionTopology`/`ResolveSession` (topologies collapse into the
  template-or-implicit rule in §1), the channel-subset check's session form.
- `SessionSpec`, `SessionSpecsFromConfig`, `superviseClaudeSession` /
  `superviseTUISession` and the session boot arm of the daemon —
  supervision is the agent supervisor, full stop.
- `leo session *` CLI (list/status/attach/logs/reset/drain). `leo agent *`
  equivalents already exist; reset is ported per §5. Drain is dropped (unused by the sole user).
- Web `/sessions` page + `handlers_sessions.go` + nav entry; daemon
  `/session/*` endpoints (reset/depth) — replaced by agent-addressed
  equivalents where §5 requires them.
- `leo-session-<name>` tmux naming.
- Session-only e2e tests are **retargeted, not deleted**: the persistent
  task pipeline (FIFO, queue_max, shared target, completion reporting) still
  needs coverage, now against agent targets.

### 7. Docs

`docs/configuration/persistent-tasks.md` is rewritten around the
task→template model; config-reference drops `sessions:`; CLAUDE.md's
primitives collapse to Agents + Tasks; website/README copy already says
"agents and scheduled tasks" (done in leo#114 / leo-website#2).

## Non-goals / Follow-ups

- **Non-claude completion reporting** stays timeout-based, exactly as today.
  Designated follow-up PR: turn-completion detection for codex/opencode
  (TUI idle-detection or log scanning in the harness drivers) — orthogonal
  harness-layer work, deliberately not coupled to this structural change.
- No new queue features (priorities, persistence across daemon restarts).
- No migration tooling: the sole user has zero sessions and zero persistent
  tasks configured.
- `lazy` and `idle_timeout` config stubs (parsed, never implemented) are
  deleted, not built — idle-suspend + auto-wake supersedes them.

## Testing

- Unit: task→template resolution (explicit, implicit, missing-template
  error), channel-subset validation, synthesized-template shape.
- Integration: ensure-exists matrix (running/suspended/missing ×
  inject succeeds; spawn/resume failure → task failure path), queue keying
  by agent name, session-ID storage on the agent record, `leo agent reset`.
- e2e (`make e2e`): retarget the persistent suite (basic fire, FIFO +
  queue_max rejection, shared target across two tasks) at agent targets;
  cover the implicit no-template case end-to-end.
- Config/argv changes → `make e2e` is mandatory before push; run
  `golangci-lint run ./...` locally (make lint does not catch what CI does).
