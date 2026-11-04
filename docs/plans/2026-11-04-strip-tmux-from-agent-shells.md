# Plan: strip TMUX from agent shell commands

Spec: docs/specs/2026-11-04-strip-tmux-from-agent-shells.md (source of truth; open question resolved (a)).
Branch: feat/strip-tmux-agent-shells. One commit per step.
HARD RULE (every step): never touch the production tmux server (`-L leo`, /private/tmp/tmux-501) or the leo daemon.
No `leo service restart`, no agent restarts, no bare `tmux`. Tests: `env -u TMUX go test -race <pkgs>`.
e2e only as `heavy env -i HOME="$HOME" PATH="$PATH" TMUX_TMPDIR="$(mktemp -d)" make e2e` (run in background).

## Verified ground truth (corrects the research notes)
- The daemon already resolves a missing pane from `from` (= LEO_PROCESS_NAME) via `@leo_primary_pane`
  (internal/web/caller.go:25-31). TestResolveDispatchCallerUsesPrimaryPaneAndExactTmuxArgv covers that,
  which satisfies the spec's daemon test. The gap is nested dispatches: the CLI sends only `parent_dispatch_id`
  when LEO_DISPATCH_ID is set (internal/cli/dispatch.go:80-88), so the daemon ends up with no pane.
- The leo_dispatch MCP tool reads TMUX in the MCP server process (internal/mcp/tools.go:536). Claude's MCP server keeps it.
  Codex hands MCP servers only its defaults plus env_vars (internal/leomcp/env.go:72 lists no TMUX), so codex
  MCP probably lacks TMUX today. That's existing behavior, and Step 1 covers its nested case.
- Claude's TurnHooks (internal/harness/claude/hooks.go:20) is the one builder used by both supervised agents
  (AttentionHooks, settings.go:66) and interactive dispatches (internal/consult/interactive_runtime.go:183).
  Same-event hook groups append through mergeSettings/appendGroups (settings.go:281-310), so a new SessionStart
  group sits alongside the context group (settings.go:97-101).
- Codex argv is built in internal/harness/codex/codex.go: exec branch :91-108, TUI branch :111-126.
  options.go:54-67 holds only the MCP bridge `-c` args.
- Opencode: leo-turns.js is added only for non-dispatched agents (attention.go:80, used at options.go:120) and
  does nothing under LEO_DISPATCH_ID. The TMUX strip therefore needs its own plugin.
- Skill doc: daemon-management.md:70-71 is confirmed. `leo-<pid>` is also stale (sessions are `leo-<name>`): follow-up only.

## Step 1: Daemon resolves a nested dispatch's caller pane from its parent (implement, medium)
Files: internal/web/caller.go `resolveDispatchCaller` (signature unchanged). No CLI change.
Logic: if pane=="" && session=="" && parentDispatchID!="" → `s.consults.Get(parent)`. If parent.PaneID != "",
use it and mark it derived. If display-message fails for a derived pane, return `dispatchCaller{}, nil`
(background placement, not a 400). The harness then comes from parentDispatchHarness (the PaneID matches).
Red tests (internal/web/handlers_consult_test.go, existing exec-seam pattern, asserting exact argv):
- `TestResolveDispatchCallerDerivesPaneFromParentDispatch`: interactive parent with PaneID %9 and harness codex.
  Call with ("", "", parentID). Argv must equal
  `[-L leo display-message -p -t %9 #{session_name} #{session_id} #{window_id}]`; result {%9, $sid, @wid, codex}.
- `TestResolveDispatchCallerParentPaneGoneDegrades`: display-message errors → empty caller, nil error.
- `TestResolveDispatchCallerHeadlessParentNoPane`: parent PaneID "" → empty caller, no tmux call.
- One API test (TestAPIConsult* pattern): POST /api/dispatch with only parent_dispatch_id → stored record's
  CallerPaneID == %9.
Verify: `env -u TMUX go test -race ./internal/web/ ./internal/cli/ ./internal/consult/`
Commit: `fix(dispatch): resolve a nested dispatch's caller pane from its parent`

## Step 2: Doc fix (orchestrator direct; docs exception to TDD)
internal/templates/skills/daemon-management.md:70-71 → `tmux -L leo ls`, `tmux -L leo attach -t leo-<pid>`.
List other bare `tmux ls|attach|kill` in internal/templates and docs/ as follow-ups; don't fix them here.
Verify: `env -u TMUX go test -race ./internal/templates/...`. Commit: `docs(skills): target leo's tmux socket explicitly`

## Step 3: Spike (gates Steps 4-6; can run alongside 1-2; read/exec only, no repo changes)
Setup (all tmux goes through the wrapper; the env -i strips LEO_* so nothing reaches the daemon):
```sh
SPIKE=$(mktemp -d); mkdir "$SPIKE/work"
cat >"$SPIKE/t" <<EOF
#!/bin/sh
exec env -i HOME="$HOME" USER="$USER" LOGNAME="$USER" SHELL=/bin/zsh PATH="$PATH" LANG=en_US.UTF-8 \
  TERM=xterm-256color TMUX_TMPDIR="$SPIKE" ${CLAUDE_CONFIG_DIR:+CLAUDE_CONFIG_DIR="$CLAUDE_CONFIG_DIR"} \
  tmux -S "$SPIKE/s" -f /dev/null "\$@"
EOF
chmod +x "$SPIKE/t"; "$SPIKE/t" new-session -d -s spike -c "$SPIKE/work" -x 220 -y 60
"$SPIKE/t" show-environment -g | grep -c '^LEO_'   # must be 0
```
Probe (label L): `printf '%s TMUX=%s PANE=%s TMPDIR=%s\n' L "${TMUX-unset}" "${TMUX_PANE-unset}" "${TMUX_TMPDIR-unset}" >> "$SPIKE/work/probe.txt"`
Env-dump MCP server: `/bin/sh -c 'env > $SPIKE/work/mcp-env-<harness>.txt; exec sleep 600'`. It fails init; only the dump matters.
Baseline: run the probe in a plain spike pane. It must show TMUX set, which proves the rig works.
Drive each TUI with `"$SPIKE/t" send-keys -t spike …` and read it with `"$SPIKE/t" capture-pane -p -t spike`.
- Claude: `$SPIKE/claude.json` = exactly Step 4's SessionStart group, plus a PostToolUse hook running probe `hook`.
  Launch `claude --settings $SPIKE/claude.json --strict-mcp-config --mcp-config $SPIKE/mcp.json --allowedTools Bash,Monitor,Task`.
  Prompt it to probe: `fg`, a run_in_background Bash (`bg`), `cd /tmp` then the next call (`after-cd`),
  a Task subagent (`sub`), a Monitor whose command is the probe (`mon`). Then `/clear` and probe `after-clear`.
  Also check `ps eww` on the claude child of `#{pane_pid}`: it should have TMUX=.
- Codex: first `grep -n shell_environment_policy ~/.codex/config.toml` (a confounder if present). Launch with
  `-C $SPIKE/work --sandbox workspace-write -a never -c 'shell_environment_policy.exclude=["TMUX","TMUX_PANE"]'
  -c features.multi_agent=true` plus `mcp_servers.envdump.*` overrides. Probes: fg, a backgrounded/unified-exec
  session (after a `cd`), subagent. Monitor is n/a. If the brew cask wedges, use ~/leo-plan3-fixtures/bin/codex.
- Opencode: `strings "$(command -v opencode)" | grep -n 'shell\.env'` to get the hook's name and signature (undocumented).
  Write `$SPIKE/strip.js` deleting output.env.TMUX/TMUX_PANE. Launch with
  `OPENCODE_CONFIG_CONTENT='{"plugin":["file://$SPIKE/strip.js"],"mcp":{"envdump":{"type":"local","command":[...]}}}'`
  and the model from `templates.local` in ~/.leo/leo.yaml (read-only). Probes: fg, background (if offered), task subagent, after-cd.
Pass (each harness): every shell probe shows TMUX=unset PANE=unset TMPDIR=$SPIKE. `hook`, the harness process and
the MCP dump (claude, opencode) still have TMUX. Codex MCP lacking TMUX is expected and changes nothing.
Results that change the design (stop and report to the orchestrator; don't improvise):
- Claude `bg`/`sub`/`mon` leaks → CLAUDE_ENV_FILE misses that path. Candidate fix: PreToolUse(Bash) updatedInput prefix.
- Claude `after-cd` leaks → also register Step 4's group under CwdChanged (fold into Step 4).
- Claude `hook`/MCP/process lose TMUX → the env file reaches the harness side. Stop.
- Codex `TMUX_TMPDIR` dropped → the exclude isn't exact-name. Background path leaks → document that gap.
- Opencode hook missing, or deletion doesn't strip → Step 6 becomes doc-only. Setting vars to "" doesn't count
  unless `TMUX= tmux ls` inside a spike pane fails to reach the spike server.
Output: a results table of ≤20 lines for the PR body. Teardown: `"$SPIKE/t" kill-server; rm -rf "$SPIKE"`.

## Step 4: Claude (implement, medium)
Files: internal/harness/claude/hooks.go. Add const `shellEnvStripCommand` =
`[ -z "$CLAUDE_ENV_FILE" ] || grep -qxF 'unset TMUX TMUX_PANE' "$CLAUDE_ENV_FILE" 2>/dev/null || printf '%s\n' 'unset TMUX TMUX_PANE' >> "$CLAUDE_ENV_FILE"`
TurnHooks also emits `SessionStart: [{hooks:[{type:command, command:<const>, timeout:5}]}]` (no matcher, so every
source). That covers agents and interactive dispatches with no new call sites. Add CwdChanged only if the spike says so.
Red tests: decode the `--settings` JSON from `Claude{}.TurnHooks(cmd)` and assert the exact group. `AttentionHooks(…report)`
carries both the context group (matcher startup|resume|…) and the strip group. Behavior check: run the command via
`sh -c` twice with CLAUDE_ENV_FILE=tmp → the file holds exactly one `unset TMUX TMUX_PANE` line. With the var unset →
exit 0 and no file. internal/consult/interactive_runtime_test.go: the merged dispatch argv contains the strip group.
Verify: `env -u TMUX go test -race ./internal/harness/claude/ ./internal/consult/ ./internal/service/`
Commit: `feat(claude): unset TMUX in agent shell commands`

## Step 5: Codex (implement, low)
Files: internal/harness/codex/codex.go. New `shellEnvPolicyArgs()` → `-c`, `shell_environment_policy.exclude=["TMUX","TMUX_PANE"]`
(via tomlStringArray), appended in both the TUI and exec branches. Confirm leomcp.MigrateLaunch (internal/leomcp/migrate.go)
only rewrites the MCP command arg, so it needs no change.
Red test (codex_test.go, table-driven): Args for KindAgent, KindAgent+Dispatched and KindTask each contain that adjacent
pair exactly once.
Verify: `env -u TMUX go test -race ./internal/harness/codex/ ./internal/agent/ ./internal/leomcp/ ./internal/consult/`
Commit: `feat(codex): exclude TMUX from shell environments`

## Step 6: Opencode (implement, medium; only if the spike passes, else doc-only)
Files: new internal/harness/opencode/plugin/leo-shellenv.js (strip only, no guards). attention.go: generalize
ensureTurnPlugin to `ensurePlugin(prefix, source)`, content-addressed. options.go:120: add the shellenv entry for every
KindAgent launch, dispatched ones included. leo-turns.js is untouched.
Red tests: OPENCODE_CONFIG_CONTENT `plugin` contains `file://…/leo-shellenv-<hash>.js` for agent and dispatched specs,
and leo-turns only for non-dispatched. A node-run harness (the attention_test.go:90-150 pattern; skips without node) gets an
env with TMUX, TMUX_PANE and TMUX_TMPDIR → only TMUX_TMPDIR remains.
Spike fails: add "Known gap: opencode shell commands keep TMUX" to docs/configuration/harnesses.md.
Verify: `env -u TMUX go test -race ./internal/harness/opencode/`

## Step 7: Integration gate (orchestrator)
- Check load average; `env -u TMUX make lint`; `heavy env -u TMUX go test -race -cover ./...` (background); the e2e recipe above.
- Review dispatch on `git diff main...HEAD`. Bar: P0/P1 plus anything a realistic agent/dispatch launch can hit.
- Live check is Evan's call: the hooks/argv apply only on a fresh launch, and adopted agents keep TMUX until relaunched.
  Propose relaunching one named claude agent, then a Bash probe there. Never restart the daemon.

## Risks
- Partial coverage (some Bash path ignores the env file or policy): no regression, documented from the spike.
- Codex `-c` replaces any `shell_environment_policy.exclude` from the user's config.toml for leo launches. Accepted; flag in the PR.
- `leo update` stale report will list relaunch-pending agents as argv-changed. Report only, nothing bounces.
- Accepted behavior changes: `leo attach` from an agent shell attaches directly. `leo dispatch run` from an agent's
  non-primary pane now splits beside the primary pane. Bare tmux hits the default server (resolution (a)).
- Step 1 trusts a caller-asserted parent_dispatch_id, the same trust as caller_pane_id today (behind the bearer token).
## Rollback
Each step is its own commit, so `git revert <sha>` per harness. The runtime effect ends at the next fresh launch.
Nothing persists except the line in CC's per-session env file and a plugin file in the cache that does nothing on its own.
