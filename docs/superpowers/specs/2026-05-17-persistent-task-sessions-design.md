# Persistent Task Sessions — Design

**Status:** Draft — pending implementation plan
**Date:** 2026-05-17
**Authors:** Evan Coleman (via brainstorming session)

## Motivation

Scheduled tasks today (`internal/run/runner.go`) invoke `claude -p <prompt>` as a one-shot subprocess. Every firing pays full claude startup cost, re-reads CLAUDE.md / hooks / MCP servers, and discards conversational context. Three goals motivate moving to a persistent interactive session model:

1. **Cost / latency** — a warm session amortizes startup and tool loading across firings.
2. **Conversational continuity** — successive firings can reference prior turns without juggling `--resume` session IDs manually.
3. **Observability** — `tmux attach` against a live session lets you watch a scheduled task run, step in mid-turn, and inspect what the model is doing.

Goal: zero `claude -p` invocations in the persistent-task flow, including failure-notification delivery.

## Architecture

Each `runtime: persistent` task runs its prompt inside a long-lived `claude` process living in a leo-supervised tmux session. Sessions are first-class config objects (new top-level `sessions:` map). The daemon (`leo service`) supervises them in parallel with `processes:`, using the same tmux naming convention (`leo-session-<name>`), restart-on-crash with exponential backoff, and `--resume <session_id>` when a prior session id is persisted.

Cron firings (`leo run <task>`) no longer spawn `claude -p`. They post to the daemon, which queues the prompt per-session and injects it into the live tmux pane via `tmux paste-buffer` + Enter. A leo-managed Stop hook in the workspace's `.claude/settings.local.json` reports each turn's completion back to the daemon, correlated to the originating invocation via a sentinel marker embedded in the prompt body.

```
cron ─▶ leo run <task> ──HTTP via Unix socket──▶ daemon /task/enqueue
                                                       │
                                                       ▼ append to per-session FIFO
                                                  session queue (in-memory)
                                                       │
                                                       ▼ (when session idle)
                                              tmux set-buffer / paste-buffer / Enter
                                                       │
                                                       │  prompt body:
                                                       │    <!-- leo:invocation=<uuid> -->
                                                       │    <user prompt>
                                                       │    ---
                                                       │    When finished, deliver your reply via channels: <list>.
                                                       ▼
                                            claude in leo-session-<name>
                                                       │   (uses loaded channel plugins
                                                       │    to deliver result — no -p)
                                                       ▼ end of turn
                                              Stop hook → leo internal task-report
                                                       │  (reads transcript, finds uuid,
                                                       │   extracts last assistant turn)
                                                       ▼
                                              daemon /task/report  ──signal──▶ pending invocation
                                                       │
                                                       ▼
                                              record history, advance queue, persist session id
```

Three topologies all flow through the same primitives:
- **A — dedicated** (default): task without `session:` gets an implicit session named after the task.
- **B — shared workspace**: task references a name from `sessions:`.
- **C — existing process**: task references `session: process:<name>`. The process's tmux session is reused; injection coexists with any human typing, handled by the marker correlation.

### Concurrency model

Per-session FIFO queue (in-memory in the daemon). Default `queue_max: 5` per task. Cron firings while a session is busy are queued in arrival order. Overflow at `queue_max` rejects the new firing immediately with a "queue full" history entry (and a `notify_on_fail` if configured). Each session has a single pump goroutine that owns the inject → wait-for-Stop-hook → advance cycle.

### Lifecycle

- `runtime` enum: `oneshot` (default — today's `claude -p` flow, unchanged) or `persistent`.
- Default for persistent sessions: **always-on**. Boot with `leo service`, supervised, resumed on crash via stored session id.
- Opt-in **lazy** mode per task (`lazy: true`): session is spun up on first enqueue and torn down after configurable idle (`idle_timeout`, default 1h). For rarely-fired tasks where cold start is acceptable but `oneshot` isn't.

## Config Schema

New top-level block:

```yaml
sessions:                          # NEW. Reusable persistent claude sessions.
  daily:
    workspace: ~/work/daily        # required
    model: sonnet                  # falls back to defaults.model
    agent: orchestrator            # optional
    permission_mode: acceptEdits   # optional, falls back to defaults
    allowed_tools: [...]           # optional
    disallowed_tools: [...]        # optional
    append_system_prompt: ""       # optional
    add_dirs: []                   # optional extra read-only dirs
    channels: [plugin:slack@official, plugin:telegram@official]
                                   # plugins loaded at session start (LEO_CHANNELS env);
                                   # tasks may use any subset.
    env: {...}                     # optional
    idle_timeout: "1h"             # optional; only meaningful for lazy tasks pointing here
```

Task additions:

```yaml
tasks:
  standup:
    runtime: persistent            # NEW. enum: oneshot (default) | persistent
    session: daily                 # NEW. omitted = dedicated (topology A);
                                   # "<name>" = sessions.<name> (B);
                                   # "process:<name>" = processes.<name> (C)
    lazy: false                    # NEW. opt-in; default false (always-on)
    schedule: "0 7 * * *"
    prompt_file: prompts/standup.md
    channels: [plugin:slack@official]   # MUST be subset of session.channels
    timeout: 10m
    queue_max: 5                   # NEW. per-task queue depth; default 5
```

Go types added to `internal/config/config.go`:

```go
type SessionConfig struct {
    Workspace, Model, Agent, PermissionMode, AppendSystemPrompt string
    AllowedTools, DisallowedTools, AddDirs, Channels []string
    Env         map[string]string
    IdleTimeout string  // duration string; parsed at use
}

type TaskConfig struct {
    // existing fields preserved...
    Runtime  string `yaml:"runtime,omitempty"`   // "oneshot" | "persistent"
    Session  string `yaml:"session,omitempty"`
    Lazy     bool   `yaml:"lazy,omitempty"`
    QueueMax int    `yaml:"queue_max,omitempty"` // 0 → default 5
}

type Config struct {
    // existing fields preserved...
    Sessions map[string]SessionConfig `yaml:"sessions,omitempty"`
}
```

### Validation rules (in `Config.Validate()`)

- `runtime` must be `oneshot` or `persistent`.
- `session` only valid when `runtime == "persistent"`.
- `session: "<name>"` must resolve in `sessions:`. `session: "process:<name>"` must resolve in `processes:`. Otherwise error.
- For topology A (no `session:`): an implicit `SessionConfig` is materialized from the task — `workspace`, `model`, `permission_mode`, `allowed_tools`, `disallowed_tools`, `append_system_prompt`, `channels`, `env` inherit from the task itself. The implicit session is named after the task; a name collision with an explicit `sessions:` entry is a load-time error.
- For topology B/C: `task.channels` must be a subset of the resolved session's `channels`. Mismatch → error with actionable message ("add `<channel>` to sessions.<name>.channels or change task.channels").
- For topology C: `task.workspace`, if set, must equal the process's `workspace` (we don't shadow workspaces).
- Empty `task.channels` is valid: the model runs the prompt with no delivery footer; result lives only in history. Same as today's `oneshot` with no channels.

### Backwards compatibility

Every existing task without `runtime:` defaults to `oneshot`, identical to today. The `claude -p` code path is preserved unchanged.

## Components

### 1. Persistent session supervisor — `internal/service/session.go` (new)

Mirrors `internal/service/process.go`. On `RunSupervised()` boot, materializes a `SessionSpec` for:
- every entry in `sessions:`,
- every `runtime: persistent` task without `session:` (synthesized `SessionConfig`),
- excluding `session: process:<name>` (already supervised by the process loop).

Each spec runs a goroutine:

1. Call `hooks.EnsureLeoStopHook(workspace)` (Section 5).
2. Look up `session.Store.Get("session:"+sessionName)`; if present, pass `--resume <id>` to claude.
3. `tmux new-session -d -s leo-session-<name> ... <buildClaudeShellCmd(...)>`. Reuses `buildClaudeShellCmd` and the supervise/backoff loop from `process.go`, extracted into `internal/service/superviseloop.go` and shared.
4. Sets `LEO_SESSION_NAME=<name>` and `LEO_CHANNELS=<channels>` in the claude process env.
5. Restart-on-crash with the existing exponential backoff.

Lazy sessions: the supervisor registers them but doesn't boot. The enqueue handler boots on first arrival; an idle timer tears down after silence.

### 2. tmux primitives — `internal/tmux/inject.go` (new)

```go
// InjectPrompt sends body to claude in session as a single submission.
// Uses set-buffer + paste-buffer (-d deletes after paste) to avoid char-by-char
// races. Multi-line bodies preserved; Enter submits.
func InjectPrompt(ctx context.Context, tmuxPath, session, body string) error

// AbortPrompt aborts a mid-turn claude by sending Escape then Ctrl-C.
// Used on timeout/abort to free the session for the next queued prompt.
func AbortPrompt(ctx context.Context, tmuxPath, session string) error
```

### 3. Hook installer — `internal/hooks/install.go` (new)

```go
// EnsureLeoStopHook idempotently writes the leo-managed Stop hook into
// <workspace>/.claude/settings.local.json. Preserves any non-leo entries.
// Atomic write via os.Rename. Malformed JSON → error (do not clobber).
func EnsureLeoStopHook(workspace string) error
```

Reads `settings.local.json` (or `{}` if absent), navigates `hooks.Stop`, removes any element with `_leo_managed == "task-report"`, appends:

```json
{
  "_leo_managed": "task-report",
  "command": "leo internal task-report"
}
```

Called by every supervised-session goroutine before launching claude.

### 4. Per-session queue + pump — `internal/daemon/session_router.go` (new)

```go
type sessionRouter struct {
    mu     sync.Mutex
    queues map[string]*sessionQueue   // sessionName → queue
}

type sessionQueue struct {
    mu       sync.Mutex
    fifo     []pendingInvocation
    inFlight *pendingInvocation
    notify   chan struct{}            // pump signal
}

type pendingInvocation struct {
    id        string                  // UUID; embedded as <!-- leo:invocation=ID -->
    task      string
    prompt    string                  // assembled, marker + body + delivery footer
    channels  []string                // task.channels at enqueue time
    timeout   time.Duration
    enqueued  time.Time
    resultCh  chan invocationResult   // signaled by /task/report or timeout
}

type invocationResult struct {
    ok           bool
    sessionID    string
    finalMessage string
    err          string                // "timeout" | "queue full" | etc.
}
```

Pump goroutine per session: waits for `notify` or in-flight completion; pops next; sets `inFlight`; calls `tmux.InjectPrompt`. Starts a deadline timer; on expiry calls `tmux.AbortPrompt` and signals `resultCh` with `{ok: false, err: "timeout"}`. `inFlight` is cleared by `/task/report` or the deadline path.

### 5. Daemon endpoints — `internal/daemon/server.go` (modified)

- `POST /task/enqueue` — body `{task, prompt_body, channels, timeout_seconds, session}`. Server-side wraps `prompt_body` with the marker + delivery footer to form the final prompt. Returns `{accepted, invocation_id}` immediately or `{accepted: false, reason: "queue full"}`.
- `GET /task/await?invocation_id=...` — long-polls until the invocation completes or the request context cancels. Returns `{ok, session_id, final_message, error}`.
- `POST /task/report` — called by the Stop hook subcommand. Body `{invocation_id, session_id, final_message, session_name}`. Correlates to a pending invocation:
  - If `invocation_id` matches `inFlight`: signal `resultCh` with `{ok: true, ...}`, clear `inFlight`, trigger pump.
  - If `invocation_id` matches a recently-completed-but-already-timed-out invocation: discard, return 200.
  - If `invocation_id` is empty / unknown (e.g. a human-typed turn): return 200 no-op.

Client helpers in `internal/daemon/client.go`: `EnqueueTask`, `AwaitTask`, `ReportTask`.

### 6. Hidden CLI — `internal/cli/internal_task_report.go` (new)

`leo internal task-report` (parent `internal` is `Hidden: true`):

1. Read Claude Code's hook JSON envelope from stdin (fields: `session_id`, `transcript_path`, `hook_event_name`, `cwd`).
2. Open `transcript_path`, scan from the end for the most recent `type:"user"` line, extract `<!-- leo:invocation=([0-9a-f-]{36}) -->`.
3. If no marker, exit 0 (a human-driven turn).
4. From that user turn, walk forward to the next top-level assistant message; concatenate text content blocks → `final_message`.
5. POST to daemon `/task/report` with `{invocation_id, session_id, final_message, session_name: $LEO_SESSION_NAME}`.
6. Always exit 0 (hook failures must never block claude). Errors → stderr.

### 7. Persistent runner — `internal/run/persistent.go` (new)

`runner.Run()` branches at the top:

```go
if strings.EqualFold(task.Runtime, "persistent") {
    return runPersistent(ctx, cfg, taskName, task)
}
```

`runPersistent()`:
1. Build prompt body via existing `buildPrompt(task)`.
2. `client.EnqueueTask(ctx, ...)`. If `accepted == false`, record history failure (`reason: "queue full"`) and, if `notify_on_fail`, enqueue a follow-up notify prompt; return.
3. `client.AwaitTask(ctx, invocation_id)` (long-poll, respects `task.timeout`).
4. On success: `history.Record(taskName, 0, "completed", finalMessage)`; persist `session.Store.Set("session:<sessionName>", session_id)`. Note: this key is scoped by *session* name, not task name. Multiple tasks sharing a session share one session id. The existing `task:<taskName>` key (used by `oneshot`) is independent; switching a task from `oneshot` to `persistent` orphans the old `task:<taskName>` entry. That's intentional — `oneshot` and `persistent` are different lifecycles and shouldn't cross-resume.
5. On failure: `history.Record(taskName, 1, reason, "")`; if `notify_on_fail`, enqueue an in-session follow-up prompt: `"The previous task failed: <reason>. Send a brief failure notice via channels: <channels>."` — no `-p`.

### 8. Channel delivery (in-session, no `-p`)

When `task.channels` is non-empty, the daemon appends a deterministic footer to the prompt body:

```
<user prompt body>

---
When finished, deliver your final reply to the user via these channel plugin(s): <task.channels>.
```

When `task.channels` is empty, no footer is appended; the result is captured only in history.

The session already has the channel plugins loaded (via `LEO_CHANNELS` at session start). Plugins keep owning their credentials. The model uses the plugin tools to send. Stop hook fires when the turn ends. **No `claude -p` is invoked anywhere in this flow.**

Failure delivery (`notify_on_fail`): same path — daemon enqueues a follow-up failure-notice prompt into the same session.

## CLI Surface

```
leo session list                    # all configured sessions + state + queue depth
leo session status <name>           # detail: pid, session_id, in-flight invocation, queue
leo session attach <name>           # tmux attach (interactive — humans welcome in B/C)
leo session logs <name> [--follow]  # pane scrollback / pipe-pane stream
leo session reset <name>            # kill tmux + clear session_id; next supervisor pass
                                    # starts a fresh claude (no --resume). For context-full.
leo session drain <name>            # block until queue empty (for shutdown ordering)
```

Mirrors existing `leo agent` and `leo service` shapes. Remote dispatch via `client.hosts` works the same way.

Small additions on the task side:
- `leo task history <name>` gains a `runtime` column.
- `leo run <task>` unchanged externally; internally branches on `runtime`.

## File Map

```
NEW:
  internal/config/session.go              # SessionConfig + validation helpers
  internal/service/session.go             # session supervisor
  internal/service/superviseloop.go       # extracted restart/backoff (shared)
  internal/tmux/inject.go                 # InjectPrompt, AbortPrompt
  internal/hooks/install.go               # EnsureLeoStopHook
  internal/daemon/session_router.go       # per-session FIFO + pump
  internal/cli/internal_task_report.go    # hidden `leo internal task-report`
  internal/cli/session.go                 # `leo session` subcommands
  internal/run/persistent.go              # runPersistent() — enqueue + await

MODIFIED:
  internal/config/config.go               # Sessions map; TaskConfig.Runtime/Session/Lazy/QueueMax
  internal/cli/validate.go                # warnings for new fields
  internal/cli/root.go                    # register session + hidden internal commands
  internal/run/runner.go                  # top-of-Run() branch on Runtime
  internal/service/process.go             # call extracted superviseloop; boot sessions in RunSupervised
  internal/daemon/server.go               # /task/enqueue, /task/await, /task/report
  internal/daemon/client.go               # EnqueueTask, AwaitTask, ReportTask helpers
```

## Testing Strategy

### Unit tests (Go, table-driven, `-race`)

- `internal/config`: `runtime` validation; `session` resolution (dedicated / shared / process); channels-subset rule; dedicated-inheritance.
- `internal/tmux/inject`: arg shape verified via `execCommand` test seam — `set-buffer`, `paste-buffer -d`, `send-keys Enter`.
- `internal/hooks/install`: empty file; existing user hooks preserved; idempotent re-merge; malformed-JSON refusal.
- `internal/daemon/session_router`: FIFO ordering; in-flight isolation; queue-full rejection at `queue_max`; timeout → abort → next prompt advances.
- `internal/cli/internal_task_report`: synthetic transcript with marker; marker missing (no-op); multiple user turns (picks latest); corrupt JSONL line (skips and continues).

### Integration tests (`e2e/`, using `e2e/fakeclaude`)

Extend `e2e/fakeclaude` with an "interactive" mode: reads lines from stdin; on submission writes a JSONL transcript entry then a Stop-shaped envelope and re-enables stdin. Honors `--resume`.

- `e2e/persistent_basic_test.go`: dedicated persistent task; `leo run` → enqueue → inject → fake Stop hook → /task/report → history written → session_id persisted; second run resumes.
- `e2e/persistent_queue_test.go`: fire 3× rapidly; assert FIFO order, queue-full rejection at depth 5.
- `e2e/persistent_shared_test.go`: two tasks pointing at the same `sessions:` entry; fire both; assert sequencing through one session.
- `e2e/persistent_process_test.go`: task with `session: process:<name>`; inject a human turn between two firings; assert correlation by marker correctly attributes results.
- `e2e/hook_install_test.go`: existing `settings.local.json` with unrelated hook → merge preserves, idempotent on re-run.

### Manual smoke (PR checklist)

- Real `claude`, persistent task on 1-min cron, `tmux attach -t leo-session-<name>`: watch prompts arrive and be answered live, see history populate, see channel delivery happen.
- Trigger a failure (e.g. timeout via short `timeout:`); confirm `notify_on_fail` injects a follow-up in the same session, no `-p` spawned (verify with `ps`).

## Open Items (deferred to follow-up plans)

- Auto-compact / session rollover when context fills. v1 punts: `leo session reset <name>` is the manual lever.
- Cross-host (remote) persistent sessions. Today `client.hosts` makes `leo agent` dispatch to remote daemons; the same shape applies but is out of v1 scope.
- Per-firing channel override beyond task config (e.g. a one-off `leo run <task> --channel <x>`). Not in v1.
