# Run
Status: finished
Started: 2026-10-14T01:05:35Z
Budget: 20 items, until 2026-10-14T13:05:35Z
Digested-through: 5
Filed: 3/3 bugs, 5/5 ideas
Self-filed: B-001 → B-022 idea — Linux: leo update may bounce every agent (systemd KillMode)
Self-filed: B-001 → B-023 idea — config-reference skill still documents processes:/sessions:
Self-filed: B-002 → B-024 bug — Rejected dispatch send fires a false "dispatch failed" notification
Self-filed: B-002 → B-025 bug — Flaky TestBridgeReportAcceptsLargeTurnText
Self-filed: B-003 → B-026 bug — Persistent-task injection waits ~75s on the composer probe
Self-filed: B-003 → B-027 idea — Cold first opencode leo run times out at 150s (bug over cap)
Self-filed: B-003 → B-028 idea — Failed persistent task posts no Leo Chat card (bug over cap)
Self-filed: B-003 → B-029 idea — codex "Hooks need review" dialogs on symlinked home
Self-filed: B-003 → dropped (over cap) — concurrent leo run history-file rename race

## Progress
- Preflight step 7: run guard refused `git -C <worktree> merge main` [default-branch] (false positive: guard cannot expand a $VAR worktree path); main already an ancestor of autopilot, so sync was a no-op.

Lane: B-001
  Branch: autopilot-lane/B-001
  Base: d85f0b8572a2a003c515624370c715e0c088c1eb
  Tier: full
  State: landed
  Fixes: 0
  Wip: none
  Dispatched: 2026-10-14T01:06:06Z
  Reverifies: 0
  Reviewed-tip: e127c530cf7d3b9733d04659f8c56a43adfd1e27
  Call: No production change: processes folded into agents (#112), persistent-task targets are ordinary agents (#115), RestoreAgents already adopts — BACKLOG "verify first", principle 2
  Call: Treat the "config processes" criterion as already met (key no longer exists) — scope cut
  Call: Add a CHANGELOG [Unreleased] entry despite the plan — D-001
  Call: Restart check uses kill -9 (what kickstart -k does), not SIGTERM — principle 2
  Call: e2e uses the fake codex harness, not the claude bridge, so a bridge relaunch can't change pane_pid — principle 2
  Tokens: 13.7M in (13.2M cache read), ≥72.8k out, 130 requests

Lane: B-002
  Branch: autopilot-lane/B-002
  Base: d85f0b8572a2a003c515624370c715e0c088c1eb
  Tier: full
  State: landed
  Fixes: 1
  Wip: none
  Dispatched: 2026-10-14T01:06:06Z
  Reverifies: 0
  Reviewed-tip: f5088d2dc09521f6b6166c14a54123311ab9042d
- 2026-10-14T01:46:46Z: stall — runners ended turns awaiting nested-dispatch notifications that never arrive; nudged both via leo_send_dispatch to collect with leo_wait in-turn.
- Inbox applied: B-021 (nested-dispatch notification bug)
  Call: No tty-based pane lookup; %N is stable across moves and a tty fallback could paste into an unrelated pane; rejections name the tty instead — principles 1, 4
  Call: Prose never means busy when no composer row is visible; classifier says unknown — principle 4
  Call: Dropped the planned post-move redraw settle (never reproduced live) — principles 2, 4
  Call: Classifier handles codex placeholder truncation (not wrapping) at narrow widths — principle 2
  Call: Promoted go-runewidth (already indirect, MIT, go.sum unchanged) to a direct dependency; ambiguous width = 1 cell — principle 4
  Call: Accepted risk: a human draft that is exactly a placeholder prefix reaching the pane edge reads as empty — principle 4
  Tokens: 24.5M in (23.9M cache read), ≥123k out, 197 requests

Lane: B-021
  Branch: autopilot-lane/B-021
  Base: 3e34c7645fdba14303882f42f34e82bee164f89a
  Tier: light
  State: landed
  Fixes: 0
  Wip: none
  Dispatched: 2026-10-14T01:54:54Z
  Reverifies: 0
  Reviewed-tip: faa26c203ad4e728d01e94c25493b2be1c899c7d
  Tokens: 6.07M in (5.84M cache read), ≥29k out, 69 requests

Lane: B-003
  Branch: autopilot-lane/B-003
  Base: e36ee832f8eb22bf1385826b7063db0759e879b5
  Tier: full
  State: landed
  Fixes: 1
  Wip: none
  Dispatched: 2026-10-14T02:16:30Z
  Reverifies: 0
  Reviewed-tip: f979868c2e2b2db269afe09873b3329293f4163c

Lane: B-004
  Branch: autopilot-lane/B-004
  Base: 3de7bc265804ea9fed74353ad619c94d2d9b67d8
  Tier: light→full
  State: landed
  Fixes: 1
  Wip: none
  Dispatched: 2026-10-14T02:45:40Z
  Reverifies: 0
  Reviewed-tip: 950b52c0a43c915786c15ebe65e874c453fa26d9
  Call: On start failure the run settles failed and the send returns an error; the dispatch stays resumable — principle 4
  Call: The send returns once the process has started and is recorded, not when it finishes — principle 4
  Call: Queued continuations unchanged: report queued, flip to delivered on launch — principle 4
  Call: Accepted risk: cancel between cmd.Start and launch record leaves send saying delivered while the turn records undelivered/interrupted (sub-ms window) — severity bar
  Tokens: 10.1M in (9.62M cache read), ≥59.7k out, 118 requests
  Call: Turn hooks are the completion signal for codex and opencode, with no per-harness special case in the core path — principle 3
  Call: Interrupt on the matched turn fails the run as interrupted; an errored opencode turn reports StopFailure → 'turn failed: <err>' — principle 4
  Call: Opencode leo plugin POSTs to /api/agent/hook, loaded from Env() for supervised non-dispatched agents; plugin file written to ~/Library/Caches/leo/opencode/ (plan deviation) — principle 3
  Call: Child-session guard: a subagent session's idle no longer completes the parent's run — principle 4
  Call: Accepted risk: a human typing the identical task prompt during the injection wait binds their own turn (documented in persistent-tasks.md) — severity bar
  Tokens: 46.6M in (45.8M cache read), ≥176k out, 319 requests

Lane: B-005
  Branch: autopilot-lane/B-005
  Base: aa83adca1155d591483c73b75d4f3ef4b10755e5
  Tier: full
  State: shelved
  Fixes: 0
  Wip: none
  Dispatched: 2026-10-14T03:12:10Z
  Reverifies: 0
  Reviewed-tip: none
  Call: Two-layer fix: in-process per-name lifecycle lock plus field-diff agentstore.Patch, no on-disk format change — principle 1, stop-list format constraint
  Call: Store mutators refuse to overwrite an unreadable agents.json; writes are atomic — principle 4
  Call: clearFailedRestore no longer persists boot-time launch args to the record — principle 1
  Call: Delete returns an error when removing the record fails; spawn rollback and restore log it — principle 4
  Call: Restore skips the mail drop for a record it can't remove — principle 4
  Tokens: 18.9M in (18.4M cache read), ≥100k out, 144 requests

Lane: B-006
  Branch: autopilot-lane/B-006
  Base: 8835d669cedd3308eed39190e2957acceed86f41
  Tier: light
  State: shelved
  Fixes: 0
  Wip: none
  Dispatched: 2026-10-14T03:53:18Z
  Reverifies: 0
  Reviewed-tip: none
  Tokens: 2.79M in (2.61M cache read), ≥15.6k out, 36 requests

Finished 1: B-001 landed c6e8dc0afd640ffc408aaae1ec5b75c3b96b716e · shot not visually verified
- B-001 notes: verify.md restart line uses pkill (SIGTERM) → false bounce; should be kill -9. Fixed port 8391 can collide across parallel lanes. First review/verify dispatches came back lost after ~30s; retries succeeded. Memory note project_leo_update_bounces_all_agents is stale.
Finished 2: B-021 landed eb2d949e5280592e57bf785ff8b0ed8a58f84b96 · shot not visually verified
- B-021 note: full suite flaked once on TestBridgeReportAcceptsLargeTurnText (internal/daemon timeout; passed 3/3 alone) — not in the diff.
- B-021 built before B-003 (owner-filed bug, top of queue); B-003 independent of B-002.
Finished 3: B-002 landed e7269f2e1504a381124e6a5c1e578b08c1625d93 · shot not visually verified
- B-002 notes: rejection-with-tty path unit-tested only (not live); 16-col pane still misreads placeholder (accepted risk); implement.hard first dispatch lost after 27s, retry succeeded.
Finished 4: B-004 landed 32b30208e3ffc2b65e562b61359dda24c8dc8d4c · shot not visually verified
- B-004 notes: TestCancelingABackgroundedViewerKillsItsPane failed once under load (did not recur); make lint shows stale errcheck from sibling lanes unless GOLANGCI_LINT_CACHE is fresh.
Finished 5: B-003 landed 59b11c21c7b560486d25ec71ed17c84dadb7cfea · shot not visually verified
- B-003 notes: NEVER-LIST BREACH (accidental): an implementer live check used a nonexistent LM Studio model id, opencode fell back to a paid OpenAI model and spent one tiny turn; verifier told not to repeat. Opencode error path (StopFailure) unit-tested only. Plugin file written to ~/Library/Caches/leo/opencode/.
Finished 6: B-005 blocked
- EARLY STOP: B-005 runner reported Error: review.concurrency (codex-reviewer) failed twice with "interactive composer not ready" (d-bff4ca8845f1, d-cf18cc4018c5; turn rejected, undelivered, ~60s each). Treated as a failed dispatch per the Loop: no new lanes; B-006 drains.
- Early stop (failed dispatch) at 2026-10-14T03:54:54Z
Finished 7: B-006 done (no code change)
- B-006: already fixed by 85e9c5e3 (PR #223, v0.35.0); no CHANGELOG entry (would misdate a shipped fix). Issue #175 still open — closing is Evan's call.
