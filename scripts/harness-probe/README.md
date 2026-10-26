# harness-probe

Answers one question per coding-agent harness: does its MCP client re-list
tools after `notifications/tools/list_changed`, and can the model see an image
in a tool result? The answers feed `internal/mcp/harnesscaps.go`
(`honorsToolListChanged`): honoring clients get client tools as native MCP
tools, the rest get the fixed `leo_app_tools` / `leo_app_call` fallback.

`probeserver/` is a stdio MCP server exposing `probe_ping`; calling it returns
`pong` and then emits `list_changed`, after which `tools/list` also holds
`probe_late` (text token + an 8x8 red PNG). Every request is logged as JSONL to
`$PROBE_LOG`.

`run.sh [claude|codex|opencode|all]` builds the server, then drives each
installed harness twice: interactively in a private tmux server
(`tmux -L leo-probe`) and headless. The environment is scrubbed (`env -i`) so
no `LEO_*`/`TMUX`/`CLAUDE_*` state leaks in. Results merge into
`results/<UTC date>.json`, with the final pane of each interactive run beside it.
`PROBE_WAIT` (default 180s) bounds the wait for `probe_late`; `PROBE_KEEP=1`
keeps the temp dir.

Re-run after upgrading a harness and update `harnesscaps.go` from `results/`.
Don't edit `run.sh` while it is running.

## 2026-10-26

| harness | version | clientInfo.name | honors list_changed | image seen |
|---|---|---|---|---|
| Claude Code | 2.1.310 | `claude-code` | yes (interactive + headless) | yes |
| codex | 0.162.1 | `codex-mcp-client` | no, never re-lists in-session (interactive + headless) | n/a, never reached `probe_late` |
| opencode | 1.18.18 | `opencode` | yes (interactive + headless) | yes |

An earlier opencode interactive run read `honors: false` because of a probe bug,
not the client: opencode paints its input box before MCP connects, then raises
an "Update Available" modal that swallowed the typed prompt. `run.sh` now waits
for the MCP footer and Escapes that modal (Enter would confirm the update)
before typing.
