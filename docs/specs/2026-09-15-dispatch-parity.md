# Dispatch parity: completion, usage, isolation, continuation, output

Extends [dispatch roster](2026-09-14-dispatch-roster.md) and [interactive dispatch](2026-09-14-interactive-dispatch.md).
Code baseline: `3e1f98f`. Headless remains the default.

## Goal

Bring Leo dispatch closer to Claude Code native subagents: wake the caller when work finishes, expose usage, isolate changes, continue completed headless sessions, and retrieve recorded output.

## Existing contracts and required corrections

- `Record` already has `SessionID`, `Turns []Turn`, `ActiveSeconds`, and `RunningSince`; `Entry` has turn outcome/delivery fields and custom JSON marshaling.
- `Record.turns` is an existing history array. Preserve it; name the new numeric usage counter `usage_turns`.
- `harness.Result.SessionID` already exists and all three adapters populate it; `Dispatcher.run` currently discards it.
- `web.Options.ParentContext` is the daemon lifetime, **not caller identity**.
- Codex already resolves linked-worktree `gitdir` and `commondir` into writable roots in `internal/harness/codex/sandbox.go`.
- OpenCode headless resume already works through `SessionArgs`; notification injection needs new composer support.
- `leo_wait` currently has no result-text cap. The MCP client's 10 MiB response-reader limit is a separate transport limit.

## Shared data and interfaces

Extend `consult.Request` and dispatch transport DTOs with `Notify *bool` and `Isolation string`; wire `notify` and `isolation` through MCP, HTTP, and CLI. Omitted notification means enabled for dispatches; synchronous `leo_consult` does not notify.

Persist these additions on `Record`:

| JSON field | Contract |
|---|---|
| `notify` | Resolved boolean for newly created dispatches |
| `caller_pane_id`, `caller_harness`, `caller_session_id` | Captured caller pane, harness, and tmux session identity; separate from the dispatched harness's `session_id` |
| `input_tokens`, `output_tokens`, `cost_usd`, `usage_turns`, `tool_calls` | Optional numeric usage values; absent means unknown, zero means measured zero |
| `isolation`, `worktree`, `branch`, `base_commit`, `source_cwd`, `repository_root` | Isolation mode, managed path, branch, starting commit, original cwd, repository root |
| `worktree_state` | `creating`, `present`, `removed`, or `kept`, for recovery and continuation |
| `notifications` | Transition-keyed delivery ledger, with pending/claimed/suppressed/delivered/failed dispositions |

`Entry` gains the five usage fields plus optional `worktree` and `branch`. Update both custom JSON methods in `entry.go`, not just struct tags. Usage in entries is cumulative for the run; turn text/outcome remains specific to the selected turn.

New public surfaces:

- `leo_dispatch`: optional `notify` and `isolation`, where isolation accepts only omitted/empty or `"worktree"`.
- `leo dispatch run --notify=false --isolation worktree`.
- `leo_dispatch_output {id, tail?: N}`.
- `GET /api/dispatch/{id}/output?tail=N`.
- `leo dispatch output <id> [--tail N]`.

These are request options, not template defaults. No new `leo.yaml` section or harness `OptionsSchema` keys are needed. Update MCP input schemas, API DTOs, MCP client methods, CLI flags/help, and configuration documentation.

## 1. Completion notification

### Caller lookup

Today MCP sends `processName` as HTTP `from`, which becomes `Request.Caller` and `Record.Caller`. `web.go` resolves that name through live `processes.States()` and `agent.SessionName(caller)`.

At acceptance, resolve the caller's **primary pane** using the session's `@leo_primary_pane` option, maintained by `internal/service/process.go`. Capture its tmux session ID and resolved caller harness. Never use the active window: it may be a dispatch Viewer.

CLI dispatch currently omits `from`; forward the same process identity when available. Support an explicit originating `caller_pane_id` in the transport for nested interactive dispatches and callers outside supervised agents; MCP/CLI obtain it from their originating `TMUX_PANE`. Validate pane membership and capture its session identity. With neither a resolvable caller nor a supplied pane, dispatch succeeds but notification is unavailable.

Before delivery, verify the captured pane still belongs to the captured session. Do not redirect a stale notification to a restarted caller. `Record.PaneID` identifies the **subagent**, not its caller.

### Transition and suppression rules

Generate one completion candidate when a headless invocation terminates or an interactive turn acquires an outcome. Include failed launch, cancellation, timeout, rejection, and lost-turn outcomes. An interactive session closing after a previously completed turn does not repeat that turn's notification.

Use this exact single-line format:

```text
[leo] dispatch <id> (<name>) <status> · active <dur> — collect with leo_wait
```

Use `d-…#n` for turn completions, otherwise the run ID; name falls back to template. Sanitize controls/newlines. Duration uses the roster's `m:ss`/`h:mm:ss` formatter and cumulative active time at the boundary.
For turn outcomes, render `finished→done`, `rejected/lost→failed`, and `interrupted→canceled` or `timeout` when applicable. Do not change persisted run statuses: a completed interactive turn may leave its Record `idle`.

Register blocking waits and resolve run IDs to their target turns atomically under `Dispatcher.mu`. Maintain reference counts keyed by resolved run/turn transition; unregister on every return/cancellation path. A wait for an older turn does not cover a newer turn.

At completion, suppress permanently if a covering wait is registered. Otherwise enqueue one candidate. Recheck coverage before delivery so a later wait can collect a pending completion. Duplicate reports, sweeps, and cancellation/process-exit races reuse the same ledger key.

### Delivery

- Claude: reuse `peerinbox.ResolveSocket` with the exact caller pane, then `peerinbox.Deliver`. It resolves `#{pane_pid}` and descendants and writes `{"type":"user","message":{"role":"user","content":"…"}}\n` to `/tmp/cc-socks/<pid>.sock`, with existing alternate socket directories.
- Claude launches already include `--settings {"crossSessionInbound":"accept"}` in `claude/args.go`; preserve it when merging hooks/settings.
- Codex/OpenCode: use `tmux.InjectInto` against the caller pane. Add OpenCode passive composer classification and paste confirmation; existing confirmation supports only Claude/Codex, and OpenCode's permissive probe classifier is unsuitable.
- Busy, draft, or unknown composer: leave pending without keystrokes and retry on the existing sweep. Serialize delivery per caller pane.
- Perform socket/tmux I/O outside `Dispatcher.mu`. Persist a claim before any possible submission. Retry only failures proven to have submitted nothing; never retry ambiguous writes/Enter failures.

Delivery is **at most once**, best effort: the socket protocol has no acknowledgment or deduplication key. A crash after claiming can lose a notification. Recovery never resends a claimed transition; failures remain logged and collection remains available.

## 2. Usage accounting

Add an optional usage structure to `harness.Result` and an incremental adapter accumulator used by `recordingTee`. Feed complete native lines once; update live Record usage under the dispatcher lock. Reuse the accumulator for `ParseEvents` so live and final accounting agree.

Keep per-invocation totals separate from completed-invocation totals. Final authoritative totals replace provisional values for that invocation; never add both. Preserve measured usage on failure/cancellation and never turn missing data into zero.

| Harness | Sources and counting |
|---|---|
| Claude | Result `usage`, `total_cost_usd`, `num_turns`; assistant `message.id`, `message.usage`, and `message.content` tool-use blocks for provisional input and tool counts |
| Codex | `turn.completed.usage.input_tokens`/`output_tokens`; count `turn.started` for native turns; count unique tool item IDs across `item.started`/`item.completed` |
| OpenCode | `step_finish.part.tokens`, unique `step_start.part.id`, and tool-use part IDs; absent final step usage remains unknown/incomplete |

Claude assistant events sharing `message.id` must not double-count usage; deduplicate tool blocks by tool-use ID. Result output usage is authoritative because per-step output counts can be placeholders. Claude result fields include error results. [Claude usage contract](https://code.claude.com/docs/en/agent-sdk/cost-tracking), [result events](https://code.claude.com/docs/en/agent-sdk/agent-loop).

Normalize input totals to include cache-read/write inputs where reported separately; Codex cached input is already a subset of `input_tokens`. OpenCode output includes separately reported reasoning tokens. Do not add subset counters twice. `cost_usd` comes only from Claude's `total_cost_usd`; never estimate other harness costs.

Codex exec fixtures use typed tool items such as `mcp_tool_call`, not generic `function_call` events. Count execution, MCP, search, and file-change tool items once by ID; exclude messages, reasoning, and task lists. A separate rollout parser may recognize `function_call`, but must not count the same call through two sources.

`usage_turns` means the harness-native count above, distinct from Leo's `Turns` history; its granularity differs by harness. Interactive hooks currently provide no complete usage stream: leave unsupported counters absent rather than infer tokens from text or screen captures. Claude token usage is reported top-level usage; cost can include native nested agents.

Expose totals in wait entries and API Record JSON. Append CLI list columns `INPUT OUTPUT COST_USD USAGE_TURNS TOOLS`, using `—` for unknown; retain existing `TURNS=len(record.Turns)`.

Roster suffix, independently omitting unknown components:

```text
✓ reviewer 1:23 · 12 tools · 15.4k tokens
```

Tokens are `(input_tokens + output_tokens)/1000`, one decimal, including `0.0k`; show only when both totals are known. Retain 16-character labels, ordering, colors, and three-space entry separators.
Preserve current width behavior: render the full roster and let tmux clip at the status-line edge; do not wrap or introduce another status line. Usage changes invalidate the existing text cache.

## 3. Worktree isolation

Allocate the dispatch ID and prepare isolation **before** building adapter argv: `Start` currently builds argv before allocating its Record, which would miss worktree Git metadata.

Resolve the repository and current HEAD from request cwd. Reject non-repositories and unborn HEADs. Create `<state>/worktrees/<id>` on `leo/<sanitized-name-or-template>-<hex4>`:

```text
git -C <repository_root> worktree add -b <branch> <path> <base_commit>
```

Use a Git-safe bounded slug, validate with `git check-ref-format --branch`, and regenerate suffixes on collision. Pass separate argv elements, never shell interpolation. The run cwd and `LaunchSpec.Workspace` become the worktree root. Starting content is committed HEAD; source-worktree edits are not copied.

Persist `creating` metadata before the Git operation and `present` before launching. Isolation metadata writes are required for isolated runs; do not degrade to an unrecorded worktree. Never fall back to running in the source checkout after preparation failure.

Codex's existing `gitMetaDirs` follows `.git` → `gitdir` → `commondir`. Build argv after creation and assert writable roots include both `<main>/.git/worktrees/<entry>` and `<main>/.git`, preserving the existing `.agents` grant.

### Collection and recovery

Serialize cleanup against send/resume and other collection calls. Clean up only once no process or interactive pane can write; headless cancellation currently publishes terminal status before process reaping, so status alone is insufficient.

Remove with non-forced `git worktree remove` only when `git status --porcelain` is empty, no commits exist in `<base_commit>..HEAD`, and HEAD still equals the recorded base. Any Git uncertainty, changed branch identity, removal refusal, dirt, or commits means keep.

Keep the branch even after clean worktree removal, allowing continuation to recreate the same path. Never force removal or reset changes. Entries for retained worktrees include `worktree` and `branch`; persist cleanup disposition before returning the entry.

Apply cleanup on collection of all terminal statuses, not just `done`. Existing `Wait` collects only `done`; extend it. Interactive turn collection while the session remains live does not remove its worktree.

On cancel, reap/kill first, then apply the same clean-or-keep rule. On restart, extend `MarkInterrupted`: settle interrupted turns, verify writers are gone, reconcile `creating` records with Git's worktree inventory, then clean or retain. Uncertain/orphaned writers defer cleanup.

Retained worktree records are exempt from ordinary `RecordsKept=20` pruning until the worktree is removed. Never prune user changes or erase their recovery metadata.

## 4. Headless continuation

Allow `leo_send_dispatch` on terminal headless dispatches with a captured session ID and available workspace. Reject active runs, missing session IDs, missing retained worktrees, and unsupported adapter capabilities before mutation. Interactive send behavior remains unchanged.

Use existing `LaunchSpec.Session = {Mode: SessionResume, ID: record.SessionID}`:

| Harness | Verified resume shape and ID source |
|---|---|
| Claude | `claude -p … --resume <id> <new-prompt>`; result `session_id` |
| Codex | `codex exec <exec-options> resume <id> <new-prompt>`; `thread.started.thread_id` |
| OpenCode | `opencode run --format json … -s <id> <new-prompt>`; event `sessionID` |

The adapters already build these forms. Codex exec-only sandbox/approval flags stay before `resume`; preserve `--json`. Installed `codex exec resume --help`, `opencode run --help`, and adapter argv tests confirm these contracts. OpenCode needs no new server API for this path.

Create an opening headless `Turn` as `#1`; each accepted continuation appends `#n`. Snapshot run-ID waits to the latest orchestrator turn for both modes; old turn-ID waits must never switch to the new invocation.

Under the lock, reserve capacity and claim the next invocation. Clear current `Text`, `Error`, `EndedAt`; transition through `queued` to `running` only after process start succeeds. Keep `StartedAt`, session identity, history, and accumulated active time. Apply the configured timeout separately to each invocation.

Each invocation owns its cancellation context and completion channel. Waiters retain their selected channel/turn; never close or replace a channel still owned by another invocation. Cancellation must be fully reaped before resume. Process completion consumes its slot exactly once and emits one notification candidate.

Reopen the stream in append mode: `FileRecorder.Open` currently uses `O_EXCL`, and closed handles ignore updates. Add an explicit recorder resume operation preserving timestamps and increasing lifecycle sequence numbers. Append turn/status events and retain previous output.

Persist parsed session IDs even on unsuccessful invocations when available. For records loaded after restart/eviction, rebuild execution using the recorded harness/model and current template options/environment; reject missing templates or a changed harness. Do not persist secrets in launch metadata.

For clean removed worktrees, recreate the same path from the retained branch only if it still points to `base_commit`; otherwise reject. For kept worktrees, resume in place without resetting changes.

## 5. Output access and wait limits

Extract `rendererFor`, `consultFeed` event rendering, and offset formatting from `internal/cli/consult.go` into shared consult rendering code. Preserve watch's `%7s  %-8s %s` rows and 18-space continuation indentation.

Output reads `<state>/dispatches/<run-id>.ndjson` through `StreamPath` and `DecodeEvent`, renders complete envelopes, and returns `{id, lines, truncated}`. CLI prints those lines. Render without terminal color/control sequences.

`tail` defaults to 60; require a positive integer and clamp above 400. Count rendered physical lines, not native events, using a bounded line ring. Ignore an incomplete trailing envelope; malformed complete envelopes follow watch's skip behavior. Missing streams produce an explicit recording-unavailable error.

Resolve IDs before constructing paths; support turn IDs by resolving their parent run and returning that run's stream. Output is a nonblocking snapshot available while running and has no collection side effect. In particular, do not reuse the existing GET-record handler, which calls `Collect`.

Cap each returned wait entry's text at **32,768 UTF-8 bytes including its note**. Preserve a valid UTF-8 prefix and append `[truncated; collect recorded output with leo_dispatch_output {"id":"<run-id>"}]`. Apply at the shared wait-entry boundary so MCP, API, and CLI agree; bound error fallback text too. Persist full Record/Turn text and stream output unchanged.

## Persistence, testing, and build order

Use existing atomic JSON replacement and permissions. Missing usage fields remain unknown; missing isolation means shared cwd; existing `turns` arrays and `session_id` remain valid. Do not retroactively notify already-terminal legacy records.
Lazily synthesize headless `#1` history when continuing a legacy record, preserving its result/outcome. Update `cloneRecord` for new pointer/slice/map fields. Persist resumed stream sequence state or recover it from complete lifecycle envelopes.

Write failing tests first, including:

- Notification/wait races, multi-ID coverage, duplicate hooks, cancel/exit races, restart claims, stale caller panes, exact socket envelope, and no keystrokes into drafts.
- Real temporary Git repositories: dirty/untracked/committed work, source subdirectories, branch collisions, interrupted creation, cancel/restart cleanup, collection/send races, and clean-worktree recreation.
- Exact adapter/Git/tmux argv assertions, especially Codex resume option ordering and linked-worktree common-dir writable roots.
- Usage fixtures: duplicate native IDs, cumulative versus provisional values, zero/unknown, failed/truncated streams, cache accounting, and accumulation across resumes.
- Old/new Record and Entry JSON round trips; turn snapshot waits; append-only streams; slots and active time across continuation.
- Rendered-line tails, multiline events, partial NDJSON, invalid IDs, UTF-8 truncation, output without collection, and unchanged watch rendering.
- `e2e/` daemon/tmux/fake-harness cases following `interactive_dispatch_test.go` and `dispatch_viewer_test.go`; isolated live checks for Claude inbox wakeup, Codex/OpenCode composer delivery, harness resume, and Codex worktree writes.

Build order. Features 1, 3, and 5 overlap in `consult.go`, Record/Entry/lifecycle integration, `mcp/tools.go`/`client.go`, `web/handlers_consult.go`, and `cli/dispatch.go`, so they are not independent PRs.

1. Serial foundation: shared Record/Request/Entry contracts, caller identity, transition keys, collection serialization, and transport schemas.
2. Parallel implementation with partitioned ownership: notification helpers, worktree helpers, and shared output renderer/reader. Integrate shared call sites serially.
3. Usage accounting, including adapters, live accumulation, CLI, and roster.
4. Headless continuation, integrating session capture, append recording, turn waits, worktree recreation, and notification generations.
5. Fresh-context review, address findings, then `make test`, `make lint`, `make build`, and applicable `make e2e` cases.

## Decisions on the planner's open questions

- The scalar harness-native turn counter is `usage_turns`; `turns` stays the Leo turn history.
- Notification delivery is at most once. Neither the inbox socket nor the tmux composer offers an acknowledgment, so exactly-once is not available; `leo_wait` remains the reliable collection path.
