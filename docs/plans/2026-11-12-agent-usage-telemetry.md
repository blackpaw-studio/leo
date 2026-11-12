# Plan: PR A — richer agent usage telemetry (Claude + git)

Consumer spec (source of truth): `/Users/evan/.leo/agents/leoterm/docs/superpowers/specs/2026-11-12-agent-telemetry-request.md`.
Base: `ff55774a`. Run every Go test with `env -u TMUX`.

## Wire contract (agreed with leoterm; exact names, all optional/omitempty, raw values only, no new event types)

- `usage.model` string — observed model from the mod. `Agent.Model` stays config-derived.
- `usage.effort` string — observed effort.
- `usage.context.breakdown` `{system, tools, mcp, memory, messages}` ints.
- `usage.context.compact_at` float fraction.
- `usage.rate_limits` `[{kind: five_hour|seven_day, percent_used, resets_at RFC3339 UTC}]`.
- `usage.git` `{added, removed}`.

Delivered via `/api/v1/state` `Agent.Usage`, `agent_usage` SSE, and usage on turn-completed events.

## Verified facts

1. The host's `usage({breakdown:'summary'})` returns `usage.context.breakdown = {categories:[{name,tokens,kind}], rawMaxTokens, autoCompactThreshold?, isAutoCompactEnabled}`.
   - Keep only rows with `kind==='used'`.
   - Map by name: `System prompt`→system; `System tools`/`Skills`/`Custom agents`/`Slash commands`→tools; `MCP tools`→mcp; `Memory files`→memory; `Messages`→messages. Any other name→tools, so the totals still sum to `/context`.
2. Host `rateLimits[].resetsAt` is a string. Parse it as RFC3339 and store UTC. Also accept a number as epoch ms.
3. `agent_turn_completed` carries only `cost_usd` and `context`, and `OnBridgeEvent` returns early on turn.complete.
4. `ContextUsage` is compared with `!=` (`bridgefeed.go:605`) and shallow-copied by `cloneOf`. New fields on it must be value types.
5. There is no `/api/v1` reference page. `agent_usage` is documented only in `docs/specs/2026-10-06-bridge-observe.md`.

## Decisions (orchestrator, final)

- **D1 compact_at** = `autoCompactThreshold ÷ context.window`, only when `isAutoCompactEnabled`. This matches the consumer spec ("÷ window") and `context.percent`. Omit it when disabled, absent, ≤0, or the window is 0.
- **D2 fold location:** the daemon, in Go. The mod forwards raw host JSON.
- **D3 git for agents without a bridge:** OUT of PR A. In PR A, `usage.git` appears only on agents that already have bridge usage. The existing `session_id/session/incarnation` serialization is unchanged (no breaking shape change). Git for agents without a bridge (codex) lands in PR B with codex usage.
- **D4 poller:** one daemon-wide poller.
  - Every 10 s tick, list running agents (skip suspended/stopped) and run one git call per unique workspace.
  - At most 4 calls in parallel, 5 s timeout each.
  - Skip a tick if the previous one is still in flight.
- **D5 merge:** BridgeFeed owns the git map (keyed by agent name, under `f.mu`) and is the only publisher of `agent_usage`.
  - `SetGitStats(map[string]GitStats)` diffs the map and publishes changed names.
  - Every bridge-driven publish includes the current git value.
- **D6:** after `agent_turn_completed`, also publish `agent_usage` when model, rate limits or effort changed.
- **D7:** add `-c core.fsmonitor=false` to the git argv.

## Steps (TDD: each starts from the named failing test)

1. **Types** (`internal/observe/snapshot.go`)
   - `AgentUsage` gains `Model`, `Effort string` (omitempty), `RateLimits []RateLimit` (omitempty), `Git *GitStats` (omitempty).
   - `ContextUsage` gains `Breakdown ContextBreakdown` (omitzero, all-int value type) and `CompactAt float64` (omitzero).
   - New type `RateLimit{Kind; PercentUsed float64; ResetsAt time.Time}`.
   - Failing test: `TestAgentUsageJSON` (table) checks exact names and omission.
2. **Fold** (`bridgefeed.go` `modUsage`/`foldUsage`, new `usagefold.go`)
   - Breakdown per fact 1; clamp negatives to 0.
   - compact_at per D1.
   - Rate limits: keep only the two known kinds, require a valid percent, parse resetsAt per fact 2, drop unparseable entries.
   - Model: cleaned `ClampDetail`-style; reject control characters.
   - `usage()` deep-copies `RateLimits`. Change detection uses `slices.EqualFunc` with `time.Equal`.
   - Failing tests (table-driven over a captured host JSON fixture): rows with `kind!='used'` excluded; unknown kinds dropped; an identical repeat publishes nothing; a malformed breakdown keeps cost and context.
3. **Effort/model reach agent state**
   - `case bridge.EventEffort` sets `st.effort` and marks usage changed.
   - `relaunch` clears effort and model.
   - Failing test: `TestBridgeFeedEffort` (publish once; a repeat publishes nothing; a relaunch clears it).
4. **turn.complete also publishes usage** per D6.
   - Failing test: new rate_limits on turn.complete emit both events; a cost/context-only change emits only `agent_turn_completed`.
5. **Mod** (`internal/harness/claude/bridgemod/leo-bridge/hooks/register.js`)
   - `turnCompleteReport`: `Promise.all([$.session.usage({breakdown:'summary'}), $.session.model()])`, send `{...u, model: <id string>}`. Check what `forkConsultAnswer` expects `model` to be.
   - `sendHello` resets `reportedEffort = null`.
   - Tests in `tests/observe.test.ts`. Extend `harness.ts` to record the arguments to `session.usage` and add a `model` option.
   - Failing tests: the usage call gets `{breakdown:'summary'}`; `model` is included; effort is re-sent after a reconnect; turn.complete is still sent when usage throws.
   - Run with `claude plugin test` (see `docs/specs/2026-10-06-bridge-observe.md:114` and `bridgemod/main_test.go`).
6. **Shortstat parser** (new `internal/observe/gitstat.go`, a pure function)
   - Failing test: `TestParseShortstat` covering empty input, insertions only, deletions only, both, singular/plural forms, and garbage→error.
7. **Git runner seam** (`gitstat.go`)
   - `CommandRunner` interface. The real implementation uses `exec.CommandContext`, env `os.Environ()+GIT_OPTIONAL_LOCKS=0`, sets `WaitDelay`, never a shell.
   - `GitStatsFor(ctx, runner, ws)` applies a 5 s timeout.
   - Failing test (recording fake) asserts:
     - exact argv `git -C <ws> -c core.fsmonitor=false diff HEAD --shortstat`
     - env contains `GIT_OPTIONAL_LOCKS=0`
     - deadline ≤5 s
     - exit 128 or no HEAD gives `ok=false`, logged at debug, no error.
8. **Feed composition** (`SetGitStats`, `GitStats()`, every publish path)
   - Failing tests: a bridged `agent_usage` carries git; a later bridge report keeps git; a git change keeps cost/context/effort; removal publishes once; an unchanged map publishes nothing; git for an agent without a bridge entry is held but not published (D3).
9. **Poller** (new `internal/observe/gitpoller.go`)
   - `NewGitPoller(lister, runner, feed, clock, opts)` with `pollOnce`/`Run`. Inject the clock; no real sleeps.
   - Failing tests: two agents sharing a workspace → one call; a tick during an in-flight poll is skipped; at most 4 concurrent calls; a runner error → no git for that agent.
10. **Snapshot merge** (`internal/web/handlers_observe.go`)
    - The `/api/v1/state` claude agent shows the merged fields, including git.
    - Failing test in the existing handler test.
11. **Wiring** (`internal/service`, where `NewBridgeFeed` is constructed)
    - The lister comes from the supervisor's live agents and their `WorkDir`. Start `poller.Run(daemonCtx)`.
    - No unit test: verified live.
12. **Docs**
    - Add an "Agent usage fields" table to `docs/configuration/api-clients.md`: field, source, which harness fills it.
    - Add an addendum to `docs/specs/2026-10-06-bridge-observe.md`.

## Verification

- Targeted runs:
  - `env -u TMUX go test -race -run 'Usage|Fold|Effort|Shortstat|GitStat|GitPoller' ./internal/observe/`
  - `env -u TMUX go test -race ./internal/web/ ./internal/service/ ./internal/bridge/`
- Mod: `claude plugin test` in the leo-bridge dir.
- `make lint`, then `env -u TMUX make test` (use `heavy` in the background if it runs longer than 2 min).
- Live check in an isolated `LEO_HOME` test daemon. NEVER restart production.
  - Capture one real turn.complete usage JSON to confirm facts 1 and 2.
  - `curl /api/v1/state` and watch the events stream while a claude agent edits a file.
  - Git should update within 10 s; nothing is published while the workspace is idle.
