# Claude harness: per-template MCP allowlist

Requested by Evan through the assistant agent, 2026-11-14.

## Problem
A lean Claude dispatch (`mcp: none`, the default since the 2026-09-16 lean-dispatch spec) launches with `--strict-mcp-config`, so it gets only the leo bridge. The alternative, `mcp: inherit`, brings back every user-scope server along with its boot-token cost. A template has no way to ask for just one extra server, such as the new user-scope `chrome` server (headed Playwright Chrome, used when WebFetch is bot-blocked).

## Change
1. **New option.** A new `harness_options` key on the Claude harness, `mcp_servers: [name, ...]`. It is valid at `defaults` and `templates.*` (and `tasks.*`, wherever claude `harness_options` already apply).
   - It cascades like the other options: the most specific scope's list replaces the inherited one; lists are not merged.
   - It is a separate key rather than a union type on `mcp`, so the existing `mcp` enum, its schema, the web form and the config API stay unchanged.
2. **Meaning.** When the resolved `mcp` is `none` (including the default) and `mcp_servers` is non-empty, the strict MCP config contains exactly two things: the leo bridge server (when present) and the named servers.
   - No other servers are added.
   - The plugin profile is unchanged.
   - With `mcp: inherit`, `mcp_servers` is ignored and a warning is logged, because inherit already includes every server.
3. **Resolving names.** Each name is looked up in the run's own account config: the top-level `mcpServers` of `$CLAUDE_CONFIG_DIR/.claude.json`, or `~/.claude.json` when that variable is unset. `CLAUDE_CONFIG_DIR` is taken from the environment the run is launched with, not from Leo's own process, because accounts differ by environment.
4. **Missing names.** A name the account config doesn't define is skipped, with a visible warning:
   - in the daemon log, with the dispatch id and template;
   - on the dispatch record or start output, so `leo_dispatch` and `leo dispatch watch` show it.

   A missing name is not an error.
5. **Secrets.** Server definitions can carry tokens in `url`, `headers` or `env`.
   - When `mcp_servers` adds any server, the strict config is written to a file under `state/` with mode 0600, and its path is passed as `--mcp-config <path>`. The definitions never go into argv or logs.
   - The file is removed when the run is released or closed. Stale files are swept on daemon start.
   - Leave the bridge-only inline config unchanged when no servers are added.
6. **Scope.**
   - Every place the Claude harness builds a strict MCP config from `mcp: none`: dispatches, and supervised agents or tasks if they share the path. The planner should confirm which call sites there are.
   - Codex and opencode are out of scope; their adapters reject the key through strict option decoding.
7. **Docs and schema.**
   - The `mcp` row in `docs/configuration/harnesses.md` and a new `mcp_servers` row.
   - The `OptionsSchema` entry for the web form, as a string-list field.
   - The config API schema testdata, if it pins the claude option keys.

## Acceptance
- A template with `mcp_servers: [chrome]` produces a dispatch whose MCP config file holds exactly the bridge and `chrome`. The file is 0600, and the argv contains the path only.
- An unknown name produces a warning and the run still launches.
- Existing `inherit` and `none` behaviour and argv are unchanged when `mcp_servers` is unset.
- A `CLAUDE_CONFIG_DIR` set in the run's environment is the config the names are resolved from.
