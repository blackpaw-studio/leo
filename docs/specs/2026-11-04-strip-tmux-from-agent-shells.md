# Strip `TMUX` from agent shell commands

Status: approved by Evan 2026-11-04 · open question resolved as (a)

## Problem
Supervised agents run in panes of the production tmux server (`tmux -L leo -D`). Everything an agent runs inherits `TMUX`/`TMUX_PANE`. A tmux command with no `-L` or `-S` (`tmux kill-server`) targets the socket in `$TMUX`, and `TMUX_TMPDIR` doesn't change that. On 2026-11-04 at 13:22, an agent's teardown ran `TMUX_TMPDIR=… tmux kill-server` and killed every agent and dispatch.

## Goal
An agent's shell commands (Bash/exec tool calls) don't see `TMUX` or `TMUX_PANE`. The harness process, its MCP server and its bridge keep them. This guards against accidents, not malicious agents: `tmux -L leo kill-server` still works if someone means it.

## Design
1. **Claude** (`internal/harness/claude/settings.go`): add a dedicated SessionStart command hook that appends `unset TMUX TMUX_PANE` to `$CLAUDE_ENV_FILE`. CC sources that file before every Bash command, and `unset` is allowed there. `settings env` can't remove variables, so it can't do this.
2. **Codex** (`internal/harness/codex/options.go`): `-c shell_environment_policy.exclude=["TMUX","TMUX_PANE"]`. These are exact names, so `TMUX_TMPDIR` survives.
3. **Opencode** (`internal/harness/opencode` plugin): delete the two vars in the `shell.env` hook. This isn't documented, so it ships only if the spike proves it works. Otherwise opencode is left out, and that gap is documented.
4. **Dispatch placement** (`internal/cli/dispatch.go:157`, `internal/tmux/origin.go`): `leo dispatch run` called from an agent shell loses `TMUX_PANE`. The daemon should resolve the caller pane from `LEO_PROCESS_NAME`/the agent name when `caller_pane_id` is missing, so the split-in-caller placement keeps working. This lands first or in the same change.
5. **Doc fix:** `internal/templates/skills/daemon-management.md:70-71` uses bare `tmux ls` / `tmux attach`. Change them to `tmux -L leo …`.

Accepted behavior change: `leo attach` run from an agent shell attaches directly instead of opening a display-popup.

## Spike first (throwaway tmux server, never prod)
Confirm, for each harness:
- **Coverage:** the vars are gone in foreground Bash, background Bash, subagent Bash and Monitor.
- **Survival:** they stay gone after a `cd` (CwdChanged clears the env-file lines it wrote itself; check that it leaves ours alone).
- **Scope:** the MCP server's env still has `TMUX`.

## Open question (resolved: (a))
Once `TMUX` is gone, a tmux command with no `-L`/`-S` falls back to `$TMUX_TMPDIR/tmux-501/default`, which is your own default server if you run one on Dionysus. Options:
- **(a) Accept it (recommended).** It's far less damaging than killing the fleet.
- **(b) Also export a per-agent scratch `TMUX_TMPDIR`.** That makes bare tmux harmless, but it would break any leo CLI path that finds `-L leo` through `TMUX_TMPDIR`, so it needs an audit.
- **(c) Add a CC permission deny for bare `tmux kill-server`.** This only works for claude, and pattern matching it is brittle.

## Tests
- Settings and argv tests assert the real hook JSON and the codex `-c` argv; mocked seams aren't enough here.
- A daemon test: a dispatch with no `caller_pane_id` but a known `LEO_PROCESS_NAME` resolves the caller's pane.
- Run the e2e suite with the isolated-env recipe before merging.

## Out of scope
A hard isolation boundary between agents and the tmux server, e.g. a separate tmux user or socket permissions.
