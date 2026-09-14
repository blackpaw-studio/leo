# Interactive dispatch: subagents you can watch, steer, and message

Extends `docs/specs/2026-09-13-subagent-dispatch.md` (shipped, v0.23.0).
Revision 2 after design review: adds turn identity, an `idle` state distinct
from terminal `done`, composer ownership, per-run report tokens, restart
reattachment, and lost-hook recovery.

## Goal

Make `leo_dispatch` comparable to Claude Code's native subagents: the
orchestrator starts a subagent, gets its result, and can send follow-up
messages; the user can watch it in a tmux window and type into it. The
subagent is a real harness TUI (codex or claude) in a pane of the caller's
own tmux session. Turn completion comes from the harness's own hooks, never
from screen scraping or private rollout files.

## Non-goals

- Replacing headless mode. It stays the default and what `leo_consult` uses.
- Supervision: no restart loop, no agentstore entry, no `leo agent` listing.
- OpenCode (no hooks). Interactive on an opencode template is a validation
  error.
- Cross-host dispatch. Per-turn timeouts (session timeout only).

## Design

### Mode

`leo_dispatch` gains `mode`: `headless` (default, unchanged) or
`interactive`. Switching the default is a later decision, after both harness
contracts have passed live tests.

### Identity

Every interactive run has:

- `pane_id` (tmux `%N`, from `new-window -P -F '#{pane_id}'`). All
  targeting, probing, and cleanup use the pane id, never the window id or
  name. Cleanup is `kill-pane`.
- `generation`: incremented on launch and on reattach after a daemon
  restart. Hook reports carry it; reports from an older generation are ignored.
- `report_token`: random per run, stored on the record, exported to the
  harness process as `LEO_DISPATCH_REPORT_TOKEN`. `POST
  /api/dispatch/{id}/report` accepts only that run's token (or the API token).
  Anything inside the subagent's sandbox can therefore forge reports for its
  own run and nothing else.
- `session_id`: the harness session, learned from the first hook.

### Launch

The daemon builds a `KindAgent` launch spec from the template (model,
`harness_options`, `env`, cwd = dispatch cwd) and starts it in a new window
of the caller's tmux session (`leo-<caller>`), or `leo-dispatch` for
non-agent callers, named `<label>·<hex4>`:

```
tmux new-window -d -P -F '#{pane_id}' -t <session> -n <label>·<hex4> -c <cwd> [-e K=V…] <shell-quoted harness command>
```

Argv comes from the adapters' `Args`/`Env`; each launcher and hook argv
element is shell-quoted; the leo executable path is absolute.

Startup dialogs are **prevented, not dismissed**: the supervisor's
Escape-based heuristics skip attached sessions, and these windows are usually
attached. The adapters' existing prelaunch config (codex trust entry, claude
onboarding/trust settings) is reused, plus whatever config key disables the
codex update prompt (verified live; if none exists, the readiness probe treats
the prompt as launch failure).

### Hooks

New adapter method `TurnHooks(reportCmd []string) (args []string, err
error)` adds per-launch hooks for four events, all running
`leo --config <path> dispatch report <id> --generation <n>`, which forwards
the stdin payload verbatim with the report token:

| Event | codex | claude | leo effect |
|---|---|---|---|
| prompt submitted | `user_prompt_submit` | `UserPromptSubmit` | opens a turn (or acknowledges a pending one) |
| turn finished | `stop` | `Stop` | closes the oldest open turn with `last_assistant_message` |
| turn aborted | `interrupt` | none | closes the oldest open turn as `interrupted` |
| session exited | `session_end` | `SessionEnd` | `closed` |

Hook config delivery, codex: hooks in the session-flags layer via `-c`
overrides. Trust must be pinned to one verified mechanism before merge, in
this order of preference: (a) session-layer hooks are trusted implicitly;
(b) leo records a trust entry for its own hook's hash in codex's hook state;
(c) `--dangerously-bypass-hook-trust`, allowed only after leo confirms that no
other hook sources (user, project, managed) are discovered for that home and
cwd, since the flag would trust them too. If none can be made to work,
interactive mode on codex ships disabled with a validation error saying why.
Claude: entries merged into the existing `--settings` JSON.

The report command retries the POST with backoff (5 attempts, ~30 s total)
before exiting non-zero.

### Turns

A run holds an ordered list of turns. Invariant: the harness executes turns
serially, so leo closes the **oldest open turn** on `stop`/`interrupt`; when
the payload carries a `turn_id` (codex), that id is recorded on the turn and
used to match instead. Turn:

```
{n, source: orchestrator|user, started_at, ended_at, delivered: bool,
 outcome: finished|interrupted|lost|timeout, text, harness_turn_id}
```

Statuses (interactive): `queued`, `running` (an open turn exists), `idle`
(alive, no open turn), and the terminal, latched `closed`, `failed`,
`canceled`, `timeout`. `done` remains the headless terminal state. A terminal
status is never overwritten; reports for a terminal run or a stale generation
are logged and ignored.

Transitions are serialized under the dispatcher lock. Idle-close timers carry
the generation and turn count they were armed with and no-op if either moved.

### Opening prompt and follow-ups

Orchestrator input goes through one new injector entrypoint,
`InjectInto(paneID, marker, text)`:

1. Capture the pane. Require the harness prompt marker on the last line and
   an empty composer; otherwise fail `composer busy` without touching the
   pane. No probe keystroke, no Ctrl-U: a human's draft is never modified.
2. `set-buffer --` / `paste-buffer -d` / `Enter`, as the existing injector.
3. Open a turn `{source: orchestrator, delivered: false}` **before** step 2,
   so a hook can never arrive for a turn that does not exist.
4. Acknowledgment: the next `user_prompt_submit` within `ack_timeout` (10 s)
   marks the turn `delivered: true`. A human submission inside that window is
   attributed to the pending orchestrator turn; the composer-empty check makes
   this a narrow race and it is documented as such. No acknowledgment: the
   turn stays open with `delivered: false` (a late `stop` still closes it) and
   the caller is told delivery is uncertain.

`leo_send_dispatch {id, message}` → `{id, turn: n, delivered}`. Allowed only
when the run is `idle`; `running`, `queued`, and terminal runs are rejected
with the status in the error. `POST /api/dispatch/{id}/send` and
`leo dispatch send <id> <message>` mirror it. Message bodies never pass
through a shell; control characters other than newline are rejected.

Claude sends use the inbox socket first (resolved from the pane's process
tree, sharing `peerinbox`'s resolution), then paste. Whether an inbox message
fires `UserPromptSubmit` is verified live before merge; if it does not, claude
sends use paste only.

### Human steering

A `user_prompt_submit` with no pending orchestrator turn opens a
`source: user` turn and moves `idle → running`. It holds no concurrency slot.
The record shows `steered: true` once any user turn exists.

### Waiting

`leo_wait` ids may be a run (`d-…`) or a turn (`d-…#3`, as returned by
send). A run id waits on its latest orchestrator turn; a turn id waits on that
turn's immutable outcome, so later human turns cannot hide it. Entries gain
`turn`, `outcome`, and `delivered`. A `queued` run keeps the wait pending, as
headless does today. Waiting on a run with no open orchestrator turn returns
its latest turn immediately; on a run with only user turns, the latest user
turn.

### Concurrency

`maxConcurrent` (6) admits **orchestrator turns**: the opening prompt and
each send take a slot from admission until the turn closes or the run goes
terminal, released exactly once. Idle sessions and user turns hold no slot.
A send that cannot get a slot returns the turn as `queued`; injection happens
when admitted.

### Lifecycle

- `leo_cancel`: `kill-pane`, status `canceled`, open turns closed `interrupted`.
- `session_end`: `closed` (or `failed` if no turn ever finished). Pane death
  observed by the sweep without a `session_end` means the same; the sweep
  waits `final_report_grace` (15 s) after noticing a dead pane before
  classifying, so a final `stop` racing exit is not lost.
- Idle close: `idle` with no new turn for `idle_close_after` (1 h) →
  `kill-pane`, `closed`.
- Session timeout: `timeout_seconds` on the dispatch caps the whole session
  as today (`timeout`, latched).
- Daemon restart: interactive records that are `idle` or `running` are
  reattached: if the pane is alive, generation is bumped, the record kept,
  and any open turn left open for the stuck sweep; if the pane is dead, the
  record is finalized as above. Reports from the old generation are ignored.
- Stuck sweep: a `running` run with no hook activity for `stuck_after`
  (10 min) has its pane captured; if the idle prompt marker is present with an
  empty composer, its oldest open turn closes with outcome `lost` (empty
  text). Otherwise it is left alone.

Interactive windows never close on collection. Dead panes are swept after the
existing grace period. Records of `idle`/`running` runs are exempt from
retention pruning.

### Records, stream, CLI

`Record` gains `mode`, `pane_id`, `generation`, `session_id`, `turns`,
`steered`, `report_token`. The ndjson stream gains `{"type":"turn", …}`
events written after the record update they describe. `leo dispatch watch`
prints turns as they close and stays open through `idle`, exiting on a
terminal status. `list`/`show` display mode, status, turns, steered.
`leo_dispatch`'s reply names the window.

## Error handling

- Interactive on a harness without `TurnHooks`, or codex trust unpinned:
  validation error at dispatch.
- Readiness never reached (dialog, crash): `failed` with the probe's error;
  pane kept under the usual grace.
- Report with bad token, unknown id, stale generation, terminal run, or no
  open turn: 4xx or ignored, logged.
- Claude has no abort hook: an interrupted claude turn is closed by the stuck
  sweep (`lost`) or the next `UserPromptSubmit`.
- `composer busy`: send fails, nothing changes; the reply says a human has a
  draft.

## Testing

- Adapter `TurnHooks` argv exact for codex and claude; launch produces
  `new-window -P -F '#{pane_id}'` with the pane id persisted.
- Report endpoint: token scoping; generation filtering; FIFO close and
  `turn_id` match; duplicate and late reports ignored; terminal latch.
- Injector `InjectInto`: composer-busy rejection without keystrokes; paste
  argv; ack within timeout; no ack → `delivered: false`.
- Waits: run id vs turn id; queued keeps pending; user turns do not hide an
  orchestrator turn's outcome.
- Concurrency: slots per orchestrator turn, released once; user turns exempt.
- Lifecycle: cancel, session_end vs pane death with final-report grace, idle
  close no-ops when generation/turn count moved, restart reattach and
  finalize, stuck sweep.
- e2e with a fake harness binary (prompt box, runs the hook command on each
  input): dispatch → wait turn 1; send → wait turn 2; typed input → user turn;
  daemon restart → reattach.
- Live before merge, from this session: codex interactive on
  `codex-explorer` (wait, send, type in the pane), claude once for the inbox
  path, hook trust mechanism confirmed, update prompt confirmed suppressed.

## Follow-ups

Default to interactive once both contracts are proven; inbox push of turn
results to idle Claude callers; dispatches page in the web UI; OpenCode when
it exposes hooks.
