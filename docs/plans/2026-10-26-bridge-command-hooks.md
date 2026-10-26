# Plan: bridge command hooks (spec `docs/specs/2026-10-26-bridge-command-hooks.md`)

## What the code shows (these shape the steps)
- **The four passed-over subscriptions have consumers only for agents.** `BridgeFeed` drops dispatch bridge keys (`internal/web/consult_runtime.go:290`). `HookPayload` maps only turn.start, turn.complete and session.end to the dispatcher. So a dispatch loses only `classic.Stop` pending work, and its Stop command hook already exists. A dispatch's delegation is blank by design (`consult/bridge_state.go:164`).
- **Agents already get some command hooks.** `AttentionHooks` gives them Stop, UserPromptSubmit, SessionEnd, Notification(`permission_prompt|elicitation_dialog`), PostToolUse and PermissionRequest (Leo Chat). The hook route sets needs_input **without a reason**. Only the bridge sends the reason (kind, tool, summary).
- **The mod passes more than its event registrations.** `$.session.append` is the delegation-change note (`register.js:894`), and `$.ui.invalidate('prompt.section')` is at :788. Both go when told state moves to the daemon.
- **Schemas confirmed in the CC 2.1.310 bundle:**
  - Every hook input carries session_id, prompt_id?, agent_id? and agent_type?.
  - `SessionStart.source` is one of startup, resume, clear, compact, fork.
  - SessionStart and UserPromptSubmit both accept `hookSpecificOutput.additionalContext`.
  - `SubagentStart{agent_id,agent_type}`, `SubagentStop{agent_id,…}`, `Elicitation{mcp_server_name,…}`, `PreCompact{trigger}` and `PostCompact{trigger,compact_summary}`. PreCompact and PostCompact triggers are manual|auto.
  - All of these also exist in 2.1.292.
  - The org type is at `oauthAccount.organizationType` (claude_team / claude_enterprise).
- **This host has `/Library/Application Support/ClaudeCode/managed-settings.json` = `{"prependPlugins": []}`.** So the guard is currently unseated everywhere, team accounts included.

## Steps (each one starts from the named failing test)
0. **Live spike, no code.**
   - Goal: before any build, confirm the facts the plan rests on. With the guard seated:
     - (a) SessionStart and UserPromptSubmit `additionalContext` reaches the model;
     - (b) SubagentStart/SubagentStop, PreCompact/PostCompact, Elicitation, PermissionDenied and PostToolUseFailure command hooks fire;
     - (c) whether PreCompact/PostCompact fire for the mod's own `$.session.compact()`.
   - Method: a scratch `--settings` with hooks that append stdin to a file. Uses the live-gate seat procedure below.
   - Verify: the hook log, plus asking the model to quote the injected marker.
1. **Agent hook route: attention with a reason.**
   - Files: `internal/web/handlers_attention.go`; `internal/observe/attention.go` (add `SetByTokenNeedsInput(token, AttentionReason)`); new `internal/observe/toolsummary.go`, a Go port of `protocol.js` `toolSummary`/`toolActivity` (Bash first word, path tools home-abbreviated, pattern, WebFetch host, capped).
   - Mapping: PermissionRequest → needs_input{permission, tool, summary}. Elicitation → needs_input{elicitation, mcp_server_name}. PostToolUse, PostToolUseFailure and PermissionDenied clear needs_input. Any payload with `agent_id` set is ignored, matching the mod's main-loop filter.
   - Failing tests in `handlers_attention_test.go`: POST `/api/agent/hook` with each payload, then assert the attention state and reason. Also: a Notification after Elicitation keeps the reason; a subagent's PermissionRequest changes nothing.
   - Verify: `go test -race ./internal/web/ ./internal/observe/`
2. **Agent hook route: subagent count and compaction.**
   - Files: `handlers_attention.go`; `observe/bridgefeed.go` (public methods that set an agent's subagent count and publish compaction, for the agent's bound key and launch); a per-token subagent-ID set that dedups repeats and dies with the token.
   - Mapping: SubagentStart/SubagentStop update the count, which `/api/v1` `subagents` and `Outstanding` read. PreCompact becomes `agent_compaction` started; PostCompact becomes completed, with the trigger.
   - Failing tests: start a, a, b → 2; stop a → 1; a new launch token → 0. A bridge hello without `subagents` keeps the hook count (`bridgefeed_test.go`). Compaction events are published with `ContextPercent`.
   - Verify: `go test -race ./internal/web/ ./internal/observe/`
3. **Delegation content and told state, in pure Go.**
   - New `internal/web/delegation_context.go`:
     - `contextFor(event, source, current, told *DelegationState) (text, nextTold)`;
     - a Go port of `delegationNote` and `sameDelegationText`;
     - a told store keyed by agent and session ID, persisted atomically under `state/` and pruned by age with an injected clock.
   - Rules:
     - SessionStart with delegation on: emit the section.
     - SessionStart with delegation off, where the told state was on: emit the OFF note.
     - UserPromptSubmit: emit the note only when the text differs from the told state. No told state counts as "never told".
   - Failing table test covers every source, on/off flips, and a repeat UserPromptSubmit that emits "". A round-trip test reloads the store from disk.
   - Verify: `go test -race -run Delegation ./internal/web/`
4. **Route `POST /api/agent/context {token, payload}` → `{context}`.**
   - Resolves the token to an agent, reads `delegationSource.Get()` and updates the told store.
   - Accepts any session ID that comes with a valid token, because a SessionStart after /clear names a new ID (see Risks).
   - Unknown token → 200 with an empty context.
   - Failing httptest: SessionStart(startup) → section; UserPromptSubmit → ""; config change → UserPromptSubmit → note once; then SessionStart(clear) with a new ID → section.
   - Verify: `go test -race ./internal/web/`
5. **CLI `leo dispatch context` (hidden, next to report/permission).**
   - Reads stdin, posts with `LEO_ATTENTION_TOKEN` and `LEO_WEB_PORT`, and prints `{"hookSpecificOutput":{"hookEventName":<from payload>,"additionalContext":…}}` only when the context is non-empty.
   - Fails open: daemon down, timeout, or `LEO_DISPATCH_ID` set → no stdout, exit 0, one stderr line. The deadline is about 3s, injected.
   - File: `internal/cli/dispatch_context.go`.
   - Failing tests: exact stdout bytes against an httptest daemon (via `t.Setenv`); the down, dispatch and empty cases.
   - Verify: `go test -race -run DispatchContext ./internal/cli/`
6. **Agent launch wiring.**
   - Change: `AttentionHooks` (`settings.go`) adds:
     - report-command groups for PermissionRequest (alongside the existing permission command), Elicitation, PostToolUseFailure, PermissionDenied, SubagentStart, SubagentStop, PreCompact and PostCompact;
     - SessionStart (matcher `startup|resume|clear|compact`) and UserPromptSubmit groups running `<leo> dispatch context`, derived from the report command the same way `permission` is.
   - Failing tests:
     - `settings_test.go` decodes the `--settings` JSON and asserts every event, matcher, command and timeout, and that Stop is present ("agents get it too");
     - `service/attention_launch_test.go` asserts the final agent argv through `attentionLaunchArgs`, including the spill-file path. No mocked exec.
   - Verify: `go test -race ./internal/harness/claude/ ./internal/service/`
7. **Dispatch path: Stop pending without the bridge copy.**
   - Failing tests in `consult/interactive_ids_races_test.go`, for a bridged run with a monitor and a session_cron pending (shell is excluded so the tracker cannot mask the result):
     - (i) hook Stop (prompt_id, background_tasks) then bridge turn.complete (bridge_turn_id, no pending) for the same turn → `waiting`;
     - (ii) the reverse order → `waiting`.
   - Fix in `interactive_ids.go` only if a test is red.
   - Argv test in `interactive_runtime_test.go`: the dispatch `--settings` has Stop, UserPromptSubmit, SessionEnd, PermissionRequest and Pre/PostToolUse(+Failure), and no `dispatch context`.
   - Verify: `go test -race ./internal/consult/`
8. **Mod: remove the passed-over subscriptions and add the guard-set test.**
   - New `tests/guard.test.ts` wraps `on`, records every event the mod registers, and asserts that set ∩ PASSED_OVER = ∅. PASSED_OVER is a constant citing the spec. It fails today.
   - Remove from `register.js`:
     - all `classic.*` handlers, `session.compact` and `prompt.compose`;
     - `withDelegationSection`, `noteIfToldOtherwise`, `addNote`, all told and `$.store` told code (`withTold`, `pruneTold`, the restamp in turn.complete);
     - `invalidate('prompt.section')`;
     - `subagentIds`, `trackSubagent` and the hello `subagents` field;
     - `stopPending`, and `stopHookPending` usage in `protocol.js`.
   - `runCompact` reporting follows the step-0 result (see Open questions).
   - Delete or rewrite the affected cases in `observe`, `delegation`, `store`, `bridge` and `protocol` `.test.ts`.
   - Go keeps accepting `ev.Pending` and hello `subagents`, because adopted launches keep old immutable mod directories.
   - Verify: `claude plugin test internal/harness/claude/bridgemod/leo-bridge` and `go test -race ./internal/harness/claude/bridgemod/`
9. **Doctor line.**
   - New `internal/cli/doctor_guard.go` with a pure `guardSeatLine(managed, dirs)`. Managed probe: `/Library/Application Support/ClaudeCode` or `/etc/claude-code` → `managed-settings.json`, `managed-settings.d/*.json`, `managed-mcp.json`. Each config dir's `.claude.json` is found via `claudeharness.StateFile(env)` for the default dir and every `environments:` entry.
   - Failing table test:
     - prependPlugins lists the guard → seated;
     - `[]` with a team account → unseated;
     - key unset with managed settings present → seated;
     - key unset, nothing managed, claude_team → seated;
     - claude_pro → unseated.
   - The level is always `info`, and the line says nothing breaks either way.
   - Hook into `reportBridge`. Verify: `go test -race -run Guard ./internal/cli/`
10. **Integration gate.**
    - Commands: `make test`, `make lint`, `claude plugin test …`, then `env -i HOME=$HOME PATH=$PATH TMUX_TMPDIR=$(mktemp -d) make e2e`. Run e2e from a plain shell, never from a dispatch pane against the live daemon.
    - Check the host load first. Then run the live gate.

## Live gate (spec)
1. Build the branch and run an **isolated test daemon** (own `LEO_HOME` and port). Never run `leo service restart`.
2. Back up the managed file byte for byte. Evan writes `{"prependPlugins":["cc-plugin-sec-default@builtin"]}` with `! sudo …`. The change is machine-wide: any production agent that relaunches in the window seats the guard too, so keep the window short.
3. From the test daemon, launch one agent and one interactive dispatch. Confirm the guard is seated from the `--debug` log.
4. Check:
   - `tmux capture-pane | grep -c "hook was skipped"` = 0;
   - a permission prompt → `/api/v1` needs_input with reason permission/tool, cleared after answering;
   - a native subagent → `subagents` goes 1 then 0;
   - a dispatch with `run_in_background` and ScheduleWakeup → status `waiting`;
   - the transcript JSONL has the delegation context at start, after `/clear` and after `/compact`;
   - after a delegation config edit, two prompts → exactly one note.
5. Restore the original file (`{"prependPlugins": []}`). Repeat unseated and confirm exactly one delegation injection per SessionStart, and no system-prompt section.

## Risks
- **Whether `additionalContext` reaches the model with the guard seated is unverified.** The spec only verified that command hooks fire. Step 0 settles it.
- **Possible size cap.** CC may cap hook `additionalContext` (I recall about 10k chars; not verified). Measure the length of `leomcp.DelegationSection`.
- **Compaction "failed" may be lost.** No command hook reports a failed auto-compaction, so a hook-only path can leave "started" with no end.
- **The session filter may drop hooks after /clear.** `isAgentHookSession` rejects a session ID that differs from `h.IDs`. If `h.IDs` is not updated on /clear, subagent, compact and attention hooks are dropped afterwards. The bridge was keyed by key, not session, so this is a regression unless step 2 tests a post-/clear session ID.
- **Attention now arrives on two channels.** The bridge still sends questions and activity; hooks send permission and elicitation. The ordering is not guaranteed (AskUserQuestion also fires PermissionRequest), so the reason can flap.
- **Every agent prompt gains a synchronous daemon round trip** for UserPromptSubmit. It must stay bounded and fail open.
- **Told state can be lost or duplicated.** A wrong or missing persistence path means a duplicate injection after a daemon restart. An agent rename re-emits once.
- **Orphaned store keys.** Old `told:` keys stay in the mod's `$.store` forever (cosmetic).

## Open questions
1. **Dispatch scope.** The code shows dispatches have no consumer for attention, subagent or compaction bridge events, and get no delegation. Is it confirmed that the dispatch path needs only the Stop work in step 7? And does "Dispatch path has none today" for Elicitation mean *add* dispatch needs_input on elicitation (a new feature)?
2. **Non-bridged agents.** The delegation hooks ride on `AttentionHooks`, which every Claude agent gets, including legacy fallback and pre-mod Claude. Should `/api/agent/context` emit only when the agent's current launch is bridged (parity), or always (new behavior)?
3. **Compaction source of truth.** If step 0 shows PreCompact/PostCompact fire for `$.session.compact()`, drop the mod's started/completed reports and keep only `failed` from `runCompact`? If not, keep the mod reporting its own compactions?
4. **The spec's `helloIfSessionChanged` line.** "The existing change detector (`helloIfSessionChanged`) reads it there" is unclear. Hook payloads carry session_id, so the plan leaves `helloIfSessionChanged` unchanged. Is that the intent?
5. **SessionStart `fork`.** The spec lists four sources; fork is excluded unless you say otherwise.
6. **The host's `prependPlugins: []` workaround.** Should it be removed after the merge, or left in place?

## Resolved open questions (orchestrator, 2026-10-26)
1. Dispatch scope: only the step-7 Stop work. No new dispatch needs_input on elicitation — the spec table note just recorded the gap; nothing consumes it for dispatches.
2. Non-bridged agents: parity — `/api/agent/context` emits only when the agent's current launch is bridged.
3. Compaction: decided by step 0. If PreCompact/PostCompact fire for `$.session.compact()`, hooks own started/completed and the mod reports only `failed`; otherwise the mod keeps reporting its own compactions (runCompact is an API call, not a passed-over subscription).
4. `helloIfSessionChanged`: leave unchanged. Spec line means the told store moves to the daemon; detection stays where it is.
5. SessionStart `fork`: include it (matcher `startup|resume|clear|compact|fork`) — a fork is a new session that prompt.compose used to cover.
6. Host `prependPlugins: []` workaround: out of scope; leave in place.
Additional gate: if step 0 shows `additionalContext` does not reach the model while seated, or is size-capped below `DelegationSection`, stop and report before step 3.
