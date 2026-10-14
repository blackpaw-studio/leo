# Backlog — Leo

Ranked. Milestone: reliability (VISION.md "Done looks like"). Items sourced from memory notes say "verify first": reproduce on current main before fixing; if it no longer reproduces, mark done with the evidence.

## B-021 · Nested dispatch completion notifications never wake the parent…   [done]
Type: bug
Report: Nested dispatch completion notifications never wake the parent dispatch: an implement dispatch that calls leo_dispatch and ends its turn to await the child's notification (the documented default) sits idle forever after the child finishes. Seen 2026-10-13 in autopilot runners d-6daead1ca329 (planner d-968f0cca86f6 done, runner idle 35 min) and d-3628f831baf4 (implementer d-dcbef2370461 done, runner idle); only an explicit leo_send_dispatch nudge + leo_wait recovered them. Either deliver child notifications to dispatch sessions or have leo_dispatch tell dispatch callers to collect with leo_wait.
Accept: A failing test reproduces the report; it passes after the fix; nothing else regresses.
Source: owner (/issue, 2026-10-13)
Inbox: 20261014T014703194176Z-99571247#1
Done: 0ac8ed48 (merge eb2d949e) — caller harness resolved from the parent dispatch record
Verified: not visually verified — daemon-only change; isolated daemon showed child notification suppressed on base, pending on lane

## B-001 · Persistent-task sessions and config processes survive `leo update`   [done]
Accept: a daemon restart adopts surviving tmux sessions for config `processes:` and `runtime: persistent` task agents, as it already does for ephemeral agents; shown on the isolated daemon (start, kill daemon PID, restart, session pane PID unchanged).
Source: owner (vision, memory project_leo_update_bounces_all_agents; internal/service superviseloop)
Done: 021fb4ae (merge c6e8dc0a) — already worked; e2e regression guard added
Verified: not visually verified — CLI/tmux item; isolated-daemon kill -9 restart kept pane pids (text captures .autopilot/shots/B-001-1-before.txt, B-001-2-after.txt)

## B-002 · `leo_send_dispatch` rejects "composer busy" on an idle run   [done]
Accept: an idle interactive dispatch with an empty composer accepts a follow-up; a joined-away/moved pane is found by tty; failing test first.
Source: owner (vision, issue #196; memory project_moved_pane_composer_busy)
Done: f5088d2d, 2d560f79, 17e5fb75, bb18a32a, 9944eaad (merge e7269f2e) — codex placeholder truncation in narrow panes read as draft
Verified: not visually verified — terminal classifier fix; real codex-cli 0.160.1 at 12–40 cols classifies empty after, draft before; isolated-daemon sends delivered

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

## B-022 · Linux: `leo update` may bounce every agent (systemd KillMode)   [idea]
Note: the systemd unit sets no KillMode, so `systemctl --user restart` SIGTERMs the whole cgroup including the tmux server (internal/service/daemon_linux.go:202); unverified; a fix changes the unit on existing installs and needs Evan's sign-off — principle 1
Source: autopilot polish (B-001)

## B-023 · config-reference skill still documents `processes:` and `sessions:`   [idea]
Note: internal/templates/skills/config-reference.md lines 21-31, 49, 64-68 describe removed keys and are served via leo_skill — principle 4
Source: autopilot polish (B-001)

## B-024 · Rejected dispatch send fires a false "dispatch failed" notification   [ready (next run)]
Type: bug
Report: A rejected leo_send_dispatch fires a "dispatch … failed" notification for a turn that never started (issue #196) — a false outcome, principle 4
Accept: A failing test reproduces the report; it passes after the fix; nothing else regresses.
Source: autopilot bug (B-002)

## B-025 · Flaky: TestBridgeReportAcceptsLargeTurnText times out under a loaded full suite   [ready (next run)]
Type: bug
Report: internal/daemon TestBridgeReportAcceptsLargeTurnText timed out in full `make test` runs in B-021 and B-002 lanes; passes alone — flaky test blocks the milestone's "no known flaky test" goal
Accept: A failing test reproduces the report; it passes after the fix; nothing else regresses.
Source: autopilot bug (B-002)
