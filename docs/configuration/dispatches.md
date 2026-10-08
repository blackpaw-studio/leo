# Dispatches and consults

Leo can run a template's harness/model as a one-shot subagent. `leo_consult`
and headless dispatches receive a self-contained prompt and retain no
conversation after they finish. Dispatches can also run interactively.

## Viewer placement

Dispatch viewers open in panes below the caller by default, using tmux's
`main-horizontal` layout. Configure `defaults.dispatch.viewer` with
`placement: pane|window|background` (default `pane`), `max_panes: 1..6`
(default `3`), and `main_pane_height: 20..90` (default `60`). Leo falls back to
a separate window when the cap is reached or a split fails. Session options
`@leo_viewer_placement` and `@leo_viewer_max_panes` override the config.
Release an idle or finished interactive pane with `leo dispatch release <id>`.

`background` never touches the caller's tmux session: every dispatch (headless
viewer or interactive TUI, nested dispatches included) opens as a detached
window in the `leo-dispatch` session, even when the caller is a live
supervised agent. Nothing appears in the caller's window; look at a dispatch
with `leo dispatch watch <id>` or `leo dispatch attach <id>`.

Inside Leo's tmux server, press `prefix + L` to open the dispatch viewer
settings menu. It has four actions: cycle viewer placement (pane → window → background), cycle the pane
cap from 1 through 6, close finished viewers belonging to the current caller
session, and save the current session overrides as the config default. Saving
merges only placement and max-panes into `defaults.dispatch.viewer`, preserves
`main_pane_height`, reloads the daemon, and clears the session overrides only
after both save and reload succeed.

### Per-attach placement

Config and session options are per daemon or per session, so a terminal and an
app attached to the same agent cannot differ. `leo agent attach
--dispatch-placement pane|window|background` (also `leo attach`, with or
without `--cc`, locally or over `--host`) fixes that per client:

```bash
leo agent attach --dispatch-placement background fetch
ssh -tt -e none host leo agent attach --cc --dispatch-placement background fetch
```

The attach registers its pid with the daemon just before it execs tmux, so the
pid is the tmux client's. When a dispatch starts, Leo lists the tmux clients of
the root caller's session (for a nested dispatch, the root agent's, not the
subagent's). Each client contributes its registered placement, or, with no
registration, the session override or config default. The most visible wins:
`pane` over `window` over `background`. The winner then goes through the usual
`max_panes` and caller-pane fallbacks. With no clients attached, placement is
exactly as before.

A registration counts only while a live client of that session has that pid and
attached no earlier than the registration (so a reused pid is ignored). Nothing
unregisters it; it lapses when the client detaches. If the daemon is
unreachable the attach prints a warning and proceeds without it. The flag is
refused from inside tmux (the attach is a popup there). Clients can check the
`attach_dispatch_placement` feature on the SSE `hello` before relying on it.

## Nested agents

Leo disables each harness's native subagent tool inside dispatches and
consults (Claude's `Agent` tool, Codex `multi_agent`, OpenCode `task`), but
nested Leo dispatches are permitted: the dispatch preamble tells the subagent
it may dispatch its own subagents when its brief calls for it, and not to
self-certify its work. There is no per-dispatch opt-out. Leo's MCP server is
injected into dispatches only for the `claude` harness today, so only Claude
dispatches can call `leo_dispatch` this way.

A subagent that calls `leo_dispatch` starts a nested dispatch. Leo records the
calling dispatch as its `parent_dispatch_id` and files the child under the same
`caller` as the parent, so a whole tree reports the root agent as its caller.

Every dispatch and consult run carries `LEO_DISPATCH_ID` (headless, interactive,
and headless continuations alike), which Leo's MCP server uses to refuse
caller-only tools such as `leo_surface_file`: a subagent inherits its
orchestrator's `LEO_PROCESS_NAME`, so without it the subagent would pass for
the orchestrator. The subagent should report files to its orchestrator instead.

## MCP tools

`leo_consult(template, prompt, model?)` is for a quick second opinion. It
waits and returns the consultant's final answer directly. Consults get an
advisory preamble: inspect and answer, but do not modify files.

`leo_consult(prompt, fork: true)` asks a fork of the caller instead: the same
model, over the caller's whole conversation, with no tools. It is the cheapest
consult (the conversation is served from the prompt cache) and suits a sanity
check of the caller's own plan; `template` and `model` are ignored. Only a
bridged claude session can fork: its leo-bridge mod answers the call in-session,
so a fork consult never reaches the daemon and leaves no consult record. Any
other caller gets "fork consult needs a bridged claude session".

`leo_dispatch(template? | role?, prompt, model?, effort?, cwd?, name?, mode?, timeout_seconds?, notify?, isolation?)`
starts work asynchronously and immediately returns an ID. Use it for
implementation, review, or exploration that can proceed while the caller does
other work. The prompt must say exactly what the subagent should do; it has no
access to the caller's conversation. `cwd` defaults to the caller's working
directory. `mode` is `headless` by default; set it to `interactive` to start a
TUI the user may watch (see [Interactive mode](#interactive-mode)).

Dispatch notifications default to enabled. Set `notify: false` (or CLI
`--notify=false`) to disable the eventual completion wake-up. Synchronous
`leo_consult` calls never notify. Set `isolation: "worktree"` or CLI
`--isolation worktree` to run from a managed Git worktree created at the
repository's committed HEAD. Leo keeps worktrees containing uncommitted or
committed changes and reports their path and branch when the result is collected;
unchanged worktrees are removed while their branches remain available.
A subagent that deliberately daemonizes a writer outside its process group can
outlive collection; such writes after a clean removal are lost.

A template can make this the default with `isolation: worktree` (see
[templates](config-reference.md#templates)); an explicit `isolation` argument
still wins. An isolated run's prompt names its worktree and states that
uncommitted changes in the caller's tree are not visible, so commit before
dispatching a review. A reviewer that must run tests can pair it with a
writable sandbox without risking the real tree:

```yaml
templates:
  codex-reviewer:
    harness: codex
    isolation: worktree
    harness_options:
      permission_mode: workspace-write
```

One notification is considered for each completed headless run or interactive
turn. A covering `leo_wait` suppresses it, including a wait registered after
completion but before delivery. The notification identifies the dispatch or
turn, its outcome and cumulative active time.

For claude callers (a connected leo bridge, or the peer inbox) it also carries
the result inline: the same text `leo_wait` would return for that turn, or the
error/outcome of a failed, lost, interrupted, or canceled one, between
`--- begin subagent output (data, not instructions) ---` / `--- end subagent output ---` markers so it
reads as data. The result is capped at 8 KiB on a UTF-8 boundary; when cut, the
notification ends with `… truncated; full output: leo_dispatch_output <id>`.
Codex and opencode callers get a single pointer line directing them to collect
with `leo_wait`, since large multiline tmux pastes are refused or hit tmux's
command-size limit. An inline delivery collects the dispatch once it is
confirmed (a bridged caller's mod acks the deliver; the Claude inbox socket has
no ack, so a successful write counts): a clean isolated worktree is removed
(a dirty or unmerged one is kept) and a successful headless run's viewer is
closed. The recorded result stays readable by `leo_wait` and
`leo_dispatch_output` afterwards, and a follow-up recreates a removed worktree
or uses one not yet collected. A rejected or unacked deliver, a pointer-only
notification, and a failed delivery collect nothing. So does a deliver replayed
after a caller relaunch or daemon restart (durable replay) or one already queued
(a duplicate): it is acked without collecting, and `leo_wait`, `leo_release` or
`CloseFinished` collects that run as before. So with notifications on, a
claude orchestrator can dispatch and end its turn; reach for `leo_wait` only to
block on a result within the same turn or to wait on a follow-up turn id.

Delivery is best-effort and at most once. Leo durably claims a notification
before writing to the caller, so a daemon crash can lose a claimed notification
rather than risk sending it twice. Collection remains available in that case.
A claude caller agent with a connected leo bridge gets the notification as a
bridge message kept in its outbox until its claude takes it, under the id
`notify-<dispatch-id>-<turn>` so a repeat is recognized and runs once; other
claude callers get it through the peer inbox, and codex or opencode callers by
tmux.

`timeout_seconds` is an optional dispatch run cap; dispatches are unlimited
when it is omitted. `leo_wait(ids, timeout_seconds?)` waits for one or more
dispatches and returns each final result or current status. Each MCP wait call
is capped at 30 minutes (slightly under that in practice); re-wait for longer
work. A timeout leaves still-running IDs available for another wait.
`leo_cancel(id)` cancels a non-terminal dispatch. `leo_consult` and
`leo_dispatch` select templates; `leo_wait` and `leo_cancel` operate on
dispatch IDs. In interactive mode, `leo_wait` accepts either a run ID or a
turn ID (`d-…#n`).

`leo_dispatch_output(id, tail?)` reads a nonblocking snapshot of recorded
output without collecting the dispatch. `tail` is a positive rendered-line
count, defaults to 60, and is capped at 400. It accepts turn IDs and returns
the parent run's stream. Use it when a wait result is truncated.

The template-selecting tools use a named `templates:` entry, not a running agent. The
template supplies the harness, model, environment, and harness options; an
explicit `model` is validated by that harness. `leo_dispatch` accepts exactly
one of `template` or a [delegation role](delegation.md); explicit `model` and
`effort` override the active profile target. For a role dispatch,
`can_consult` is checked against the resolved template. `can_consult`
permissions apply to both consult and dispatch targets.

## CLI

```console
leo dispatch run codex-implementer "Add the parser tests" --cwd "$PWD"
leo dispatch run --role implement --effort high "Add the parser tests"
leo dispatch run codex-implementer "Inspect only" --notify=false
leo dispatch list
leo dispatch watch d-12ab34
leo dispatch attach d-12ab34
leo dispatch attach d-12ab34 --host prod
leo dispatch show d-12ab34
leo dispatch output d-12ab34 --tail 120
leo dispatch send d-12ab34 "Please also cover malformed input"
leo dispatch cancel d-12ab34
```

`run` waits and prints the final result; in interactive mode it waits for the
opening turn. `list` and `watch` also work for consults, preserving the older
`leo consult list|watch` interface. `watch` accepts an unambiguous ID prefix;
Ctrl-C only detaches. `show` prints a record as JSON. `list` and `watch`
support `--host`; `run`, `show`, `output`, `send`, and `cancel` currently require the
local daemon.

`attach` shows an interactive dispatch's live TUI, read-only. It links the
dispatch's own tmux window into a throwaway `_watch-<id>-<rand>` session and
attaches a read-only (`tmux attach -r`) client to that, so nothing typed
reaches the dispatch and no pane is moved. The throwaway session removes
itself when you detach or when the dispatch's window closes (exit status 0
either way); the dispatch keeps running. Inside tmux, `attach` opens the same
view in a popup. It prints a one-line reason and exits 1 for an unknown,
headless, or ended dispatch, and for one whose pane is split into its caller's
window or is hidden (use `background` placement, or `leo dispatch watch`).
`--host` runs the remote leo over `ssh -tt`. `leo agent attach` is unchanged.
Because the dispatch's own session has no client attached, the auto-dismiss of
Claude startup dialogs (which skips attached sessions) can still act on a
dispatch someone is watching this way.

`list` includes measured `INPUT`, `OUTPUT`, `COST_USD`, `USAGE_TURNS`, and
`TOOLS`. An em dash is unknown; zero is measured. Claude reports native token
usage and cost (including separately reported cache input), Codex reports
completed-turn usage without double-counting cached input, and OpenCode only
reports tokens from completed steps (reasoning output is included). Leo never
estimates Codex or OpenCode cost. Interactive runs leave usage unknown because
their hooks do not provide a complete native event stream, except bridged claude
runs: their leo-bridge mod reports each turn's tokens (input including cache
reads and writes) and the session's running cost.

## Interactive mode

Set `mode: interactive` on `leo_dispatch`, include `"mode": "interactive"`
in `POST /api/dispatch`, or run `leo dispatch run <template> <prompt> --mode
interactive`. Headless remains the default. Interactive dispatch is not
supported by the opencode harness.

Leo opens a real Codex or Claude TUI, placed the same way as the viewer
(usually a split pane in the caller's own tmux window, falling back to a
separate window when the pane cap is reached or the caller can't be
resolved), labeled `<label>·<hex4>`. The pane is for the user to watch:
follow-ups go through `leo_send_dispatch`, and an orchestrator never asks the
user to type into it. Typing there still works and is recorded as a user turn
(`steered`), but nothing depends on it. `leo_dispatch` reports back exactly where it landed
(`pane <id> (title <label>)` or `window <label> (pane <id>)`). The opening prompt and each orchestrator follow-up are turns; their
completion is reported by the harness hooks to `leo dispatch report`.
Codex uses `user_prompt_submit`, `stop`, `interrupt`, and `session_end` hooks
installed in `$CODEX_HOME/hooks.json`, with matching trust entries; the hooks
are environment-gated, so ordinary Codex sessions no-op. Claude gets `Stop`,
`UserPromptSubmit`, and `SessionEnd` hooks through `--settings`, plus a trust
flag in `~/.claude.json`.

| Event | Codex hook | Claude hook | Behavior |
| --- | --- | --- | --- |
| Prompt submitted | `UserPromptSubmit` | `UserPromptSubmit` | Acknowledges an armed orchestrator turn; if that acknowledgement is late, Leo matches normalized prompt text to the oldest undelivered orchestrator turn before opening a user turn. |

A pane split into the caller's window stays there only while the
orchestrator has work in it. When an orchestrator turn ends and the run goes
`idle`, Leo moves the pane out with `break-pane -d` into a background window
named `<label>·<hex4>` in the same tmux session (the record's `viewer_kind`
becomes `hidden`). A `leo_send_dispatch` follow-up moves it back with
`join-pane -d`, split below the caller as at launch; if the caller's pane is
gone, the pane stays in its own window instead. Turns the user types never
move a pane, and panes that launched in a separate window stay there. Tmux
pane ids (`%N`) survive both moves, so follow-ups keep addressing the same
pane.

Interactive statuses are `queued`, `running`, `needs_input`, `waiting`,
`idle`, `settling`, `closed`, `failed`, `canceled`, and `timeout`. Turn outcomes are `finished`,
`interrupted`, `lost`, and `rejected`. A user submission opens a free user
turn and marks the record `steered: true`; orchestrator turns consume a
concurrency slot until they settle. Slots are per orchestrator turn, so idle
sessions and user turns are free.

Use `leo_send_dispatch {id, message}` or `leo dispatch send <id> <message>`
only while the run is `idle`. A send returns `{turn_id, delivered, queued}`; `delivered`
is usually `false` at that moment because the harness acknowledges the paste
asynchronously through its prompt-submit hook, so never re-send on that alone:
wait on the turn id and trust its outcome. A send is
rejected if the composer is busy or unknown, pasting
fails, the body contains disallowed control characters, or the run is not
idle.

When every slot is busy a follow-up is not rejected: it queues
(`queued: true`, and the turn carries `queued` until it starts) and the turn
id comes back at once. Queued follow-ups and queued new dispatches share one
FIFO line capped by `defaults.dispatch.max_concurrent`; the turn is pasted
when its place comes up, and `leo_wait` on its turn id blocks until it has
finished. A queued follow-up emits no completion notification until it
resolves. If the run is canceled, times out, or closes first, the turn
resolves `interrupted` (or `lost`) with a text saying it was never sent; if a
human types into the pane first (steering), it resolves `lost` for the same
reason; so does daemon shutdown (`interrupted`, with a shutdown reason). A
continuation of a finished headless run queues the same way (`queued: true`,
run status `queued`) and `leo_cancel` withdraws it. A run has at most one
queued follow-up, so a second send is rejected until the first starts. Permission decisions (`decision` / `request_id`) never
queue and never use a slot. Use `leo_wait` on a run ID to wait for the latest orchestrator turn at
the moment the wait starts, or on an explicit turn ID such as `d-…#2` to wait
for that turn. Wait results include `turn_id`, `outcome`, `delivered`, and
`stalled`; a turn with no hook activity for ten minutes is reported stalled on
wait timeout, not forcibly closed.

A claude turn that stops while its own background work is still in flight
(a `run_in_background` shell, a Monitor, a subagent, or a scheduled wakeup)
is not finished: the run goes `waiting`, the turn stays open, and `leo_wait`
keeps blocking. The record's `pending_work` (and `pending` on wait results,
the roster, and `/api/v1`) counts what it waits on, e.g. `1 shell · 1 monitor`.
When that work wakes the session (a task notification or the wakeup's
prompt), the same turn continues, and its next Stop with nothing pending
finishes it with that final message. A `waiting` run is never idle-closed;
it reads stalled only after two hours without hook activity (background work
can end without waking the session), and is not finished on its own then. A
pane that dies while waiting loses the turn, as a busy one does. A send to a
waiting run is rejected like a send to a running one; cancel works as usual.

An interactive dispatch is bound to its caller's tmux session. It closes on
cancel, TUI exit, one hour of empty-composer idle time, or the session timeout.
Nothing survives a daemon restart, and interactive panes never close merely
because their result was collected.

Known limitations: turns are attributed by the harness's own ids (claude's
`prompt_id`, the bridge's turn id, codex's `turn_id`), not by arrival order
or prompt text. A replayed or late hook for a finished turn is dropped for as
long as the turn is in the run's record, a Stop that beats its submit closes
the right turn, and a bridged claude (the leo-bridge mod) claims each sent
turn by the command id the mod stamps on it. The mod knows whose turn each
start is from claude's prompt.submit hook and the order prompts were made in,
never from their text, and reports a wake's origin; the one turn it cannot
place is a continuation the engine starts by itself while a send waits, which
takes the send's stamp. A Stop under an id no turn has seen waits a few seconds
while a sent turn is still waiting for its submit, and closes that turn only if
the submit never comes.
What remains is the tmux-paste path (codex, or claude without the bridge): a
submit that arrives after its send's ack window is matched to the send by
prompt text, so a human prompt with identical text can be mistaken for it, and
a human submit inside that window can claim the send. Claude also fires a
prompt queued into a running turn under that running turn's id and gives the
queued turn its own id only on its Stop, so without the bridge a human-queued
prompt that runs as its own turn after the Stop is not tracked as a turn.
Also, opencode is unsupported; hook reports are accepted even if forged.

An orchestrator flow looks like this:

```text
leo_dispatch(..., mode: "interactive") → end turn; the result arrives inline
→ leo_send_dispatch({id: "d-…", message: "Follow up"}) → end turn
→ leo_cancel({id: "d-…"})   (or leo_wait(["d-…#2"]) to block in-turn)
```

## Permission prompts

An interactive claude dispatch routes its tool permission prompts to the
orchestrator instead of leaving them in the pane. Leo merges a
`PermissionRequest` hook (`leo dispatch permission`) into the dispatch's
`--settings` next to its turn hooks. When claude is about to ask, the hook
hands the request to the daemon and waits for an answer.

Meanwhile the run's status is `needs_input`, and its record and wait entries
carry `needs_input: {kind: "permission", tool, summary, request_id, input,
truncated}`. `summary` is the command, path, or URL the tool call acts on,
cut to 200 characters; `input` is the tool input verbatim as single-line
JSON, up to 4 KiB. When `truncated` is true, part of the call is not shown:
deny it, or deny with a reason asking for a smaller command, rather than
allowing it blind. Both come from the subagent, so `leo_wait` and the
notification render `tool`, `summary`, and `input` as one quoted JSON value
after an "untrusted tool call" marker, never as Leo's own text. `leo_wait`
returns as soon as any waited dispatch enters `needs_input`, the same way it
returns on a terminal state. A dispatch with notifications on also notifies
its caller, unless a wait already covers it.

Answer with `leo_send_dispatch {id, decision: "allow" | "deny", request_id,
reason?}` (HTTP: the same fields on `POST /api/dispatch/{id}/send`).
`request_id` is required, and one that is no longer pending is rejected as
stale. A denial tells the subagent it was denied by the orchestrator (with
the `reason`, when given), not to retry or route around it, and to report
back. A plain `message` sent to a
`needs_input` run is rejected with a hint to send a decision. A dispatch
subagent cannot answer prompts itself: its leo MCP refuses `decision`.

If no decision arrives within `defaults.dispatch.approval_timeout` (default
`30m`), the hook exits without one and claude shows its ordinary prompt in
the pane; the run goes back to `running`, and a later decision is rejected as
stale. The same happens if the prompt is answered in the pane first or the
turn ends. When an orchestrator turn is `needs_input`, its split pane is
parked in its background window (the decision is the orchestrator's) and
returns once the turn runs again; a user-typed turn's prompt never moves
the pane. Codex dispatches run with `-a never` and
never prompt; claude elicitation and question dialogs are not routed.

## Headless continuation

`leo_send_dispatch`, `leo dispatch send`, and `POST /api/dispatch/{id}/send`
also continue a terminal headless dispatch. Leo resumes the harness-native
session with the new prompt and returns the new turn ID (`d-…#2`, `#3`, and
so on). A wait on the run ID snapshots the latest turn when the wait begins;
a wait on an explicit older turn ID remains bound to that invocation.

Continuation requires the original session ID, unchanged template harness,
and an available workspace. A kept isolated worktree resumes in place without
resetting changes. A clean removed worktree is recreated from its retained
branch only while that branch still points to the recorded base commit. Active
or still-reaping runs, missing sessions/templates/workspaces, changed
harnesses, unsupported adapters, and exhausted capacity (headless continuation
does not queue) are rejected before
the record is changed. Timeout and completion notification apply separately
to each invocation; elapsed active time and measured usage accumulate across
turns. Recorded output is append-only, so `dispatch output` and `watch` show
the complete session history.

## Headless viewer windows

Headless `leo_dispatch` opens a detached viewer on Leo's dedicated tmux server
(`tmux -L leo`), placed the same way as an interactive dispatch: usually a
split pane in the caller's own tmux window (subject to the `pane|window`
placement setting and the pane cap), falling back to a separate window when a
split isn't possible or the caller can't be resolved. If its caller is a live
supervised agent, a fallback window is added to that agent's `leo-<caller>`
session; otherwise Leo creates or reuses the `leo-dispatch` session. Either
way it's labeled `<label>·<hex4>`, using the run name when set or its
template otherwise; labels replace whitespace, `:`, and `.` with `-` and are
truncated to 24 characters. It runs `leo dispatch watch <id>` with
`remain-on-exit` enabled. The pane or window closes when a successful result
is collected: via `leo_wait`, `leo dispatch run`, or web `/api/dispatch/wait`,
or when its completion notification is delivered inline and confirmed (claude
callers; a pointer-only codex/opencode notification collects nothing). Collection also
removes a clean isolated worktree, keeping a dirty or unmerged one, and a
second collection is a no-op; `leo_dispatch_output` keeps working afterwards.
An interactive dispatch's pane is never closed by collection. Failed, canceled, and timed-out runs remain open for
about one hour for post-mortem inspection. Use `leo dispatch watch <id>` to
replay the stream anytime. Tmux is observability only: any tmux failure is
logged and never prevents a dispatch from running. `leo_consult` does not open
a pane or window.

## Records and limits

Records live under `<state>/dispatches/`, with `0600` files in a `0700`
directory:

- `<id>.json` records caller, kind (`dispatch` or `consult`), template,
  harness, model, workspace, prompt, status, timing, terminal error/text, and
  for interactive runs mode, pane/session IDs, turns, and `steered`.
- `<id>.ndjson` contains timestamped harness events. Interactive streams also
  record turn and status transitions; malformed output is kept as raw text.

Headless status moves through `queued → running → done | failed | timeout |
canceled`. At most `defaults.dispatch.max_concurrent` runs (default 6, `0` =
unlimited) execute at once; queued work remains cancellable. Interactive slots are instead held per orchestrator turn. An
interactive dispatch started while every slot is busy is recorded `queued`
with no pane (`leo_dispatch` says so) and launches, placed as usual, once a
slot frees. `leo_cancel` on it finishes it `canceled` without opening a pane;
`leo_send_dispatch` on it is rejected until it has started, so `leo_wait` on
it first. The cap applies to new runs and queued follow-ups alike and is
re-read from the config on each dispatch or send.
Consult runs are capped at 30 minutes. Dispatch runs are unlimited unless
`timeout_seconds` (or CLI `--timeout`) is set. Leo retains the 20 newest
settled records and never prunes plausible in-flight runs.

## Example templates

Use separate templates when the worker and reviewer need different model or
permission profiles:

```yaml
templates:
  codex-implementer:
    harness: codex
    model: gpt-5.6-terra
    harness_options:
      permission_mode: workspace-write
  codex-reviewer:
    harness: codex
    model: gpt-5.6-sol
    harness_options:
      permission_mode: read-only
```

The subagent runs in the caller's cwd unless `cwd` overrides it.

An orchestrator can dispatch the implementation, then dispatch the reviewer
with the implementation's scope and request the review after it completes.
