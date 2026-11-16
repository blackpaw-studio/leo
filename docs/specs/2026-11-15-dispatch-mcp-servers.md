# Per-dispatch MCP servers

Requested by Evan, 2026-11-15. Builds on [the per-template MCP allowlist](2026-11-14-claude-mcp-allowlist.md) (#338).

## Problem
Only the template config chooses which MCP servers a dispatch gets (`harness_options.mcp_servers`, #338). A calling agent sometimes knows a one-off run needs a server that its role's template doesn't list, such as `chrome` for a bot-blocked page. Today it has to ask Evan for a new template or role mapping.

## Change
1. **New dispatch input `additional_mcp_servers: [name, ...]`.** Optional. It is accepted by:
   - the `leo_dispatch` MCP tool;
   - the daemon dispatch API that the tool calls;
   - `leo dispatch` on the CLI, as the repeatable flag `--additional-mcp-server <name>`.

   `leo_consult` and `leo_send_dispatch` don't take it.
2. **Added to the template's list.** The run's effective server set is the leo bridge, plus the template's resolved `mcp_servers`, plus the caller's list, with duplicates removed. A caller can only add servers. It can't remove the template's.
3. **Policy.** The caller may name any server defined in the run account's config. Names are resolved exactly as in #338: top-level `mcpServers` of `$CLAUDE_CONFIG_DIR/.claude.json` from the run's environment, or `~/.claude.json` when that variable is unset.
   - There is no operator allowlist, and the read-only roles (`explore`, `plan`, `review*`) get no exception.
   - This is a deliberate choice. A read-only role's harness permissions still block file writes, but an added MCP server can act on external systems. The `docs/configuration/` page states this in plain terms.
4. **Missing names** are skipped with the same warning path as #338: the daemon log, plus the `warnings` on the dispatch result and on `leo dispatch watch`. A missing name is never an error.
5. **Input validation** happens at the API boundary. Over these limits the request fails with a 400 and a clear message:
   - at most 16 names;
   - each name is 1–64 characters of `[A-Za-z0-9_.-]`;
   - duplicates are removed silently.
6. **Harness interaction.**
   - Claude with `mcp: none` (the default): extras go into the same 0600 strict-config file as #338. The argv carries the path only.
   - Claude with `mcp: inherit`: extras are ignored, with a warning, because every server is already present.
   - Codex or opencode: extras are ignored and the run launches, with a warning naming the harness. The caller can't see which harness a role resolves to, so this is not an error.
7. **Persistence.**
   - The dispatch record stores the requested list.
   - Continuations and reopens (`leo_send_dispatch`, `--resume`) rebuild the same effective set, so a follow-up turn keeps its servers.
   - The state file's lifecycle is unchanged from #338: it is removed on release or close and swept on daemon start.
8. **Visibility.** The record and the `leo dispatch` status/watch output show the extra servers that were added, so Evan can see when a dispatch got more than its template gives it.

## Out of scope
- An operator allowlist or per-role restriction. Evan chose open access; add one later if needed.
- Narrowing or replacing the template list.
- `leo_consult`.
- Codex and opencode support.

## Acceptance
- A `leo_dispatch` call with `additional_mcp_servers: [chrome]` on a template with no `mcp_servers` produces a config file containing exactly the bridge and `chrome`. The file is 0600 and the argv carries only its path.
- If the template lists `[a]` and the call lists `[a, b]`, the effective set is the bridge, `a` and `b`.
- An unknown name produces a warning, and the run launches.
- A follow-up through `leo_send_dispatch` relaunches with the same effective set.
- A codex-resolved role with `additional_mcp_servers` launches with a warning.
- A request with 17 names, or a name containing `/`, gets a 400.
- With no `additional_mcp_servers` given, argv and config are byte-identical to v0.83.0.
