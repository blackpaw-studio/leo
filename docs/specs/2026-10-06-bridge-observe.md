# Bridge state in the observability API

Status: approved design, 2026-10-06. Builds on `2026-10-03-claude-mod-bridge.md`,
`2026-10-06-bridge-delegation-ux.md`, and `2026-07-31-observability-api.md`.

## Goal

The Claude Code mod bridge sees turn boundaries, usage, tool calls, permission
prompts, subagents, and compaction, but none of it reaches `/api/v1`. Expose it
through the generic observability API so any client (leoterm, the Den, the web UI)
gets semantic agent state instead of inferring it from terminal output, and give
clients a bearer-authed control surface that does not type into tmux.

Non-goals: anything client-specific; parity for codex/opencode (their new fields stay
empty); background Bash job tracking (no hook exists).

## Decisions

- **B-051:** after `turn.complete`, an agent stays `working` while it has outstanding
  leo dispatches or native background subagents. `finished` fires when the last child
  ends. No new attention state; an additive `outstanding` count explains the hold.
  Only dispatches whose caller has a bridge key count, owned by the agent holding that
  key; keyless dispatches still appear in `Snapshot.dispatches` but never hold attention.
- **Content:** `agent_turn_completed` carries a 280-char sanitised preview of the final
  message, and tool activity carries a one-field summary. Always on, no config knob.
  Prompts, full tool inputs, and results are never sent.
- **Control auth:** the new control endpoints accept the operator `apiToken` only.
- **Versioning:** all changes are additive; `SnapshotVersion` stays 1. The SSE `hello`
  payload gains `features: ["bridge_turns","attention_reason","dispatch_tree","agent_usage","agent_control"]`.

## Mod → daemon reports

New `event` reports, strictly parsed in `internal/bridge/protocol.go`:

| Report | Payload | Source (mod API) |
|---|---|---|
| `activity` | `{tool?, summary?}` | main-loop `tool.call`; empty `tool` after `next` resolves |
| `attention` | `{state: needs_input\|cleared, kind: permission\|question\|elicitation, tool?, summary?}` | `classic.PermissionRequest`, `tool.call` AskUserQuestion, `classic.Elicitation`; cleared by `PostToolUse`, `PostToolUseFailure`, `PermissionDenied`, or `turn.complete` |
| `subagents` | `{running}` | `classic.SubagentStart` / `SubagentStop`, counted by `agent_id`, survives `turn.complete` |
| `compact` | `{phase: started\|completed\|failed, trigger: manual\|auto, error?}` | main-loop `session.compact` around `next` |

`activity` is latest-wins: the mod replaces an unsent activity report, sends at most
one per `ActivityMinInterval = 1s`, and never retries it. The `turn.complete` usage already
carries tokens and `context {tokens, window, percent}`.

Tool summaries (mod-side; one field, ≤200 chars before daemon clamping):
Bash → first word; Read/Edit/Write → path, home-abbreviated; Grep/Glob → pattern;
WebFetch → host; MCP and everything else → tool name only.

## SSE events (`internal/observe/event.go`)

- `agent_turn_started {agent, session_id}`
- `agent_turn_completed {agent, session_id, outcome: completed|aborted, preview, tokens{input,output,cache_read,cache_creation}, cost_usd?, context?}`
- `agent_session_ended {agent, session_id, reason}`
- `agent_compaction {agent, phase, trigger, context_percent?}`
- `agent_usage {agent, usage}`: only when usage changes outside a turn completion
- `dispatch_changed {dispatch}`: full record; terminal `status` marks the end
- `agent_activity` (existing): `current_action.kind` gains `"tool"`; `attention` gains
  optional `reason {kind, tool, detail}` and `outstanding {dispatches, subagents}`

Throttling: `agent_activity` is coalesced to at most one per agent per second, and the
trailing edge is always published. Turn, attention, compaction, and dispatch events are
never throttled.

Daemon clamps all mod-supplied strings as untrusted input: `detail`/`summary` to
`MaxActionDetail = 120`, `preview` to `MaxTurnPreview = 280`, both through the pane
sanitiser (strip ANSI, fold newlines).

## State (`/api/v1/state`)

- `Agent.usage {session_id, session{tokens, cost_usd}, incarnation{tokens, cost_usd}, context{tokens, window, percent}}`:
  `session` resets when `session_id` changes; `incarnation` resets on respawn.
- `Agent.outstanding {dispatches, subagents}`
- `Agent.bridge: connected|absent`: omitted for non-claude harnesses
- `Snapshot.dispatches[]`: `{id, name, role, template, model, status, stalled, caller_agent, parent_dispatch_id, started_at, ended_at?, tokens_in, tokens_out, cost_usd}`.
  Includes live dispatches plus those finished within the last 60 s. `parent_dispatch_id` is set when
  the caller's bridge key is `dispatch.<id>`, so clients build the tree from the flat list.

## Control endpoints

On `apiMux` (bearer auth, operator token only) and on the daemon unix socket:

- `POST /api/v1/agents/{name}/message {text ≤ 256 KiB, from?}` → `{transport: "bridge"|"legacy"}` (202 `{queued: true}` when queued)
- `POST /api/v1/agents/{name}/interrupt`
- `POST /api/v1/agents/{name}/compact {instructions? ≤ 4 KiB}`
- `POST /api/v1/agents/{name}/clear`

Each wraps a core extracted from `internal/web/handlers_agent_control.go`: the bridge
route first, then the legacy inbox/tmux path. A missing agent returns 404.

## Wiring

- `observe.BridgeFeed` subscribes to the bridge hub, registered alongside the dispatcher
  subscriber in `internal/web/consult_runtime.go`. It maps bridge keys to agent names,
  keeps a per-agent store, and publishes events.
- `buildAgent` in `internal/web/handlers_observe.go` merges the feed's store.
- Dispatch events come from a 1 s diff ticker over the dispatch store (same pattern as
  `consult/bridge_state.go`), which also exports per-agent outstanding dispatch counts.
  Those counts cover only records with a `CallerBridgeKey`, resolved through
  `bridgeKeyOwner`; no tmux lookup or rename alias is used for keyless records.

## Testing

- Go table tests: strict parse of each report; feed translation into a recording
  publisher; throttle/coalesce with a fake clock; clamping (ANSI, newlines, 10 KB input);
  deferred `finished` with outstanding > 0; dispatch parent links.
- Endpoint tests with `httptest`: bridge path and legacy fallback, asserting the tmux argv.
- Mod tests via `claude plugin test`: each hook mapping, activity coalescing, summaries.
- e2e on an isolated daemon (B-051 repro, written first): a scripted turn starts a
  background `Agent`; the SSE trace must show `working` → `finished` only after `SubagentStop`.

## Implementation split

0. Contract: protocol report types, observe event/state types and caps, `features`,
   provider seams in `handlers_observe.go`. Lands first.
1. A, mod: `bridgemod/leo-bridge/hooks/*` and mod tests.
2. B, projection: `observe/bridgefeed.go`, `observe/attention.go`, wiring, `buildAgent` merge.
3. C, dispatch tree: `consult/observe_dispatch.go`, which provides the outstanding-dispatch count.
4. D, control API: `web/handlers_api_control.go`, core extraction, routes, IPC registration.

A, C, and D run in parallel after 0. B depends on C's count seam (defined in 0). The
e2e fixture lands last.
