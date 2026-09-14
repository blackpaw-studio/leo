# Interactive dispatch: subagents you can watch, steer, and message

Extends `docs/specs/2026-09-13-subagent-dispatch.md` (shipped, v0.23.0).
Revision 3. Rev 2 tried to make sessions durable and deliveries exclusive;
both were the source of most review findings and neither is needed for the
goal. Rev 3 binds subagents to their caller, drops queued sends and per-run
tokens, and makes delivery honest rather than exclusive.

## Goal

Make `leo_dispatch` comparable to Claude Code's native subagents: the
orchestrator starts a subagent, gets its result, and can send follow-up
messages; the user can watch it in a tmux window and type into it. The
subagent is a real harness TUI (codex or claude) in a pane of the caller's
tmux session. Turn boundaries come from the harness's own hooks.

## Non-goals

- Replacing headless mode. It stays the default and what `leo_consult` uses.
- Supervision or durability. A subagent lives and dies with its caller's
  tmux session and with the daemon. No restart loop, no reattach, no
  agentstore entry.
- Exclusive ownership of the composer. A human and the orchestrator share
  one input box; leo avoids clobbering and reports delivery honestly.
- Queued sends, per-turn timeouts, inbox-socket delivery, OpenCode.
- Any security claim beyond today's: a subagent holds the caller-level API
  token like every leo agent and can forge reports for any run.

## Design

### Mode

`leo_dispatch` gains `mode`: `headless` (default, unchanged) or
`interactive`. Interactive on an opencode template is a validation error.

### Identity and ownership

- `pane_id` (tmux `%N`, from `new-window -P -F '#{pane_id}'`) persisted on
  the record. All targeting and cleanup use it; cleanup is `kill-pane`.
- `session_id` from the first hook report.
- Ownership: the pane is created in the caller's session (`leo-<caller>`),
  or `leo-dispatch` for non-agent callers. When that session ends for any
  reason (agent stop/restart, daemon shutdown), the pane dies with it and the
  run finalizes on the next sweep. On daemon start, every non-terminal
  interactive record is finalized: `closed` if it has a finished turn,
  `failed` otherwise, and its pane killed if still alive. Nothing is
  reattached.

### Launch

The daemon builds a `KindAgent` launch spec from the template (model,
`harness_options`, `env`, cwd = dispatch cwd) and starts it:

```
tmux new-window -d -P -F '#{pane_id}' -t <session> -n <label>·<hex4> -c <cwd> [-e K=V…] <shell-quoted harness command>
```

Argv comes from the adapters' `Args`/`Env`; every element is shell-quoted;
the leo executable path is absolute. The record (with `pane_id`) is persisted
before the opening prompt is injected; if persistence fails, the pane is
killed and the dispatch fails.

Startup dialogs are prevented, not dismissed: the adapters' existing
prelaunch config (codex trust entry, claude onboarding/trust settings) is
reused, plus the codex config key that disables the update prompt, verified
live. The readiness probe treats any remaining dialog as launch failure.

### Hooks

New adapter method `TurnHooks(reportCmd []string) (args []string, err
error)` adds per-launch hooks for four events, all running
`leo --config <path> dispatch report <id>`:

| Event | codex | claude | leo effect |
|---|---|---|---|
| prompt submitted | `user_prompt_submit` | `UserPromptSubmit` | acknowledges a pending orchestrator turn, else opens a user turn |
| turn finished | `stop` | `Stop` | closes a turn with `last_assistant_message` |
| turn aborted | `interrupt` | none | closes a turn as `interrupted` |
| session exited | `session_end` | `SessionEnd` | begins final settlement |

The report command reads the stdin payload, generates an `event_id` once,
and POSTs `{event_id, payload}` to `POST /api/dispatch/{id}/report` with the
agent API token from its environment, retrying with backoff (5 attempts,
~30 s) on transport failure. The daemon dedups on `event_id` and answers a
duplicate with 200.

Codex hook delivery: hooks in the session-flags layer via `-c` overrides,
with trust established by **one** mechanism chosen during implementation and
verified live, in this order of preference: session-layer hooks trusted
implicitly; a trust entry for leo's hook hash in codex's hook state;
`--dangerously-bypass-hook-trust` only when leo has confirmed no other hook
sources are discovered for that home and cwd. Only the chosen mechanism
ships; if none works, interactive on codex is a validation error saying why.
Claude: entries merged into the existing `--settings` JSON.

### Turns

```
{turn_id: "d-…#n", source: orchestrator|user, started_at, ended_at,
 delivered: bool, outcome: finished|interrupted|lost|stalled?, text,
 harness_turn_id}
```

Statuses: `queued`, `running` (an open turn exists), `idle` (alive, no open
turn), and the terminal, latched `closed`, `failed`, `canceled`, `timeout`.
`done` remains the headless terminal state. Terminal statuses are never
overwritten; reports for a terminal run are ignored after settlement.

Matching a `stop`/`interrupt` to a turn: by `harness_turn_id` when the
payload's `turn_id` was seen on a submit event; otherwise the oldest open
turn. How codex assigns `turn_id` to prompts submitted while a turn is
running (queued input) is verified live before merge and covered by a test;
if codex coalesces queued prompts, leo closes every open turn whose submit
preceded the `stop` with that same text.

Transitions happen under the dispatcher lock; side effects (paste, kill)
happen outside it after revalidating status.

### Opening prompt and follow-ups

One injector entrypoint, `InjectInto(paneID, classifier, text)`:

1. Open the turn `{source: orchestrator, delivered: false}` and take a
   concurrency slot. If no slot is free, fail `no capacity` and close the
   turn (no queueing).
2. Capture the pane and classify with the harness's existing passive
   classifier (the one the readiness probe uses). Require "prompt box shown,
   composer empty"; unknown layouts fail closed. Otherwise fail
   `composer busy` and close the turn without touching the pane. No probe
   keystroke, no Ctrl-U.
3. `set-buffer --`, `paste-buffer -d`, confirm the pasted body appears,
   `Enter`. A confirm failure fails the turn `paste failed` (nothing was
   submitted).
4. Acknowledgment: a `user_prompt_submit` within `ack_timeout` (10 s) marks
   `delivered: true`. A human submission in that window is attributed to the
   pending orchestrator turn; the empty-composer check makes the race narrow
   and it is documented, not prevented. Without acknowledgment the turn stays
   open with `delivered: false`; a later `stop` still closes it. After the
   window a pending turn no longer absorbs submits.

`leo_send_dispatch {id, message}` → `{turn_id, delivered}` (also
`POST /api/dispatch/{id}/send`, `leo dispatch send`). Allowed only when the
run is `idle`; anything else is rejected with the current status. Bodies are
plain text; control characters other than newline are rejected. Both
harnesses use paste. The `#` in a turn id is URL-encoded in paths.

### Human steering

A submit with no pending orchestrator turn opens a `source: user` turn and
moves `idle → running`. User turns take no slot. The record shows
`steered: true` once any user turn exists.

### Waiting

`leo_wait` ids may be a run (`d-…`) or a turn (`d-…#n`). A run id is
resolved **once, when the wait starts**, to the run's latest orchestrator
turn, or its latest user turn if it has none; the wait then tracks that turn.
A turn id waits on that turn's outcome. Entries gain `turn_id`, `outcome`,
`delivered`, `stalled`. A `queued` run keeps the wait pending. An open turn
whose run has had no hook activity for `stalled_after` (10 min) is reported
with `stalled: true` on each wait timeout; it is not closed and its slot is
not released. The caller cancels or keeps waiting.

### Concurrency

`maxConcurrent` (6) admits orchestrator turns: the opening prompt and each
send hold a slot from step 1 until the turn closes or the run goes terminal,
released exactly once. Idle sessions and user turns hold no slot.

### Lifecycle and settlement

Settlement closes every open turn (`interrupted` for cancel, `lost` for
exit), latches the terminal status, notifies waiters, and closes the
recorder. It runs:

- on `leo_cancel`: `kill-pane`, then settle as `canceled`;
- on `session_end`, and on pane death noticed by the sweep: wait
  `final_report_grace` (15 s) for a trailing `stop`, then settle as `closed`
  if any turn ever finished, else `failed`;
- on idle close: `idle` for `idle_close_after` (1 h) with the composer empty
  at check time (a human draft defers it) → `kill-pane`, settle as `closed`;
- on session timeout (`timeout_seconds`, whole session): `kill-pane`,
  settle as `timeout`.

Interactive panes never close on collection. Records of `idle`/`running`
runs are exempt from retention pruning; dead panes are swept after the
existing grace.

### Records, stream, CLI

`Record` gains `mode`, `pane_id`, `session_id`, `turns`, `steered`. The
ndjson stream uses the existing `{t, d, raw}` envelope with `d.type =
"turn"` and a per-run sequence number, written after the record update it
describes; `leo dispatch watch` renders those events and stays open through
`idle`, exiting on a terminal status, and on start reconciles from the
persisted turns so a crash between record and stream writes loses nothing
visible. `list`/`show` display mode, status, turn count, steered.
`leo_dispatch`'s reply names the window.

## Error handling

- Interactive on a harness without `TurnHooks`, or codex trust unverified:
  validation error at dispatch.
- Readiness never reached: `failed` with the probe's error; pane kept under
  the usual grace.
- Report with unknown id, unknown `turn_id` on a close event and no open
  turn, or a settled run: logged and ignored (200 to stop retries).
- Claude has no abort hook: an interrupted claude turn shows as `stalled`
  until the next submit opens a new turn (which closes the old one as
  `lost`) or the user cancels.
- `composer busy`, `no capacity`, `paste failed`: send returns the reason;
  the run's status is unchanged.

## Testing

- `TurnHooks` argv exact for codex and claude; launch produces
  `new-window -P -F '#{pane_id}'`; record persisted before injection.
- Report endpoint: `event_id` dedup, `harness_turn_id` and oldest-open
  matching, ignored cases, terminal latch.
- `InjectInto`: composer-busy and unknown-layout rejection with zero
  keystrokes; paste argv; ack; no-ack leaves `delivered: false`; late stop
  closes it.
- Waits: run id snapshot vs turn id; queued pending; stalled reporting.
- Concurrency: slot per orchestrator turn, released once; `no capacity`
  rejects; user turns exempt.
- Settlement: cancel, `session_end` and pane death with final-report grace,
  idle close deferred by a draft, session timeout, daemon-start finalize.
- e2e with a fake harness binary (prompt box, runs the hook command per
  input): dispatch → wait; send → wait; typed input → user turn; kill pane →
  closed.
- Live before merge, from this session: codex interactive on
  `codex-explorer` (wait, send, type in the pane, cancel); claude once; codex
  queued-prompt `turn_id` behaviour; hook trust mechanism; update prompt
  suppressed.

## Follow-ups

Default to interactive once both contracts are proven; inbox-socket sends
for claude after verifying the hook fires; durable sessions if a real need
appears; dispatches page in the web UI.
