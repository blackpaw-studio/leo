# Agent Brief

An agent can keep a short **brief** of its current work: what it is trying to do, where it stands, what was decided and what it is waiting on. Leo's apps show it so you can pick the thread back up without reading a transcript. The agent writes it itself, through a tool; no separate summarizer runs. It is **off by default**.

## Turning it on

```yaml
brief:
  enabled: true   # absent means false
```

Or `PUT /config/brief` with `{"values": {"enabled": true}}` and an `If-Match` revision, the same as any [config resource](config-api.md) (the apps' "Agent briefs" switch does this). It is per machine and takes effect on the next config reload, with no restart.

While it is off, Leo injects no guidance, sends no nudges, does not offer `leo_brief` to agents and leaves briefs off the API. Stored briefs are kept, so turning it back on restores them.

## What an agent writes

The `leo_brief` MCP tool replaces the whole brief on every call; a call with no arguments clears it.

| field | rules |
|---|---|
| `goal` | one line, at most 140 characters |
| `status` | current state or next step, one line, at most 140 characters |
| `decisions` | up to 5 items, each at most 140 characters |
| `open` | open items or what the agent waits on: up to 5 items, each at most 140 characters |
| `updated_at` | daemon clock, set on every write |

Text is plain: control characters are stripped and newlines read as spaces. A field that is too long, or a list with too many items, is an error the agent reads (`400` names the field and the limit); it is never silently cut. Empty strings and empty items are dropped, so a call with nothing left clears the brief.

Like `leo_progress`, the tool authenticates with the agent's per-launch attention token and has no agent argument: an agent writes only its own brief. Agents Leo did not launch with hooks, and `leo_dispatch` subagents, get a readable error. A template's `deny_tools` can name `leo_brief`.

The brief is stored on the agent's record in `agents.json`. It survives a daemon restart, `/clear`, compaction and resume, moves with the agent on `leo agent move` (a `copy` starts without one) and is deleted with the agent.

## How agents learn to use it

Leo tells a Claude agent through its context hooks (the same `leo dispatch context` hook that carries [delegation](delegation.md) context):

- **SessionStart** (startup, resume, clear, compact, fork): a short instruction to keep the brief current and to update it only when its goal, status or next step, decisions or open items change. After `clear`, `compact` or `resume`, the agent's stored brief follows, so it knows where it was.
- **UserPromptSubmit**: the instruction again only when this session has not heard its current text (briefs were switched on mid-session). Plus a one-line **nudge** ("your Leo brief is missing" or "may be stale") once the agent has completed 10 turns without writing its brief, and at most once per 10 turns. The turn count lives in daemon memory, so a restart can only delay a nudge, never double it.

The hooks cost a read of the agent's record only when a stored brief is restored or a nudge is due.

Codex and opencode get the same guidance through their own hooks. See [Harness support](#harness-support).

### Harness support

| Harness | `leo_brief` | Instruction, restore, nudge |
|---|---|---|
| claude | yes | yes |
| codex | yes (listed from the launch env; a Codex session never re-lists, so a switch flipped mid-session takes a relaunch and the tool refuses in the meantime) | yes: leo installs `leo dispatch context` as trusted SessionStart and UserPromptSubmit hooks in the agent's `CODEX_HOME`. Hooks there are home-global, so a session that is not the agent's own is ignored. |
| opencode | yes | yes: the `leo-turns` plugin asks the daemon before a session's first message (SessionStart, startup), after a compaction (SessionStart, compact) and on every message (UserPromptSubmit), and appends the answer to the message as a synthetic text part. opencode cannot tell startup from resume; the session's earlier injections are already in its history. |

## API

- `POST /api/agent/brief` (agent bearer): `{"token", "brief": {"goal"?, "status"?, "decisions"?, "open"?}}` → `200 {"ok": true, "brief": {...} | null}`. `400` invalid brief (unknown field, too long, too many items), `403` unknown or expired token, `404` no record, `409` briefs are off.
- `POST /api/agent/brief/config` (agent bearer): `{"token"}` → `{"ok": true, "data": {"enabled": true}}`, `403` unknown or expired token. The agent's MCP server asks on every event-stream (re)connection, so a switch flipped while it was disconnected still re-lists `leo_brief`.
- `GET /api/v1/state` and the daemon socket's `GET /state`: an agent row carries `brief` when the agent has one and briefs are on.

```json
"brief": {"goal": "…", "status": "…", "decisions": ["…"], "open": ["…"], "updated_at": "2026-11-25T02:00:00Z"}
```

  `decisions` and `open` are omitted when empty.
- `/api/v1/events` and the socket's `/events`:

```json
{"type": "agent_brief", "seq": 88, "at": "…", "agent": "luna-dev", "brief": {…}}
{"type": "agent_brief", "seq": 90, "at": "…", "agent": "luna-dev", "brief": null}
{"type": "brief_config_changed", "seq": 89, "at": "…", "enabled": false}
```

  `agent_brief` carries the whole brief, or `brief: null` once cleared. `brief_config_changed` fires when a config reload flips `brief.enabled`; it carries no brief text. An agent's own MCP server listens for it to list or withdraw `leo_brief`.
- `hello.features` advertises `agent_brief` whether the switch is on or off, so a client can show its setting; with it off the wire carries no briefs.

### Who can read it

A brief says what an agent is working on, so it follows the rule for other conversation-derived text (see [API clients](api-clients.md#agent-attention-and-held-work)): the operator token, a paired device, an SSO identity behind a trusted proxy and the unix socket see it. The agent token and API-client tokens never do: `brief` is absent from their `/state` rows and `agent_spawned` events, and `agent_brief` events are withheld from their streams (their `seq` has a gap, which clients already handle by refetching the snapshot).

## Known gaps

- Turning briefs off mid-session does not tell a running agent: its next `leo_brief` call fails with "briefs are off".
- An agent whose MCP server started before a daemon restart keeps the `leo_brief` listing it launched with until the next `brief_config_changed` event or relaunch.
