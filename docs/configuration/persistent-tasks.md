# Persistent Task Sessions

Tasks default to `runtime: oneshot`, which runs `claude -p <prompt>` as a fresh subprocess for each cron firing. Setting `runtime: persistent` reuses a warm session living in a leo-supervised tmux session.

This page describes the mechanics for the default `claude` harness (Stop hook, invocation marker, async pump-and-await). `codex` and `opencode` persistent sessions use the same `sessions:`/`tasks:` config shape but a different completion path — see [Harness notes](#harness-notes) below and [Harnesses → Session driver semantics](harnesses.md#session-driver-semantics) for the full driver reference.

## Why

- Skip claude startup cost on every firing.
- Carry conversational context across firings without juggling `--resume` ids.
- `tmux attach` to watch a scheduled task run live.

## Quickstart — dedicated session per task

```yaml
tasks:
  morning:
    runtime: persistent
    schedule: "0 7 * * *"
    prompt_file: prompts/morning.md
    workspace: ~/work/morning
    channels: [plugin:slack@official]
```

That config implicitly creates a dedicated session named `leo-session-morning`. Leo:

- Starts a long-running `claude` inside the tmux session at `leo service` boot.
- Resumes (`--resume <id>`) on crash using the persisted session id.
- Writes a leo-managed Stop hook into `~/work/morning/.claude/settings.local.json`.
- On each cron firing, pastes the prompt into the live session and waits for the hook to report completion.

## Sharing a session across multiple tasks

Declare a session explicitly under `sessions:` and reference it from tasks:

```yaml
sessions:
  daily:
    workspace: ~/work/daily
    model: sonnet
    channels: [plugin:slack@official, plugin:telegram@official]

tasks:
  standup:
    runtime: persistent
    session: daily
    schedule: "0 7 * * *"
    prompt_file: prompts/standup.md
    channels: [plugin:slack@official]      # must be subset of session.channels
  summary:
    runtime: persistent
    session: daily
    schedule: "0 18 * * *"
    prompt_file: prompts/summary.md
    channels: [plugin:telegram@official]
```

Tasks share the same `claude` process and channel plugins. Each firing's prompt is queued per-session (FIFO), so they execute in arrival order.

## Channel delivery

Persistent tasks reuse the channel plugins loaded by the session at boot (via `LEO_CHANNELS`). The injected prompt ends with a footer instructing the model to deliver its reply via the task's channel list. No `claude -p` is invoked for delivery.

If a task has `notify_on_fail: true` and fails (timeout, queue full, etc.), leo enqueues a follow-up failure-notice prompt into the same session — again, no `claude -p`.

## Configuration reference

### `sessions:` (top-level map)

| Field                  | Type            | Notes                                                          |
| ---------------------- | --------------- | -------------------------------------------------------------- |
| `workspace`            | path            | Required. Where the session's `.claude/` lives.                |
| `model`                | string          | `sonnet` / `opus` / `haiku`, validated by the resolved harness. |
| `harness`              | string          | Adapter override for this session; cascades from `defaults.harness`. See [Harnesses](harnesses.md). |
| `harness_options`      | map             | Adapter-specific options — for `claude`: `agent`, `permission_mode`, `allowed_tools`, `disallowed_tools`, `append_system_prompt`. **Sessions never inherit `defaults.harness_options`** — set every option you want directly here. See [Harnesses](harnesses.md). |
| `add_dirs`             | list            | Extra `--add-dir` paths.                                       |
| `channels`             | list            | Channel plugins loaded for the session.                        |
| `env`                  | map[str]str     | Extra env vars passed to claude — including `ANTHROPIC_BASE_URL`/`ANTHROPIC_AUTH_TOKEN` for a third-party endpoint; see [Harnesses → providers is gone](harnesses.md#providers-is-gone). |
| `idle_timeout`         | duration string | Reserved for future lazy-session support.                      |

### New `tasks.<name>` fields

| Field      | Type   | Notes                                                                                  |
| ---------- | ------ | -------------------------------------------------------------------------------------- |
| `runtime`  | enum   | `oneshot` (default) or `persistent`.                                                   |
| `session`  | string | `<name>` references `sessions:`. Optional — omit for an implicit dedicated session. |
| `lazy`     | bool   | Parsed but not yet honored; always-on for now.                                         |
| `queue_max`| int    | Max queued firings per session (default 5; overflow rejected with "queue full").       |

Task `channels:` must be a subset of the resolved session's `channels:`. Validation enforces this at config load.

## Operator commands

```
leo session list                  # configured sessions with workspace/model/channels
leo session status <name>         # stored session id + tmux liveness
leo session attach <name>         # tmux attach (interactive)
leo session logs <name>           # capture last 200 lines of pane scrollback
leo session reset <name>          # kill tmux + clear stored id (use when context fills)
leo session drain <name>          # placeholder; not yet implemented
```

## How it works

1. `leo service` boots a goroutine per `sessions:` entry (and per implicit dedicated session) that runs `claude` inside `leo-session-<name>` tmux, with restart-on-crash and `--resume` of the persisted session id.
2. Each session's workspace gets a leo-managed Stop hook merged into `.claude/settings.local.json`. The hook runs `leo internal task-report` when a turn ends.
3. `leo run <task>` for a `runtime: persistent` task POSTs to the daemon (`/task/enqueue`) with a prompt wrapped in:
    - a sentinel marker: `<!-- leo:invocation=<uuid> -->`
    - a delivery footer naming the task's `channels:`
4. The daemon's per-session pump goroutine pastes the prompt via `tmux paste-buffer` then `send-keys Enter`.
5. When the turn ends, the Stop hook fires `leo internal task-report`, which reads the transcript JSONL, finds the marker, extracts the assistant reply, and POSTs to `/task/report`.
6. The pump correlates the report to the in-flight invocation, signals the result channel, and the `leo run` subprocess returns. History is recorded; the session id is persisted for next-boot resume.

## Harness notes

Steps 1–6 above ("How it works") describe the `claude` path exactly: a
resident tmux-hosted TUI, a Stop hook merged into `.claude/settings.local.json`,
and an async pump that pastes the prompt, waits for the hook's
`leo internal task-report` POST (correlated via the `<!-- leo:invocation=<uuid>
--> ` sentinel), and only then returns.

`codex` and `opencode` sessions use the same `sessions:`/`tasks:` config
shape, cron firing, and daemon enqueue path, and drive a resident TUI in
tmux exactly like claude (see
[Harnesses → Session driver semantics](harnesses.md#session-driver-semantics)),
but completion detection is different:

- **No Stop hook, no invocation marker.** Both are claude-specific machinery
  (a `.claude/settings.local.json` hook and a transcript-JSONL sentinel) that
  don't exist for either non-claude harness. Nothing is written into the
  session workspace to detect turn completion.
- **Delivery is fire-and-forget, same as claude.** The daemon pastes the
  prompt into the session's tmux pane and the injector call returns almost
  immediately — it does not wait for the turn to finish.
- **Known limitation: no genuine turn-done signal for non-claude sessions.**
  Because there's no Stop hook (or any TUI event leo currently listens for)
  to report completion, a codex/opencode persistent-task invocation
  routed through this same pump falls through to the pump's outer timeout
  and completes via the timer (abort + `"timeout"` result) rather than a
  real signal that the turn actually finished. In practice this means every
  firing of a persistent `codex`/`opencode` session — even one where the
  turn actually succeeded — is currently recorded as a failed run
  (`"task: timeout"`, `handlePersistentFailure` in `internal/run/persistent.go`),
  which triggers a false `notify_on_fail` alarm if configured, and the pump
  sends its abort sequence (Escape, then Ctrl-C) into the live tmux pane on
  every one of these timeouts (see the `KNOWN LIMITATION` comments in
  `internal/daemon/session_router.go`). This does not affect ephemeral
  agents, which deliver messages directly and don't go through this
  completion-tracking pump, and does not affect `claude`, which has a real
  Stop-hook signal. A TUI-native turn-completion signal for non-claude
  sessions is a deferred follow-up, not yet implemented.
- **`codex` has no resident session id until after the first turn.** Each
  firing targets the resident TUI (fresh on first launch, `codex resume
  <session-id>` afterward); leo discovers the session id post-hoc by
  scanning rollout files and records it for the next firing.
- **`opencode` discovers its session id the same way**, via `opencode
  session list` after the first turn, and resumes with `-s <session-id>`.

See [Harnesses → Session driver semantics](harnesses.md#session-driver-semantics)
for the exact argv and attach behavior of each driver.

## Known limitations (v1)

- `lazy` sessions are parsed but always-on; `idle_timeout` is reserved.
- `leo session drain` is a stub.
- Context-fill recovery is manual via `leo session reset <name>` (no auto-compaction).
- No remote-daemon (`client.hosts`) dispatch for `leo session` subcommands yet.
- End-to-end integration tests are not yet in place; the unit tests cover the daemon router, runner branch, hook installer, tmux primitives, config validation, and the hidden CLI exhaustively.
