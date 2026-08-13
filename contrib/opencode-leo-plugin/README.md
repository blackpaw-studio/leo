# opencode ↔ Leo messaging plugin

Two-way messaging between a Leo agent and an **opencode agent Leo does not
supervise** — typically one running in Docker. See
`docs/specs/2026-08-13-external-agent-messaging.md` for the full design.

## Why a plugin and not `leo mcp-server`

opencode's MCP client passes no session identity: a `tools/call` carries exactly
`{name, arguments}` — no `_meta`, and no `OPENCODE_SESSION_*` in the server's env.
An MCP-based integration therefore cannot tell Leo *which session* to reply to,
leaving only heuristics ("the last session", "the busy session") that misroute the
moment the container has more than one session in play.

A plugin tool receives `ToolContext.sessionID` on every call. The originating
session travels with the message, so the reply lands in the right conversation
even if the container's user has switched sessions since.

## Container side

Drop `leo.ts` in the image and reference it from opencode's config:

```json
{ "plugin": ["/app/.opencode/plugin/leo.ts"] }
```

(A `.opencode/plugin/*.ts` file in the working directory is picked up too.)

Environment — all four required:

| Variable | Meaning |
|---|---|
| `LEO_URL` | Leo daemon base URL, e.g. `http://host.docker.internal:8370` |
| `LEO_TOKEN` | bearer token; scope it via `api_clients` in `leo.yaml` |
| `LEO_TARGET` | the one Leo agent this container may message |
| `LEO_CLIENT_NAME` | this container's identity, matching its `api_clients` entry |

The agent gets exactly one tool, `message_leo(text)`. There is nothing else to
deny — the restriction is structural rather than a guardrail.

Run opencode headless: `opencode serve --port 4096 --hostname 0.0.0.0`. A TUI is
not required — prompts posted to a session execute with no client attached.

## Leo side

Copy `SKILL.md` into the target agent's workspace as
`.claude/skills/reply-to-container-agent/SKILL.md`, and set on its template:

- `CONTAINER_OPENCODE_URL` — e.g. `http://127.0.0.1:4096`
- `CONTAINER_CHANNEL_SESSION` — a session created at container boot
  (`POST /session`), used when Leo starts a conversation rather than replying

## Verified behavior (opencode 1.17.7)

- `POST /session/{id}/prompt_async` delivers into a specific session and runs with
  no client attached.
- **Use the non-`/api` routes.** `POST /api/session/{id}/prompt` returns HTTP 200
  with an `admittedSeq` and then silently never executes the turn.
- `opencode run --attach` is fire-and-forget with no output, and `--continue`
  means "the server's last session" — both unusable for addressed replies.
- End to end: a tool call in session A reported `from: <client>#<A>` while a
  newer session B existed; the reply posted to A landed in A, and B stayed empty.
