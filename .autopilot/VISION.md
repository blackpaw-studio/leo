# Vision — Leo

## North star
Leo keeps a fleet of coding agents running, reachable, and delegating: a daemon you forget is there until it tells you something worth knowing.

## Taste principles
1. **Never disturb live agents.** No change may bounce, kill, re-key, or type into a running session as a side effect. Restarts and adoption must be lossless; anything that would interrupt agents is opt-in and says so.
2. **Prove it live, not through mocks.** Exec and tmux seams assert exact argv; behavior changes are shown on an isolated daemon (own home, own tmux server), not inferred from a green suite.
3. **Agnostic at the edges.** Works with stock channel plugins and all three harnesses (claude, codex, opencode). Never require forking a plugin or special-casing one harness in core paths.
4. **Failures are loud; success is quiet.** Never swallow an error, and never report success that didn't happen (false "delivered", false "failed"). Notifications fire only when a human needs to act or know.
5. **The API is the contract.** Clients (leoterm, iOS, the Den) consume `/api/v1` and the socket; features land there first, server-side, with no CORS.

## Non-goals
- The Den as part of Leo or a Leo plugin; it stays a standalone repo.
- Subagents as plain TUI sessions without a completion contract.
- tmux hooks for state that production and test daemons would share; poll instead.
- Leo owning SSH tunnels for leoterm (leoterm owns its own).
- Name-keyed bridge state.
- Leo MCP/nudge in persistent claude sessions (#110): left out for the channel-driven interaction model and spawn blast radius.
- Files on chat asks (two messages instead).
- A dispatches page in the web UI; leoterm and iOS cover dispatch viewing.

## Done looks like (current milestone: reliability)
- Every open bug issue is fixed or closed with a reason; no known flaky test in `make test` on either CI platform.
- `leo update` / daemon restart leaves every agent, config process, and persistent-task session running (no bounces).
- No dispatch path reports a false outcome (delivered/failed/busy) — each has a test that failed before the fix.
- Each fix is shown working on an isolated daemon, with the evidence in the run log.
