# Interactive dispatch: subagents you can watch, steer, and message

Extends `docs/specs/2026-09-13-subagent-dispatch.md`. Supersedes the
"dispatch takeover" idea: a resumed TUI beside a headless run cannot see or
steer it, so the process in the pane must be the process doing the work.

## Goal

Make `leo_dispatch` comparable to Claude Code's native subagents: the
orchestrator starts a subagent, gets its result, and can send it follow-up
messages; the user can watch it work in a tmux window and type into it at any
time. The subagent is a real harness TUI (codex or claude) running in a window
of the caller's own tmux session. Completion is detected through the harness's
own `Stop` hook, not by parsing screens or private rollout files.

## Non-goals

- Replacing headless mode. `mode: headless` keeps today's `exec --json` path
  (and remains what `leo_consult` uses). Scripts, CI, and consults want a
  process that exits.
- Supervision. An interactive dispatch is not an ephemeral agent: no restart
  loop, no agentstore entry, no `leo agent` listing. If the TUI dies, the run
  fails.
- OpenCode. Its TUI has no Stop hook yet; interactive mode on an opencode
  template is a validation error.
- Cross-host dispatch.

## Design

### Modes

`leo_dispatch` gains `mode`: `interactive` (default) or `headless`. Headless
behaves exactly as shipped in v0.23.0. Everything below is interactive mode.

### Launch

The daemon builds an interactive launch spec from the template, as for an
ephemeral agent: `Kind: KindAgent`, the template's model, `harness_options`,
`env`, cwd = the dispatch cwd. It starts the harness in a **new window** of the
caller's tmux session (`leo-<caller>`), or of `leo-dispatch` for non-agent
callers, named `<label>·<hex4>` as today:

```
tmux new-window -d -P -F '#{window_id}' -t <session> -n <label>·<hex4> -c <cwd> [-e K=V…] <shell-quoted harness command>
```

The window id is persisted on the record as today. The launch is the only
place harness argv is assembled; it reuses the adapters' `Args`/`Env`.

Two per-launch additions to the harness argv, each behind a new adapter
method `TurnHooks(reportCmd []string) (args []string, err error)`:

- codex: a `Stop` hook in the session-flags config layer pointing at the
  report command, with hook trust satisfied for that launch without touching
  the user's `hooks.json` or trust state. The exact override key and trust
  mechanism are verified live before merge (codex ≥ 0.153, `hooks` feature
  stable).
- claude: a `hooks.Stop` entry merged into the existing `--settings` JSON.

`reportCmd` is `leo --config <path> dispatch report <id>`.

### Opening prompt

Injected with the shared readiness-probed injector (`internal/tmux/inject.go`)
targeting `session:window_id`, the same path `leo_send_message` uses for a
codex agent. The injector gains a target form that takes a resolved tmux target
instead of an agent name; no new probing logic. Startup dialogs are handled by
the existing supervisor heuristics; codex's update prompt is suppressed by
config where the key exists.

### Turns and completion

A run is a sequence of turns. Each injected message (opening prompt,
orchestrator follow-up, or a human typing in the pane) starts a turn; each
harness `Stop` event ends one.

`leo dispatch report <id>` reads the hook payload from stdin and posts it to
`POST /api/dispatch/{id}/report {session_id, last_assistant_message, turn_id,
cwd}`. The daemon records the session id, appends a turn
`{n, ended_at, text, source}` to the record, sets `text` to that turn's text,
and moves status `running → done`. Source is `orchestrator` when the turn was
started by a leo injection and `user` otherwise.

Status meanings in interactive mode:

- `queued`: waiting for a concurrency slot.
- `running`: a turn is in progress.
- `done`: the last turn finished; the session is alive and idle.
- `closed`: the TUI exited (pane dead) after at least one completed turn.
- `failed`: the TUI exited before completing a turn, or the launch/injection
  failed.
- `canceled`, `timeout`: as today; both close the session.

`leo_wait` is unchanged: it returns when every id is not `running`. Waiting on
a `done` id returns the latest turn's text immediately.

### Follow-up messages

New MCP tool `leo_send_dispatch {id, message}` (and `POST
/api/dispatch/{id}/send`, `leo dispatch send <id> <message>`). It injects the
message into the subagent's window through the same injector (claude: inbox
socket by pane pid first, paste as fallback, mirroring `leo_send_message`),
marks the run `running` with a new orchestrator-sourced turn, and returns
immediately. The orchestrator then `leo_wait`s. Sending to a run that is
`running` is rejected (`turn in progress`); the orchestrator waits first.
Sending to `closed`/`failed`/`canceled` is rejected.

### Human steering

The user can type into the pane whenever they like. Their turn ends with a
`Stop` like any other; the record gains a `user`-sourced turn and `text`
updates. If the orchestrator is waiting during a user turn, that wait returns
the user turn's text; it is the orchestrator's prompt-writing problem to cope,
exactly as with a human answering in a Claude Code subagent. Runs with any
user-sourced turn show `steered: true` in `leo dispatch list/show`.

### Concurrency

`maxConcurrent` (6) counts runs whose status is `running`. An idle interactive
session holds no slot.

### Window lifecycle

Interactive windows never close on collection. A session closes when:

- `leo_cancel` is called (TUI killed, status `canceled`);
- the user exits the TUI (pane dead → `closed`);
- it has been `done` with no new turn for `idle_close_after` (default 1h),
  after which the daemon kills the TUI and marks it `closed`.

Dead panes are swept after the existing grace period. Headless windows keep
today's close-on-collection.

### Records and CLI

`Record` gains `mode`, `session_id`, `turns`, `steered`. `leo dispatch watch`
on an interactive run prints turns as they complete (from the ndjson stream)
instead of the exec event stream; `leo dispatch list/show` show mode, turn
count, and steered. `leo_dispatch`'s return text names the window so the
orchestrator can tell the user where to look.

## Error handling

- Interactive mode on a harness without `TurnHooks`: validation error at
  dispatch.
- Readiness probe never sees the prompt box (dialog, crash): run `failed`
  with the injector's error; window kept for post-mortem under the usual grace.
- `Stop` arrives for an unknown or non-running id: logged and ignored (a user
  turn after `closed` cannot happen; a duplicate report is a no-op).
- Report command cannot reach the daemon: the hook exits non-zero; codex and
  claude both continue the session, so the turn is "lost" to leo. The daemon
  also treats pane death as terminal, so nothing hangs forever. `leo_wait`'s
  per-call ceiling still bounds the orchestrator.
- Injection while the pane is busy (harness ignores paste): the injector's
  existing confirm-body-appeared check fails → `send` returns an error, status
  unchanged.

## Testing

- Adapter `TurnHooks` argv for codex and claude asserted exactly; a launch spec
  for interactive mode produces `new-window` with the persisted window id.
- Report endpoint: appends turns, sets text, transitions `running → done`,
  records `source`, ignores unknown/duplicate reports.
- Send: rejects `running`/terminal; marks a new orchestrator turn; injector
  called with the window target.
- Concurrency: idle `done` sessions do not hold slots.
- Lifecycle: pane death → `closed` or `failed` by turn count; idle close after
  `idle_close_after`; cancel kills the pane.
- e2e (fake harness binary that prints a prompt box and runs the hook command
  on each input): dispatch interactive, wait returns turn 1; send, wait returns
  turn 2; typing into the pane produces a `user` turn.
- Live before merge, from this session: interactive codex dispatch on
  `codex-explorer`, watch the window, `leo_wait` returns; `leo_send_dispatch`
  a follow-up and wait; type into the pane and confirm a `user` turn; claude
  template once for the inbox path.

## Follow-ups

Inbox push of turn results to idle Claude callers; a dispatches page in the
web UI showing turns; OpenCode once it exposes a Stop-equivalent.
