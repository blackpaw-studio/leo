# Bridge-driven delegation, roster, fork consult

Status: draft · 2026-10-06

## Goal

Build on the leo-bridge mod (PR #222) to fix four problems:

1. **Delegation toggle is advisory and stale.** Leo injects the delegation block once, through `--append-system-prompt` at launch, so toggling delegation in the web UI does nothing until the agent restarts. Native Claude subagents are also never hidden, so CLAUDE.md prose can override the toggle. That happened on 2026-10-06.
2. **There is no cheap, context-aware second opinion.** Every consult is a fresh process that can't see the orchestrator's conversation.
3. **Live dispatches can't be seen from the orchestrator's own UI.** Today the only views are the tmux status bar and the panes.
4. **Dispatch usage is marked incomplete** because the bridge only sends session cost. Completion notifications to claude callers also still go through the peer-inbox socket, not the bridge.

All of this applies only to bridged claude sessions (Claude Code ≥ 2.1.287). Unbridged sessions, codex and opencode keep today's behavior.

## Shared foundation: daemon → mod state push

- Add a new stream message `state`. It is a full snapshot, not a command: no `id`, no ack, no outbox, and the latest one wins. The daemon sends it when the mod connects, and again whenever an input changes (delegation config reload, or a dispatch whose caller is this agent changes status, turn or usage). It is debounced at 1 s.
  ```
  state {
    delegation: { enabled, section, hide_agents[] },   // section = rendered block text
    dispatches: [ { id, name, role, template, model, status, stalled,
                    active_seconds, tokens_in, tokens_out, cost_usd } ]
  }
  ```
- `dispatches` holds the non-terminal dispatches whose caller is this agent, plus terminal ones that finished within the last 60 s.
- Dispatch records must carry the caller's agent key. If they don't already, record it from the MCP caller identity at dispatch time.
- The mod writes the snapshot into `$.state` and redraws.
- Add a new report kind `request { op, dispatch_id }`, mod → daemon, through `leo bridge report`. The only op is `dispatch.cancel`. The daemon allows it only when the dispatch's caller is the reporting agent key, then calls the same cancel path as `leo_cancel`.

## 1. Delegation enforcement

- Leo stops putting the delegation block in `--append-system-prompt` for bridged claude launches. The rest of the nudge stays where it is. Unbridged launches keep it.
- **`prompt.compose`:** when `delegation.enabled` is true, append a section `leo-delegation` (scope `session`) containing `section`. The section text says it overrides instruction files that call delegation off. When delegation is off, append nothing. When a new `state` changes `enabled` or `section`, call `$.ui.invalidate("prompt.section")`.
- **`agent.offer`:** while delegation is enabled and fallback is not latched, return `{ isOffered: false }` for agent types whose name, without any plugin prefix, is in `hide_agents`.
- **`tool.call` on `Agent`:** apply the same condition, but match on `subagent_type`, and also block the default (no type) and `general-purpose`. The deny text is `Delegation is on: use leo_dispatch(role: …) — see the leo-delegation section. Native agents are allowed only after a leo_dispatch fails.`
- **Fallback latch:** fallback turns on when either (a) no `state` has arrived within 10 s of `session.start`, or the bridge stream is down, or (b) a `mcp__leo__leo_dispatch` call returns an error result, which the mod sees in its `tool.call` hook after `next`. It turns off on the next successful `leo_dispatch`, or the next `state` while the stream is healthy. Every change shows a `$.ui.status` note.
- **Config:** add `delegation.hide_native_agents`, defaulting to `[implementer, implementer-hard, code-reviewer, Explore, Plan, general-purpose]` and editable in the web UI form. The web page's "running agents keep their prompt until restart" note becomes "bridged claude agents update live".

## 2. Fork consult

- Add `fork: bool` to the `leo_consult` schema. With `fork: true`, `template` and `model` are ignored. The tool description says: "same model, sees this whole conversation, no tools, cheapest; use for a sanity check of your own plan".
- The mod hooks `tool.call` on `mcp__leo__leo_consult`. When `input.fork` is set, it answers without calling `next`: `$.model.fork({ prompt })`. On `isAnswered` it returns `[consult · fork/<usage.model>] tokens in/out/cache-read` followed by the text. Otherwise it returns an error naming the `reason`.
- In Go, the `leo_consult` handler returns an error for `fork: true`: "fork consult needs a bridged claude session". The handler only receives the call when no mod answered it.
- No consult record or history entry is written for a fork consult, since it never touches the daemon.

## 3. Roster band + status line

- **`ui.render` on `AbovePrompt`:** when `$.state` has dispatches, draw one row per dispatch: status glyph, `name || role`, model, status, a `stalled` marker, elapsed time, `tokens_in/out`, cost, and a `Cancel` button for non-terminal dispatches. Pressing Cancel sends `request dispatch.cancel` and shows a toast with the result. With no dispatches, return `next(e)`.
- Elapsed time is `active_seconds` from the snapshot plus local time since the snapshot arrived, while status is running. A `$.clock.every(1000)` redraw ticks only while some dispatch is running.
- **`$.ui.status`:** `⇢ N running · M idle`, cleared when there are none.
- Steering stays in the dispatch's tmux pane. There is no steer UI.

## 4. Token usage + bridge notifications

- In the mod's `turn.complete` hook, read `usage` from `await next(e)` and add `tokens { input, output, cache_read, cache_creation, model }` to the `turn.complete` event report. Cost stays as it is today.
- The dispatcher adds these token counts to the dispatch's usage. When a bridged turn reports tokens, it no longer marks that turn incomplete.
- **Caller notifications:** when the caller agent has an adopted bridge, send the completion notification as a bridge `deliver` through the durable outbox, using the deterministic id `notify-<dispatch-id>-<turn>` so a redelivery is deduplicated. If the caller is unbridged, use the peer-inbox socket as today, and tmux injection for other harnesses, unchanged.

## Out of scope

- Steering from the band.
- Transparently translating `Agent` calls into dispatches.
- In-process different-model consults.
- Bridging headless dispatches.

## Testing

- **Mod (TS tests in `leo-bridge/tests/`):**
  - the `prompt.compose` section is present or absent, and invalidated on change
  - `agent.offer` hides only the listed types, and stops hiding when latched
  - `Agent` deny, and the allow after a failed `leo_dispatch`
  - fork consult success and error formatting
  - band rows and Cancel → request report
  - `turn.complete` carries tokens
- **Go:**
  - `state` snapshot contents and debounce
  - caller-scoped dispatch filter
  - `request dispatch.cancel` authorization (another agent's dispatch is rejected)
  - no delegation text in bridged launch argv, kept for unbridged (assert argv)
  - token accumulation clears the incomplete flag
  - notification routes through the bridge outbox with a stable id, and falls back to peer inbox
  - `fork: true` error in the Go handler
- **Live:** an isolated test daemon (own LEO_HOME and port, never the production daemon) with a bridged claude agent:
  - toggle delegation in the test web UI and watch the next turn's behavior change without a restart
  - check that `Agent(implementer)` is denied
  - run a fork consult
  - dispatch, then watch the band, Cancel from it, and see the completion arrive through the bridge
  - check `leo dispatch output` usage shows tokens

## Risks

- Whether `prompt.compose` re-fires after `$.ui.invalidate("prompt.section")` mid-session must be checked live first. If it doesn't, fall back to delivering a one-line note through `deliver` when the toggle changes, and keep the section accurate from the next session.
- `tool.call` result shape for an MCP tool answered by a mod: check against the types before building on it.
