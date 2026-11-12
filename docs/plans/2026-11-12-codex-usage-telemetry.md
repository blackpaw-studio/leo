# Plan: PR B — codex agent usage telemetry

- Base: `main` @ `1f9b8a6d` (PR A #331).
- Run every Go test with `env -u TMUX`.
- Release stays ON HOLD until Evan says otherwise.
- Consumer spec: `/Users/evan/.leo/agents/leoterm/docs/superpowers/specs/2026-11-12-agent-telemetry-request.md`.
- PR A plan: `docs/plans/2026-11-12-agent-usage-telemetry.md`.

## Verified facts

### Rollout shapes (key shapes only, from 40 recent rollouts)

- `turn_context.payload.{model,effort}` are strings.
- `token_count.info = {last_token_usage, total_token_usage, model_context_window}`. Each of the two usage objects has `input/cached_input/output/reasoning_output/total_tokens`.
- `token_count.rate_limits = {limit_id:"codex", primary, secondary, …}`.
  - Each window is `{used_percent 0–100, window_minutes, resets_at}`.
  - `resets_at` is in **epoch seconds**.
- Lines can reach about 143 KB.

### Field meanings

- **Slots don't map to windows.** `primary` can hold the 10080-min window while `secondary` is null. Map rate limits by `window_minutes`: 300→`five_hour`, 10080→`seven_day`. Drop any other window.
- **Context used = `last_token_usage.total_tokens`.** This matches the codex TUI status card.
  - `total_token_usage` is cumulative, so it is the wrong counter.
  - Report raw values: `window = model_context_window`, `percent = tokens/window*100`.
  - Apply no 12k baseline.

### Finding the rollout

- `CODEX_HOME` comes from `codex.CodexHome(h.Env)`.
- `AgentService.ResolveHandle(name)` returns harness, Env, Workspace and IDs (the agentstore `SessionID`).
- `Codex{}.SessionFiles(root,"",[id])` locates `rollout-*-<id>.jsonl`.
- The id is stored only after the first message leo delivers (`tmuxtui.maybeDiscover`).

### Consumers

- leoterm `LeoAgentUsage` requires `session.tokens`. It treats `cost_usd`, `session_id` and `incarnation` as optional.
- leoterm drops the whole usage object when `session` is missing.
- leoterm hard-codes `reportingHarnesses` (`LeoPluginAppSources.swift:61`). The orchestrator tells leoterm to add codex.
- Den never reads `usage`.

## Decisions (final)

**D1. Locate the rollout.**
- Primary: the stored `SessionID` → `SessionFiles` under `CodexHome(h.Env)`. Cache the path per (home, id) and re-walk only when stat fails.
- Fallback when no id is stored: the newest rollout whose `session_meta.cwd` equals the workspace and whose mtime ≥ the agent's `StartedAt`.
  - Walk only the date dirs from StartedAt−1 day to today.
  - Re-walk at most every 60 s.
  - Use the result only when exactly one rollout matches.
  - Never persist the id.
- The `/new`-inside-codex stale id is an accepted risk and a follow-up.

**D2. Snapshot entry: `BridgeFeed.SetHarnessUsage(map[string]AgentUsage)`.**
- Modelled on `SetGitStats`: replace the map, diff it, and publish `agent_usage` under `f.mu`. BridgeFeed stays the single publisher.
- Bridged usage wins when an agent has both.
- `BridgeAgents()` includes harness-usage agents with `Bridge` empty, so it is omitted on the wire.
- `SetGitStats` publishes for agents with bridge usage or harness usage.
- A removed agent is dropped silently.

**D3. Wire shape.**
- Keep `session_id`, `session` and `incarnation` present.
- Codex fills `session.tokens` and `incarnation.tokens` and OMITS `cost_usd`.
- How: `UsageTotals.CostUnknown bool \`json:"-"\`` plus a `MarshalJSON` that drops `cost_usd` when it is set.
- The type must stay comparable.
- Claude output must stay byte-identical (it still emits `cost_usd:0`).
- No existing payload changes, because codex usage was never emitted before.

**D4. Scope limits.**
- Dispatches (consult records) are out of scope; follow-up.
- opencode is out of scope. It would need a fake `session`, and leoterm requires `session`.

## Token accounting (`codexRolloutFold`)

- `delta = total_tokens − prev`.
  - A negative delta means codex reset its totals on resume; in that case count `total_tokens` from zero.
- `session.tokens` is the sum of deltas over the current rollout.
- `incarnation.tokens` is the sum of deltas from lines whose `timestamp` ≥ the agent's `StartedAt`. This survives a daemon restart. When `StartedAt` changes, reset it to 0.
- A new rollout path resets session tokens and context, and clears model and effort until the next `turn_context`.
- Rate limits survive both resets.
  - A null `rate_limits` keeps the last values.
  - Accept only `limit_id` empty or `codex`.
- A null `info` updates rate limits only.

## TDD steps (each starts from the named failing test)

1. **Wire** (`internal/observe/snapshot.go`): `CostUnknown` + `MarshalJSON`.
   - Test: `TestUsageTotalsJSON`.
     - Claude zero cost emits `cost_usd:0`.
     - `CostUnknown` omits it.
     - Round-tripping a claude payload is byte-identical.
2. **Fold** (new `internal/observe/codexrollout.go`, pure): `AddLine([]byte)`, `Usage(sessionID, startedAt)`.
   - Test: `TestCodexRolloutFold`, table-driven over a SANITIZED synthetic fixture at `internal/observe/testdata/codex-rollout.jsonl`. The repo is public.
   - Cases:
     - model and effort; a missing effort is omitted
     - context uses `last.total_tokens`
     - window 0 omits context
     - window mapping, including weekly in `primary`; 60 is dropped
     - `resets_at` seconds → UTC
     - a percent outside 0–1000 is dropped
     - null `rate_limits` keeps the previous values
     - total-reset delta
     - incarnation timestamp filter
     - garbage lines ignored
     - reuse `cleanModel`
3. **Tailer** (new `codextail.go`): `rolloutTail{path, offset, partial, skipping}`, `Read(fsys, maxBytes)`.
   - Test: `TestRolloutTail_RealFile`, against a real `t.TempDir()` file.
   - Cases:
     - an append reads only the new bytes
     - a partial last line is held back
     - a line over 4 MiB is skipped without growing memory
     - truncation or replacement resets the offset to 0
     - a missing file gives ok=false
     - reads are capped at 8 MiB per tick
     - prefilter: only `"token_count"`/`"turn_context"` lines are decoded
4. **Locate helpers** (`internal/harness/codex`): exported `RolloutPath(home, id)` and `LatestRolloutFor(home, workspace, since) (path, id string, matches int)`.
   - Refactor these out of `discoverSessionID`, keeping its behaviour.
   - Test: `TestLatestRolloutFor_DateDirsOnly`.
5. **Codex usage poller** (new `internal/observe/codexpoller.go`): per agent and per tick, locate, then tail, fold, and collect into `SetHarnessUsage`.
   - Use the injected clock, a 10 s tick, and skip overlapping ticks.
   - Keep the last value on transient errors, as the git poller does.
   - Return silently on a cancelled ctx.
   - Tests:
     - a stored id → usage
     - a no-id fallback with one match → usage; with two matches → none
     - a locate failure omits the agent
     - an unchanged file triggers no reread
     - one real temp-file end-to-end case through the poller and a fake sink
6. **Feed** (`bridgefeed.go`): `SetHarnessUsage`, `f.harness`, `BridgeAgents` inclusion, `SetGitStats` for harness agents.
   - Test: `TestBridgeFeedHarnessUsage`.
     - publishes once
     - an identical repeat publishes nothing (deep-equal `RateLimits`/`Context`)
     - git rides along
     - a git change publishes
     - bridged usage wins
     - `Bridge` is omitted
7. **Snapshot** (`internal/web/handlers_observe.go`, test only).
   - A codex agent shows usage without `cost_usd` and with no `bridge` key.
8. **Wiring** (new `internal/web/codex_usage.go`; `consult_runtime.go` next to `startGitPoller`).
   - Lister: `agentSvc.List()`, running agents only, then `ResolveHandle`, then keep `harness=="codex"`.
   - Add `<-codexDone` to shutdown.
   - Test: `TestCodexUsageLister`.
9. **Docs**.
   - Update the `docs/configuration/api-clients.md` usage table with codex columns and a note that `cost_usd` is omitted when unknown.
   - Change the git row to "claude/codex agents with usage".
   - Add an addendum to `docs/specs/2026-07-31-observability-api.md`.

## Verification

- Targeted:
  - `env -u TMUX go test -race -run 'UsageTotals|CodexRollout|RolloutTail|CodexUsage|HarnessUsage|GitPoller|BridgeFeed' ./internal/observe/`
  - `env -u TMUX go test -race ./internal/web/ ./internal/harness/codex/`
- `make lint`.
- `env -u TMUX make test` once at the end (`heavy` in the background if it runs over 2 min).
- Live check, ISOLATED `LEO_HOME` test daemon only. Use its own port and stop it by killing its PID. NEVER run `leo service restart`, never touch production, never run `make e2e` from a dispatch pane.
  1. Spawn a codex agent (`templates.codex`, explicit model) and send one leo message.
  2. Watch `/api/v1/state` and the events stream.
     - model, effort and context must match codex `/status`
     - rate-limit kinds and `resets_at` must be sane
     - there must be no `cost_usd`
  3. Edit a file: git must appear within 10 s.
  4. Type a turn directly into the pane of a fresh agent with no stored id: the fallback must work.
  5. Restart the test daemon: incarnation tokens must not double.

## Accepted risks (PR body)

- The `/new` stale id.
- The per-tick agentstore load in `ResolveHandle`.
