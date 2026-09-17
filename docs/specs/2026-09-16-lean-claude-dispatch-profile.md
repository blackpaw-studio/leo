# Native-parity profile for claude-harness dispatches

Date: 2026-09-16 · Status: proposed

## Problem

A `leo_dispatch` on the claude harness boots a full Claude Code process against the user's
whole `~/.claude`: every global MCP server, every installed plugin (hooks, commands,
claude-mem observation generation), skills, and agents. Measured cost of a one-word
headless dispatch on `fable`: 57,131 input tokens (48,178 cold 1h-cache write, 8,951 cache
read), $0.97, before any work happens.

Goal: a dispatched claude subagent should see what a native Claude Code subagent sees, no
more. Probed empirically on 2026-09-16 (Haiku subagent asked to inspect its own context) and
cross-checked against code.claude.com/docs/en/sub-agents and /memory:

| Included by native subagents | Not included |
|---|---|
| user `~/.claude/CLAUDE.md`, project `CLAUDE.md` | plugin hooks (no claude-mem SessionStart block) |
| `~/.claude/rules/*.md` | plugin commands / skills listing |
| auto-memory `MEMORY.md` index (docs say no; observed yes, it ships in the CLAUDE.md bundle) | Agent-type listing |
| | MCP tools (only servers named in the agent's own `mcpServers`) |

Haiku boot measurements (input + cache write + cache read): baseline 43.3k, 277 tools,
3 plugin SessionStart hooks · `--strict-mcp-config` 38.2k, 31 tools · plus all plugins
disabled via `--settings enabledPlugins` 37.1k, 0 hooks. `--setting-sources ""` would
save 12k more but also drops CLAUDE.md/rules/memory, which native subagents keep — rejected.
The remaining cold-write-vs-warm-read gap is structural and out of Leo's hands.

## Design

Two new claude `harness_options` keys, decoded and validated by the adapter like the rest:

- `mcp: inherit | none` — `none` passes `--strict-mcp-config --mcp-config <json>` where the
  JSON holds only the servers leo itself adds for that run (leo bridge when present, else
  `{"mcpServers":{}}`). `inherit` (default) adds nothing.
- `plugins: inherit | none` — `none` merges `enabledPlugins: {<id>: false}` for every id in
  `~/.claude/plugins/installed_plugins.json` into the run's `--settings` JSON (interactive
  runs already carry a `--settings` object; extend the existing merge). `inherit` default.
  Not `disableAllHooks`: that would also kill leo's own Stop/UserPromptSubmit/SessionEnd hooks.

Cascade: `defaults` → `templates.<name>` like every harness option. No new CLI flags.

Dispatch default: when `kind == dispatch` and the template sets neither key, leo applies
`mcp: none` and `plugins: none`. Ephemeral agents and tasks are unchanged. A template opts
back in with `mcp: inherit` / `plugins: inherit`.

## Out of scope

Session pooling / `--resume` reuse across dispatches. Model choice (config): the claude
dispatch templates run `fable` and `defaults.model` is `opus[1m]`.

## Tests

- Adapter: option decode, rejection of unknown values, exact argv per combination
  (`--settings` JSON compared structurally), plugin ids read from an injected path.
- Dispatch: `kind == dispatch` default applied; template override wins; agents/tasks untouched.
- e2e (`make e2e`): real `claude -p` under the profile shows ≤ ~35 tools and no
  `hook_response` events in the stream; an interactive run still fires leo's `Stop` hook.
