# Persistent Tasks

Tasks default to `runtime: oneshot`, which runs `claude -p <prompt>` as a fresh subprocess for each cron firing. Setting `runtime: persistent` instead delivers the task's prompt into a supervised **agent** — the same primitive `leo agent spawn` creates — rather than spawning a new process.

This page describes the mechanics for the default `claude` harness (Stop hook, invocation marker, async pump-and-await). `codex` and `opencode` persistent tasks use the same `template:`/`tasks:` config shape but a different completion path — see [Harness notes](#harness-notes) below and [Harnesses → Session driver semantics](harnesses.md#session-driver-semantics) for the full driver reference.

## Why

- Skip claude startup cost on every firing.
- Carry conversational context across firings without juggling `--resume` ids.
- `leo agent attach` to watch a scheduled task run live, in the same place you'd watch any other agent.
- Reuse an agent that's already doing other work (e.g. a coding agent that also picks up a nightly digest task).

## Quickstart — implicit target

```yaml
tasks:
  morning:
    runtime: persistent
    schedule: "0 7 * * *"
    prompt_file: prompts/morning.md
    workspace: ~/work/morning
    channels: [plugin:slack@official]
```

With no `template:` field, the target is **implicit**: an agent named `morning` (after the task), synthesized from the task's own `workspace`, `model`, `channels`, and `dev_channels`. Leo:

- Spawns the agent the first time the task fires (not at `leo service` boot — see [Ensure-exists](#ensure-exists)).
- Writes a leo-managed Stop hook into `~/work/morning/.claude/settings.local.json`.
- On each cron firing, pastes the prompt into the agent's tmux session and waits for the hook to report completion.
- Persists the discovered claude session id onto the agent record, so a restart or start rejoins the same conversation.

## Explicit binding — sharing a template across tasks

Point one or more tasks at a `templates:` entry instead:

```yaml
templates:
  daily:
    workspace: ~/work/daily
    model: sonnet
    channels: [plugin:slack@official, plugin:telegram@official]

tasks:
  standup:
    runtime: persistent
    template: daily
    schedule: "0 7 * * *"
    prompt_file: prompts/standup.md
    channels: [plugin:slack@official]      # must be a subset of templates.daily.channels
  summary:
    runtime: persistent
    template: daily
    schedule: "0 18 * * *"
    prompt_file: prompts/summary.md
    channels: [plugin:telegram@official]
```

Both tasks target the same agent, named `daily` after the template. They share the same `claude` process, channel plugins, and conversation — each firing's prompt is queued per-agent (FIFO), so they execute in arrival order. This is exactly the pattern you'd use to have a general-purpose coding agent also pick up scheduled maintenance work: point a `runtime: persistent` task at the same `template:` you spawn that agent from.

Task `channels:` must be a subset of the resolved template's `channels:` — validation enforces this at config load, but only when `template:` is set explicitly. Implicit targets have no such constraint (there's nothing to be a subset of).

## Ensure-exists

Unlike the old dedicated-session model, persistent-task agents are **not** started at `leo service` boot. Instead, each firing runs an ensure-exists step before delivery:

1. **Running** — the agent is already live: inject directly.
2. **Dormant (stopped)** — the agent has a persisted-but-stopped record, whether from idle auto-stop or a manual `leo agent stop` (see [Idle-suspend](#idle-suspend-interaction)): start it, rejoining the prior conversation, then inject.
3. **Missing** — no record at all: spawn it fresh from the resolved template (explicit or synthesized-implicit), then inject.

A spawn or start failure fails the task the same way an injection failure does — `notify_on_fail` fires and the run is recorded as a failure in history. **Caveat:** if the ensure step itself failed (the agent couldn't be spawned or started at all), the follow-up failure notice can't be delivered *into* that same agent either — it goes through the identical ensure-exists path and will fail the same way. The failure is still recorded in `leo task logs`/history; it just won't reach you via the task's channels in that specific case. A subsequent successful firing (once whatever broke the spawn is fixed) resumes normal delivery, including any queued notices.

## Idle-suspend interaction

Persistent-task agents are ordinary agents, so [idle-suspend](config-reference.md#idle-suspend) applies to them the same way it applies to anything spawned via `leo agent spawn`: set `idle_suspend_after` on the template (explicit binding) or on the task itself (implicit binding, since it synthesizes its own template) and the agent goes dormant — process and tmux killed, record and conversation preserved — after a period of no activity.

This composes cleanly with scheduling: a daily task's agent can go dormant between firings and the next firing's ensure-exists step starts it automatically, because idle auto-stop marks it to auto-wake on the next message (unlike a manual `leo agent stop`, which does not). There's no need to keep an agent warm 24/7 just because a task targets it once a day.

An agent with a client attached (`leo agent attach`) is never idle-stopped, matching normal idle-suspend behavior.

## Queueing

```yaml
tasks:
  standup:
    runtime: persistent
    template: daily
    queue_max: 5   # default; 0 also means "use the default"
```

Each agent has its own FIFO queue. `queue_max` (default 5) caps how many pending firings can queue up if the agent is slow to finish a turn; once at capacity, a new firing is rejected outright (`"queue full"`) rather than blocking. This is a per-agent limit — multiple tasks sharing one `template:` share the same queue and the same cap.

## Completion reporting

**`claude`:** a leo-managed Stop hook, merged into the workspace's `.claude/settings.local.json`, reports back through `leo internal task-report` when a turn ends — a real, positive completion signal (see [How it works](#how-it-works)).

**`codex` / `opencode`:** there is currently no equivalent turn-done signal. Completion is inferred from a timeout instead of a hook, which means every firing — even a successful one — is presently recorded as a failed run. See [Harness notes](#harness-notes) for the full explanation and its consequences for `notify_on_fail`.

## Channel delivery

Persistent tasks reuse the channel plugins loaded by the agent at spawn time (via `LEO_CHANNELS`). The injected prompt ends with a footer instructing the model to deliver its reply via the task's channel list. No `claude -p` is invoked for delivery.

## `notify_on_fail`

If a task has `notify_on_fail: true` and fails (timeout, queue full, ensure-exists failure, etc.), leo enqueues a follow-up failure-notice prompt into the same target agent — again, no `claude -p`. See the [Ensure-exists](#ensure-exists) caveat above for the one case where this notice can't actually be delivered.

## `leo agent reset`

```bash
leo agent reset <name>
```

Kills the agent's tmux session, clears its stored claude session id, and respawns it fresh — starting a brand-new conversation rather than resuming the old one. Use this when a long-lived persistent-task agent's context has filled up or gotten stuck; there's no auto-compaction. Unlike `leo agent start`, which rejoins the prior conversation, `reset` deliberately discards it.

```bash
leo agent reset daily
```

## Configuration reference

### `tasks.<name>` fields relevant to persistent delivery

| Field       | Type   | Notes                                                                                          |
| ----------- | ------ | ------------------------------------------------------------------------------------------------ |
| `runtime`   | enum   | `oneshot` (default) or `persistent`.                                                            |
| `template`  | string | `<name>` references `templates:`. Optional — omit for an implicit agent synthesized from this task's own fields. |
| `queue_max` | int    | Max queued firings per target agent (default 5; overflow rejected with "queue full").           |

See [Config Reference → `tasks`](config-reference.md#tasks) for the full field list and [Config Reference → `templates`](config-reference.md#templates) for what an explicit binding target looks like.

## How it works

1. A `runtime: persistent` task fires (cron or `leo run <task>`). `leo run` resolves the task's target agent via `config.ResolveTaskTarget` — explicit (`template:`) or implicit (synthesized from the task) — and asks the daemon to ensure it exists (see [Ensure-exists](#ensure-exists)).
2. The target agent's workspace gets a leo-managed Stop hook merged into `.claude/settings.local.json` (if not already present). The hook runs `leo internal task-report` when a turn ends.
3. `leo run` POSTs to the daemon (`/task/enqueue`) with a prompt wrapped in:
    - a sentinel marker: `<!-- leo:invocation=<uuid> -->`
    - a delivery footer naming the task's `channels:`
4. The daemon's per-agent pump goroutine ensures the agent exists, then pastes the prompt via `tmux paste-buffer` and `send-keys Enter`.
5. When the turn ends, the Stop hook fires `leo internal task-report`, which reads the transcript JSONL, finds the marker, extracts the assistant reply, and POSTs to `/task/report`.
6. The pump correlates the report to the in-flight invocation, signals the result channel, and the `leo run` subprocess returns. History is recorded; the discovered claude session id is persisted on the agent record for the next resume.

## Harness notes

Steps 1–6 above ("How it works") describe the `claude` path exactly: a
resident tmux-hosted TUI, a Stop hook merged into `.claude/settings.local.json`,
and an async pump that pastes the prompt, waits for the hook's
`leo internal task-report` POST (correlated via the `<!-- leo:invocation=<uuid>
--> ` sentinel), and only then returns.

`codex` and `opencode` persistent tasks use the same `template:`/`tasks:`
config shape, cron firing, and daemon enqueue path, and drive a resident TUI
in tmux exactly like claude (see
[Harnesses → Session driver semantics](harnesses.md#session-driver-semantics)),
but completion detection is different:

- **No Stop hook, no invocation marker.** Both are claude-specific machinery
  (a `.claude/settings.local.json` hook and a transcript-JSONL sentinel) that
  don't exist for either non-claude harness. Nothing is written into the
  agent's workspace to detect turn completion.
- **Delivery is fire-and-forget, same as claude.** The daemon pastes the
  prompt into the agent's tmux pane and the injector call returns almost
  immediately — it does not wait for the turn to finish.
- **Known limitation: no genuine turn-done signal for non-claude agents.**
  Because there's no Stop hook (or any TUI event leo currently listens for)
  to report completion, a codex/opencode persistent-task invocation
  routed through this same pump falls through to the pump's outer timeout
  and completes via the timer (abort + `"timeout"` result) rather than a
  real signal that the turn actually finished. In practice this means every
  firing of a persistent `codex`/`opencode` task — even one where the
  turn actually succeeded — is currently recorded as a failed run
  (`"task: timeout"`, `handlePersistentFailure` in `internal/run/persistent.go`),
  which triggers a false `notify_on_fail` alarm if configured, and the pump
  sends its abort sequence (Escape, then Ctrl-C) into the live tmux pane on
  every one of these timeouts (see the `KNOWN LIMITATION` comments in
  `internal/daemon/session_router.go`). This does not affect ephemeral
  agents dispatched directly (outside the task-delivery path), which deliver
  messages and don't go through this completion-tracking pump, and does not
  affect `claude`, which has a real Stop-hook signal. A TUI-native
  turn-completion signal for non-claude agents is a deferred follow-up, not
  yet implemented.
- **`codex` has no resident session id until after the first turn.** Each
  firing targets the resident TUI (fresh on first launch, `codex resume
  <session-id>` afterward); leo discovers the session id post-hoc by
  scanning rollout files and records it for the next firing.
- **`opencode` discovers its session id the same way**, via `opencode
  session list` after the first turn, and resumes with `-s <session-id>`.

See [Harnesses → Session driver semantics](harnesses.md#session-driver-semantics)
for the exact argv and attach behavior of each driver.

## Known limitations

- `codex`/`opencode` persistent tasks are recorded as failed on every firing due to the timeout-based completion detection described above — see [Harness notes](#harness-notes).
- Context-fill recovery is manual via `leo agent reset <name>` (no auto-compaction).
