# Dispatches and consults

Leo can run a template's harness/model as a one-shot subagent. `leo_consult`
and headless dispatches receive a self-contained prompt and retain no
conversation after they finish. Dispatches can also run interactively.

## Nested agents

Dispatches and consults cannot launch nested agents: Leo disables the native
subagent tool for each supported harness, and tells the subagent that the
orchestrator performs review. There is no opt-out yet.

## MCP tools

`leo_consult(template, prompt, model?)` is for a quick second opinion. It
waits and returns the consultant's final answer directly. Consults get an
advisory preamble: inspect and answer, but do not modify files.

`leo_dispatch(template, prompt, model?, cwd?, name?, mode?, timeout_seconds?, notify?, isolation?)`
starts work asynchronously and immediately returns an ID. Use it for
implementation, review, or exploration that can proceed while the caller does
other work. The prompt must say exactly what the subagent should do; it has no
access to the caller's conversation. `cwd` defaults to the caller's working
directory. `mode` is `headless` by default; set it to `interactive` to start a
steerable TUI (see [Interactive mode](#interactive-mode)).

Dispatch notifications default to enabled. Set `notify: false` (or CLI
`--notify=false`) to disable the eventual completion wake-up. Synchronous
`leo_consult` calls never notify. Set `isolation: "worktree"` or CLI
`--isolation worktree` to run from a managed Git worktree created at the
repository's committed HEAD. Leo keeps worktrees containing uncommitted or
committed changes and reports their path and branch when the result is collected;
unchanged worktrees are removed while their branches remain available.
A subagent that deliberately daemonizes a writer outside its process group can
outlive collection; such writes after a clean removal are lost.

One notification is considered for each completed headless run or interactive
turn. A covering `leo_wait` suppresses it, including a wait registered after
completion but before delivery. The single-line notification identifies the
dispatch or turn, its outcome and cumulative active time, and directs the
caller to collect the result with `leo_wait`.

Delivery is best-effort and at most once. Leo durably claims a notification
before writing to the caller, so a daemon crash can lose a claimed notification
rather than risk sending it twice. Collection remains available in that case.

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
explicit `model` is validated by that harness. `can_consult` permissions apply
to both consult and dispatch targets.

## CLI

```console
leo dispatch run codex-implementer "Add the parser tests" --cwd "$PWD"
leo dispatch run codex-implementer "Inspect only" --notify=false
leo dispatch list
leo dispatch watch d-12ab34
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

`list` includes measured `INPUT`, `OUTPUT`, `COST_USD`, `USAGE_TURNS`, and
`TOOLS`. An em dash is unknown; zero is measured. Claude reports native token
usage and cost (including separately reported cache input), Codex reports
completed-turn usage without double-counting cached input, and OpenCode only
reports tokens from completed steps (reasoning output is included). Leo never
estimates Codex or OpenCode cost. Interactive runs leave usage unknown because
their hooks do not provide a complete native event stream.

## Interactive mode

Set `mode: interactive` on `leo_dispatch`, include `"mode": "interactive"`
in `POST /api/dispatch`, or run `leo dispatch run <template> <prompt> --mode
interactive`. Headless remains the default. Interactive dispatch is not
supported by the opencode harness.

Leo opens a real Codex or Claude TUI in a window of the caller's tmux session,
named `<label>·<hex4>`. The user can watch it and type directly into its
composer. The opening prompt and each orchestrator follow-up are turns; their
completion is reported by the harness hooks to `leo dispatch report`.
Codex uses `user_prompt_submit`, `stop`, `interrupt`, and `session_end` hooks
installed in `$CODEX_HOME/hooks.json`, with matching trust entries; the hooks
are environment-gated, so ordinary Codex sessions no-op. Claude gets `Stop`,
`UserPromptSubmit`, and `SessionEnd` hooks through `--settings`, plus a trust
flag in `~/.claude.json`.

Interactive statuses are `queued`, `running`, `idle`, `settling`, `closed`,
`failed`, `canceled`, and `timeout`. Turn outcomes are `finished`,
`interrupted`, `lost`, and `rejected`. A user submission opens a free user
turn and marks the record `steered: true`; orchestrator turns consume a
concurrency slot until they settle. Slots are per orchestrator turn, so idle
sessions and user turns are free.

Use `leo_send_dispatch {id, message}` or `leo dispatch send <id> <message>`
only while the run is `idle`. A send returns `{turn_id, delivered}`; `delivered`
is usually `false` at that moment because the harness acknowledges the paste
asynchronously through its prompt-submit hook, so never re-send on that alone:
wait on the turn id and trust its outcome. A send is
rejected if the composer is busy or unknown, no slot is available, pasting
fails, the body contains disallowed control characters, or the run is not
idle. Use `leo_wait` on a run ID to wait for the latest orchestrator turn at
the moment the wait starts, or on an explicit turn ID such as `d-…#2` to wait
for that turn. Wait results include `turn_id`, `outcome`, `delivered`, and
`stalled`; a turn with no hook activity for ten minutes is reported stalled on
wait timeout, not forcibly closed.

An interactive dispatch is bound to its caller's tmux session. It closes on
cancel, TUI exit, one hour of empty-composer idle time, or the session timeout.
Nothing survives a daemon restart, and interactive panes never close merely
because their result was collected. Known limitations: opencode is unsupported;
there is a narrow delivery-attribution race when a human submits during an
armed orchestrator send; hook reports are accepted even if forged.

An orchestrator flow looks like this:

```text
leo_dispatch(..., mode: "interactive") → leo_wait(["d-…"])
→ leo_send_dispatch({id: "d-…", message: "Follow up"})
→ leo_wait(["d-…#2"]) → leo_cancel({id: "d-…"})
```

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
harnesses, unsupported adapters, and exhausted capacity are rejected before
the record is changed. Timeout and completion notification apply separately
to each invocation; elapsed active time and measured usage accumulate across
turns. Recorded output is append-only, so `dispatch output` and `watch` show
the complete session history.

## Headless viewer windows

Headless `leo_dispatch` opens a detached viewer window on Leo's dedicated tmux server
(`tmux -L leo`). If its caller is a live supervised agent, the window is added
to that agent's `leo-<caller>` session. Otherwise Leo creates or reuses the
`leo-dispatch` session. The window is named `<label>·<hex4>`, using the run
name when set or its template otherwise; labels replace whitespace, `:`, and
`.` with `-` and are truncated to 24 characters. It runs `leo dispatch watch
<id>` with `remain-on-exit` enabled. The window closes when a successful
result is collected via `leo_wait`, `leo dispatch run`, or web
`/api/dispatch/wait`. Failed, canceled, and timed-out runs remain open for
about one hour for post-mortem inspection. Use `leo dispatch watch <id>` to
replay the stream anytime. Tmux is observability only: any tmux failure is
logged and never prevents a dispatch from running. `leo_consult` does not open
a window.

## Records and limits

Records live under `<state>/dispatches/`, with `0600` files in a `0700`
directory:

- `<id>.json` records caller, kind (`dispatch` or `consult`), template,
  harness, model, workspace, prompt, status, timing, terminal error/text, and
  for interactive runs mode, pane/session IDs, turns, and `steered`.
- `<id>.ndjson` contains timestamped harness events. Interactive streams also
  record turn and status transitions; malformed output is kept as raw text.

Headless status moves through `queued → running → done | failed | timeout |
canceled`. At most six headless runs execute at once; queued work remains
cancellable. Interactive slots are instead held per orchestrator turn.
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
