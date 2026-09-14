# Interactive dispatch: subagents you can watch, steer, and message

Extends `docs/specs/2026-09-13-subagent-dispatch.md` (shipped, v0.23.0).
Revision 4: rev 3's scope (subagents bound to the caller, no queued sends,
paste-only delivery, forgery accepted) plus the fixes from its review.

## Goal

Make `leo_dispatch` comparable to Claude Code's native subagents: the
orchestrator starts a subagent, gets its result, and can send follow-up
messages; the user can watch it in a tmux window and type into it. The
subagent is a real harness TUI (codex or claude) in a pane of the caller's
tmux session. Turn boundaries come from the harness's own hooks.

## Non-goals

- Replacing headless mode. It stays the default and what `leo_consult` uses.
- Supervision or durability. A subagent lives and dies with its caller's
  tmux session and with the daemon. No restart loop, no reattach.
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
  the record before anything is injected. All targeting and cleanup use it;
  cleanup is `kill-pane`.
- `session_id` from the first hook report.
- The pane is created in the caller's session (`leo-<caller>`), or
  `leo-dispatch` for non-agent callers.
- **Supervisor change:** today the supervisor decides an agent has exited
  when its tmux *session* disappears. Subagent windows would keep the session
  alive after the agent's own pane exits and break restart. The supervisor
  must instead watch the agent's primary pane and, when it dies, kill the
  whole session (taking subagents with it) before applying its restart
  logic.
- On daemon start, every non-terminal interactive record is settled:
  `closed` if it has a finished turn, else `failed`; its pane is killed if
  alive. Panes of already-terminal records that still exist are killed too.
  Nothing is reattached.

### Launch

The daemon builds a `KindAgent` launch spec from the template (model,
`harness_options`, `env`, cwd = dispatch cwd) and starts it:

```
tmux new-window -d -P -F '#{pane_id}' -t <session> -n <label>·<hex4> -c <cwd> [-e K=V…] <shell-quoted harness command>
```

Argv comes from the adapters' `Args`/`Env`; every element is shell-quoted;
the leo executable path is absolute. The opening turn is created at `Start`
(status `queued`) so the run always has a turn to wait on; any launch or
opening-injection failure settles the run `failed` with that turn `rejected`.

Startup dialogs are prevented, not dismissed: the adapters' existing
prelaunch config (codex trust entry, claude onboarding/trust settings) is
reused, plus the codex config key that disables the update prompt, verified
live. The readiness probe treats any remaining dialog as launch failure.

### Composer classifier

A new **passive** classifier per harness, `Classify(capture) →
{Busy, Empty, Draft, Unknown}`, from a pane capture with no keystrokes. It
must recognise multiline drafts, placeholder text, collapsed-paste markers,
and footer chrome; the existing probe classifier (which reports drafts as
empty) is not reused for this. `Unknown` fails closed: sends are rejected and
idle-close is deferred.

### Hooks

New adapter method `TurnHooks(reportCmd []string) (args []string, err
error)` adds per-launch hooks for four events, all running
`leo --config <path> dispatch report <id>`:

| Event | codex | claude | leo effect |
|---|---|---|---|
| prompt submitted | `user_prompt_submit` | `UserPromptSubmit` | acknowledges an armed orchestrator turn, else opens a user turn |
| turn finished | `stop` | `Stop` | closes the matching turn with `last_assistant_message` |
| turn aborted | `interrupt` | none | closes the matching turn as `interrupted` |
| session exited | `session_end` | `SessionEnd` | begins settlement |

The report command reads the stdin payload, generates an `event_id` once,
and POSTs `{event_id, payload}` to `POST /api/dispatch/{id}/report` with the
agent API token from its environment. Delivery is bounded to
`report_deadline` (20 s total, 3 s per request, backoff between). The daemon
dedups on `event_id`, answering duplicates and reports for settled runs with
200 so retries stop.

Codex hook delivery (verified live on 0.153.4): per-launch `-c hooks.*`
overrides are accepted but inert, and `--dangerously-bypass-hook-trust` only
affects home-file hooks, so hooks must come from `$CODEX_HOME/hooks.json`
with a trust entry in `$CODEX_HOME/config.toml`:

```toml
[hooks.state."<abs path to hooks.json>:<event>:<group>:<index>"]
trusted_hash = "sha256:<hash>"
```

Leo therefore installs its four hook entries into the user's `hooks.json`
(merged idempotently with existing entries, never removing others) and
writes their trust entries, in the same prelaunch step that already writes
codex's workspace trust. The hook command is `leo dispatch report`, which
exits 0 immediately unless `LEO_DISPATCH_ID` and `LEO_CONFIG` are set in its
environment; interactive launches set them, so ordinary codex sessions run a
no-op. Every codex launch also passes `-c check_for_update_on_startup=false`.
If the user's `hooks.json` has other untrusted hooks, codex will show its
review dialog; prelaunch detects that and fails the dispatch with a message
naming the untrusted hooks.
Claude: entries merged into the existing `--settings` JSON, with the same
env gate.

### Turns

```
{turn_id: "d-…#n", source: orchestrator|user, started_at, ended_at,
 delivered: bool, slot_held: bool,
 outcome: finished|interrupted|lost|rejected, text, harness_turn_id}
```

Statuses: `queued`, `running` (an open turn exists), `idle` (alive, no open
turn), `settling`, and the terminal, latched `closed`, `failed`, `canceled`,
`timeout`. `done` remains the headless terminal state.

**Matching close events to turns.**

- codex: every submit event carries `turn_id`; the turn records it as
  `harness_turn_id`. A `stop`/`interrupt` closes exactly the open turns with
  that `harness_turn_id` (queued prompts coalesced by codex into one turn
  share the id and close together). A close whose `turn_id` matches no open
  turn is buffered for `unmatched_grace` (5 s) awaiting its submit; if none
  arrives it is logged and dropped. It is never applied to an unrelated turn.
  Codex's assignment of `turn_id` to prompts queued during a running turn is
  verified live and covered by a test with active A plus queued B and C.
- claude: no turn ids. Closes apply to the oldest open turn (claude executes
  serially). A submit during an open claude turn opens a new turn without
  closing the old one; the old closes on its own `Stop`.
- A close for an already-closed `harness_turn_id` is ignored with 200.

**At most one open orchestrator turn.** Opening any new turn (orchestrator
or user) first settles an expired, undelivered orchestrator turn as `lost`
(releasing its slot), so an unacknowledged send can never hold a slot behind
later work.

**Slots.** Each orchestrator turn has `slot_held`, set when the slot is
acquired and consumed by one idempotent close path; every outcome
(`finished`, `interrupted`, `lost`, `rejected`) and every settlement goes
through it, so a slot is released exactly once.

Transitions happen under the dispatcher lock; side effects (paste, kill)
happen outside it after revalidating status.

### Opening prompt and follow-ups

One injector entrypoint, `InjectInto(paneID, classifier, text)`:

1. Acquire a slot for the turn; none free → `rejected: no capacity`.
2. Classify the pane. Anything but `Empty` → `rejected: composer busy` (or
   `composer unknown`). No keystrokes were sent.
3. `set-buffer --`, `paste-buffer -d -p` (bracketed paste), then confirm the
   pasted body appears; confirmation accepts both Claude's `[Pasted text…]`
   and Codex's `[Pasted Content …]` placeholders. A confirm failure →
   `rejected: paste failed` (nothing was submitted).
4. Arm the acknowledgment window (`ack_timeout`, 10 s) **immediately before**
   `Enter`, then send `Enter`.
5. A submit event while armed marks `delivered: true` and disarms. A human
   submission inside the window is attributed to the orchestrator turn; the
   empty-composer check makes the race narrow and it is documented, not
   prevented. When the window expires the turn stays open with
   `delivered: false` and no longer absorbs submits; a later matching close
   still closes it.

A rejected opening turn settles the run `failed`. A rejected send leaves the
run `idle`; the reply carries the reason.

`leo_send_dispatch {id, message}` → `{turn_id, delivered}` (also
`POST /api/dispatch/{id}/send`, `leo dispatch send`). Allowed only when the
run is `idle`; anything else is rejected with the current status. Bodies are
plain text; control characters other than newline are rejected. Both
harnesses use paste. The `#` in a turn id is URL-encoded in paths.

### Human steering

A submit with no armed orchestrator turn opens a `source: user` turn and
moves `idle → running`. User turns take no slot. The record shows
`steered: true` once any user turn exists.

### Waiting

`leo_wait` ids may be a run (`d-…`) or a turn (`d-…#n`). A run id is
resolved **once, when the wait starts**, to the run's latest orchestrator
turn (there is always at least the opening turn), and the wait tracks that
turn. A turn id waits on that turn's outcome. Entries gain `turn_id`,
`outcome`, `delivered`, `stalled`. A `queued` run keeps the wait pending. An
open turn whose run has had no hook activity for `stalled_after` (10 min) is
reported with `stalled: true` on each wait timeout; it is not closed and its
slot is not released. The caller cancels or keeps waiting.

### Concurrency

`maxConcurrent` (6) admits orchestrator turns: the opening prompt and each
send hold a slot from acquisition until their one close path runs. Idle
sessions and user turns hold no slot.

### Settlement

Settlement is entered once, with an immutable deadline, and ends in exactly
one terminal status. While `settling`: sends are rejected, trailing close
reports are still applied, submits are ignored. At the deadline every open
turn is closed (`interrupted` for cancel and timeout, `lost` otherwise), the
terminal status latches, waiters are notified, the recorder closes, and the
pane is killed if alive. A kill failure is retried by the sweep independently
of the record's status.

Entry points:

- `leo_cancel`: `kill-pane` first, then settle as `canceled` with a zero
  deadline.
- `session_end`, or pane death noticed by the sweep: deadline =
  `final_report_grace` (30 s, longer than `report_deadline`) so a trailing
  `stop` still lands; terminal status `closed` if any turn ever finished,
  else `failed`.
- Idle close: `idle` for `idle_close_after` (1 h) and the classifier says
  `Empty` at check time (`Draft`/`Unknown` defers) → `kill-pane`, settle
  as `closed`.
- Session timeout (`timeout_seconds`, whole session): `kill-pane`, settle as
  `timeout`.

Interactive panes never close on collection. Records of non-terminal runs
are exempt from retention pruning; dead panes are swept after the existing
grace.

### Records, stream, CLI

`Record` gains `mode`, `pane_id`, `session_id`, `turns`, `steered`. The
ndjson stream uses the existing `{t, d, raw}` envelope with a per-run
sequence number and two event kinds: `d.type = "turn"` (a turn opened or
closed) and `d.type = "status"` (every status transition, including
settlement). Events are written after the record update they describe.
`leo dispatch watch` renders both, stays open through `idle`, and before
exiting on a terminal status re-reads the record so a missed final event
cannot leave it hanging or wrong. `list`/`show` display mode, status, turn
count, steered. `leo_dispatch`'s reply names the window.

## Error handling

- Interactive on a harness without `TurnHooks`, or codex trust unverified:
  validation error at dispatch.
- Readiness never reached: run `failed`, opening turn `rejected`; pane kept
  under the usual grace.
- Report with unknown id, settled run, duplicate `event_id`, already-closed
  `harness_turn_id`: 200, logged, no change. Unmatched identified close:
  buffered then dropped as above.
- Claude has no abort hook: an interrupted claude turn shows as `stalled`
  until the user submits again (new turn; the old one closes as `lost` only
  once it is stalled) or cancels.

## Testing

- `TurnHooks` argv exact for codex and claude; launch produces
  `new-window -P -F '#{pane_id}'`; record persisted before injection;
  opening turn exists at `Start`.
- Classifier: fixtures for empty, single-line draft, multiline draft,
  placeholder, collapsed paste, busy, unknown, for both harnesses.
- Report endpoint: `event_id` dedup; codex `turn_id` match incl. coalesced
  ids and buffered unmatched closes; claude oldest-open; ignored cases;
  settled-run 200.
- `InjectInto`: each rejection reason with zero keystrokes; paste argv; ack
  armed before Enter; expiry leaves `delivered: false`; expired turn settled
  `lost` when a new turn opens.
- Slots: `slot_held` consumed once across every outcome and settlement.
- Waits: run id snapshot vs turn id; queued pending; stalled reporting.
- Settlement: cancel, `session_end` vs pane death with grace longer than
  report deadline, trailing stop applied during settling, sends rejected
  while settling, idle close deferred by draft/unknown, timeout outcomes,
  daemon-start settle and orphan pane kill, kill-failure retry.
- Supervisor: agent primary pane death with live subagent windows → session
  killed, restart proceeds.
- e2e with a fake harness binary (prompt box, runs the hook command per
  input): dispatch → wait; send → wait; typed input → user turn; kill pane →
  closed; daemon restart → settled.
- Live before merge, from this session: codex interactive on
  `codex-explorer` (wait, send, type in the pane, cancel); claude once; codex
  queued-prompt `turn_id` behaviour; hook trust mechanism; update prompt
  suppressed; `leo agent restart` of the caller with a live subagent.

## Follow-ups

Default to interactive once both contracts are proven; inbox-socket sends
for claude after verifying the hook fires; durable sessions if a real need
appears; dispatches page in the web UI.
