# Codex stream fixtures

Captured 2026-07-10 from `codex exec --json` (codex-cli 0.144.1).

- `fresh.jsonl` — successful fresh run (`thread.started` → `agent_message` → `turn.completed`).
- `resume.jsonl` — `codex exec --json --skip-git-repo-check resume <id> <prompt>`; note `thread.started` echoes the resumed thread id.
- `badmodel.jsonl` — `--model not-a-real-model`: non-fatal `item` error, then top-level `error` and `turn.failed` (exit 1).
- `mcp_tool_call.jsonl` — successful MCP tool call via `-c mcp_servers.leo.*` overrides with `default_tools_approval_mode="approve"`. The tool result payload and final message were sanitized to a fake two-agent inventory; event/item structure is byte-faithful.
- `mcp_cancelled.jsonl` — the same call WITHOUT the approval-mode override: headless exec auto-cancels MCP tool calls (`user cancelled MCP tool call`, item status `failed`), yet the turn completes with exit 0.

Stale-resume and non-git-workspace failures produce **no stdout** (errors go to stderr) — that's why there is no fixture for them; see the runner's stale-session text patterns.
