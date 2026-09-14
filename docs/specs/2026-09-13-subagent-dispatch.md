# Subagent dispatch: `leo_dispatch` / `leo_wait`

## Goal

Let any leo-connected coding agent (Claude Code, Codex, OpenCode) run a headless
subagent on another harness and model through leo, collect the result later, and
watch it work in tmux. No Claude model babysits the subagent's CLI: the parent
makes one dispatch call and one wait call.

Today this is done with Claude Code agent definitions (`codex-implementer`,
`codex-reviewer`, ...) that wrap the codex CLI from inside a Claude subagent.
That costs a second model's tokens for supervision and gives no live visibility.

## Non-goals (v1)

- Continuing a finished subagent (`codex exec resume`). Follow-up.
- A web UI page for dispatches. Records land on disk in a shape the UI can read
  later.
- Pushing results into the caller via the inbox socket (PR #172). `leo_wait` is
  the delivery mechanism; inbox push is a later convenience for Claude callers.
- Cross-host dispatch. Local daemon only.
- Worktree isolation. Parallel subagents in one checkout are the caller's
  problem, as with the current wrappers.

## Design

`internal/consult` already runs a one-off harness process, records its event
stream, and returns its final text synchronously. This spec generalizes it into
asynchronous runs. Consult survives as a thin wrapper: dispatch with the
read-only preamble, then wait.

### MCP tools

`leo_dispatch {template, prompt, model?, cwd?, name?}` → returns immediately
with `{id, harness, model, cwd, watch}` where `watch` is the command
`leo dispatch watch <id>`.

- Validation (unknown template, bad model, bad cwd, template allowlist) fails
  synchronously with the same messages consult uses today.
- `cwd` defaults to the MCP server process's working directory, which Claude
  Code sets to the session's project directory. An override must be an
  existing absolute directory.
- The template supplies harness, model, `harness_options`, `env`, and
  `max_turns`. Whether the subagent may write is the template's permission
  mode; dispatch adds no preamble. Users define a `codex-implementer` template
  with a write-capable permission mode and a `codex-reviewer` template without
  one.
- `name` is an optional label stored on the record and shown by `leo dispatch list/show`.

`leo_wait {ids: [string], timeout_seconds?}` → blocks until every id is
terminal or the timeout elapses; returns one entry per id:
`{id, status, elapsed_seconds, text?, error?}`.

- One call waits on any number of ids, so a fan-out needs one blocking call.
- Already-terminal ids return immediately. On timeout, still-running ids come
  back with `status: running` and no error; the caller may wait again.
- Each MCP wait is capped just under `consult.RunTimeout` (30 minutes), so
  callers re-wait for longer work. Dispatch runs themselves are unlimited by
  default; `timeout_seconds` adds an explicit cap. Consult runs remain capped
  at 30 minutes.
- An unknown id yields an error entry for that id, not a failed call.

`leo_cancel {id}` → kills the subagent's process group; status becomes
`canceled`. Cancelling a terminal id is a no-op that returns the record.

`leo_consult` keeps its external contract. Internally it is dispatch with the
read-only preamble followed by wait on that one id.

The consult template allowlist (`perms.CanConsult`, config key unchanged)
governs dispatch templates too.

### Daemon

Endpoints, all under the existing API auth:

- `POST /api/dispatch` `{from, template, model, prompt, cwd, name, timeout_seconds?}` → `{id, ...}`
- `GET /api/dispatch/{id}` → record
- `GET /api/dispatch/wait?id=..&id=..&timeout=` → long-poll, returns entries
- `POST /api/dispatch/{id}/cancel`
- `POST /api/consult` unchanged.

Execution moves out of the HTTP request. The dispatcher starts a goroutine per
run under the daemon's context, keeps the cancel func keyed by id, and reuses
the existing run path unchanged: child process in its own process group, tee to
the recorder, `ParseEvents` for the final text. A caller disconnecting no longer
kills the run. The `maxConcurrent` semaphore and `queued` status stay.

Records move from `<state>/consults/` to `<state>/dispatches/`, ids gain the
prefix `d-`, and the record gains `kind` (`consult` or `dispatch`), `cwd`,
`name`, and `text` (final message, written on `done`). On daemon start, any
non-terminal record is marked `failed` with reason `daemon restarted`.

### Visibility

When a run starts, the daemon opens a viewer window on its own tmux server:

- Caller is a supervised leo agent (its `from` resolves to a tmux session):
  `tmux new-window -d -t leo-<caller> -n <label>·<hex4> "leo --config <path> dispatch watch <id>"`. The label is the run name or template, sanitised and truncated to 24 characters; the suffix is the last four ID hex characters.
- Any other caller: the same window in a `leo-dispatch` session, created on
  demand.

The window is a viewer, never the executor. It has `remain-on-exit` set. It
closes when the result is collected (via `leo_wait`, `leo dispatch run`, or web
`/api/dispatch/wait`). Failed, canceled, and timed-out runs remain open for
about one hour for post-mortem inspection; `leo dispatch watch <id>` can replay
the stream anytime. A tmux failure is logged and never fails the dispatch.

### CLI

`leo dispatch run <template> [-m model] [--cwd dir] [--name n] [--timeout duration] <prompt>` runs
synchronously and prints the result. `leo dispatch watch|show|list|cancel <id>`
operate on records. `leo consult watch` becomes an alias of
`leo dispatch watch`.

### Documentation

`docs/configuration/consults.md` becomes `dispatches.md` covering both tools,
with an example pair of implementer and reviewer templates. The `leo_skill`
ops docs and the MCP tool descriptions mention dispatch alongside consult.

## Error handling

- Harness failure, timeout, or cancel produce a per-id `error` string from
  `leo_wait`; other ids in the same call are unaffected.
- Recording failures cost visibility, not the result, as in consult today.
- A `cwd` that disappears before the run starts fails that run with `failed`.

## Testing

- `internal/consult`: async start returns before the process exits; wait-all
  aggregates mixed statuses; cancel kills the process group; timeout; restart
  marking; consult wrapper still returns text synchronously.
- Handlers: the four endpoints via the stubbed exec seam, following the
  existing consult handler tests.
- MCP tools: dispatch returns an id and the default cwd is the server's
  working directory; override validation; wait shapes; allowlist denial.
- tmux argv is asserted through the stubbed seam, and one `make e2e` case
  dispatches a trivial run and checks the window exists on the live tmux
  server. Stubbed seams alone have shipped argv bugs before.
- Live verification before merge: dispatch from a Claude Code session to a
  codex template, confirm the window appears in that agent's session, and
  `leo_wait` returns the final text.

## Follow-ups

Inbox push for idle Claude callers (MCP server parent PID maps to the
session's socket), `continue` via `codex exec resume`, a dispatches page in
the web UI, and replacing the `codex-*` wrapper agents in `~/.claude/agents`
with skill instructions that call `leo_dispatch`.
