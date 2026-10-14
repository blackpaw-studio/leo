# Backlog — Leo

Ranked. Milestone: reliability (VISION.md "Done looks like"). Items sourced from memory notes say "verify first": reproduce on current main before fixing; if it no longer reproduces, mark done with the evidence.

## B-021 · Nested dispatch completion notifications never wake the parent…   [ready]
Type: bug
Report: Nested dispatch completion notifications never wake the parent dispatch: an implement dispatch that calls leo_dispatch and ends its turn to await the child's notification (the documented default) sits idle forever after the child finishes. Seen 2026-10-13 in autopilot runners d-6daead1ca329 (planner d-968f0cca86f6 done, runner idle 35 min) and d-3628f831baf4 (implementer d-dcbef2370461 done, runner idle); only an explicit leo_send_dispatch nudge + leo_wait recovered them. Either deliver child notifications to dispatch sessions or have leo_dispatch tell dispatch callers to collect with leo_wait.
Accept: A failing test reproduces the report; it passes after the fix; nothing else regresses.
Source: owner (/issue, 2026-10-13)
Inbox: 20261014T014703194176Z-99571247#1

## B-001 · Persistent-task sessions and config processes survive `leo update`   [ready]
Accept: a daemon restart adopts surviving tmux sessions for config `processes:` and `runtime: persistent` task agents, as it already does for ephemeral agents; shown on the isolated daemon (start, kill daemon PID, restart, session pane PID unchanged).
Source: owner (vision, memory project_leo_update_bounces_all_agents; internal/service superviseloop)

## B-002 · `leo_send_dispatch` rejects "composer busy" on an idle run   [ready]
Accept: an idle interactive dispatch with an empty composer accepts a follow-up; a joined-away/moved pane is found by tty; failing test first.
Source: owner (vision, issue #196; memory project_moved_pane_composer_busy)

## B-003 · Persistent codex/opencode runs recorded as failures   [ready]
Accept: a successful persistent-task turn on codex and opencode records success and fires no `notify_on_fail` card; verify first.
Source: owner (vision, memory project_uniform_tui_drivers)

## B-004 · Headless continuations report Delivered before start   [ready]
Accept: `delivered` is reported only after `cmd.Start()` succeeds; a start failure surfaces as an error.
Source: owner (vision, issue #236)

## B-005 · agentstore Stop/Start read-modify-write race   [ready]
Accept: concurrent Stop/Start on one record can't lose an update; `-race` test reproduces it first.
Source: owner (vision, issue #160)

## B-006 · `leo update` stale-agent check races the daemon restart   [ready]
Accept: the post-update check waits for the new daemon before judging agents stale; no false warning.
Source: owner (vision, issue #175)

## B-007 · Dispatch viewer close retried every 10 s forever   [ready]
Accept: a stale record whose viewer is already gone is closed once and not retried; log noise stops.
Source: owner (vision, issue #202)

## B-008 · `ResumeIDFor` can pick another agent's transcript   [ready]
Accept: resume selection is scoped to the agent's own session/workspace; verify first, test reproduces it.
Source: owner (vision, memory project_claude_mods_bridge_spike)

## B-009 · `tmux.InjectIntoWith` can paste without Enter and skips the paste lock   [ready]
Accept: every injection path takes the paste lock and confirms submit; argv-asserting test; verify first.
Source: owner (vision, memory project_claude_mods_bridge_spike)

## B-010 · Flaky tests: #247, #208, #201   [ready]
Accept: each root-caused (no retries or timeout bumps as the fix); the test passes 20× under `-race` locally.
Source: owner (vision, issues #247 #208 #201)

## B-011 · Spawn-route gate test matches the whole file   [ready]
Accept: `TestEverySpawnRouteIsGated` fails when one route's gate is wrong (mutation check), passes on main.
Source: owner (vision, issue #162)

## B-012 · Web saves persist `~`-expanded paths   [ready]
Accept: editing a template in the web UI keeps `~/…` in workspace/mcp_config/add_dirs as written; load still expands.
Source: owner (vision, issue #260)

## B-013 · Environment rename: surface agent-record update failures   [ready]
Accept: a rename that fails to update an agent record returns/shows the error instead of only logging it.
Source: owner (vision, issue #257)

## B-014 · e2e caller-restart test un-skipped   [ready]
Accept: the fake ephemeral agent stays up under supervision; `TestInteractiveCallerRestart` runs and passes in `make e2e` (env -i form).
Source: owner (vision, issue #176)

## B-015 · CHANGELOG backfill   [ready]
Accept: `[Unreleased]` and released sections cover #247–#271 from git log and tags; stale `[Unreleased]` content moved under the version that shipped it.
Source: owner (vision, decision D-001)

## B-016 · Environment rename via /api/v1, socket, and MCP   [ready]
Accept: rename is reachable from all three surfaces with the same validation as the web UI.
Source: owner (vision, issue #256)

## B-017 · Roster: idle-since next to active time   [ready]
Accept: an idle dispatch's roster line reads e.g. `3m12s · idle 4m`; active time unchanged.
Source: owner (vision, decision D-003; memory project_subagent_dispatch)

## B-018 · Picker shows dormant time for dormant rows   [ready]
Accept: dormant rows show when the agent went dormant, not spawn time.
Source: owner (vision, issue #161)

## B-019 · Per-template dispatch concurrency cap   [ready]
Accept: `templates.<name>` can cap live dispatches; extra dispatches queue as with the global cap.
Source: owner (vision, issue #238)

## B-020 · Push bodies: full permission command, question length   [ready]
Accept: permission pushes show the command (truncated at a byte budget, not the first word); the question cap is measured against APNs limits and tested.
Source: owner (vision, memory project_ios_daemon_surface)
