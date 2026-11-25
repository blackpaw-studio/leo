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
| codex | yes (listed from the launch env; a Codex session never re-lists, so a switch flipped mid-session takes a relaunch and the tool refuses in the meantime) | yes: leo installs `leo dispatch context` as trusted SessionStart and UserPromptSubmit hooks in the agent's `CODEX_HOME`. Hooks there are home-global, and a child harness an agent starts inherits its launch token, so a session other than the agent's own is ignored. A new session id (a `/clear`) rebinds only from a SessionStart that reaches the daemon's unix socket (`LEO_SOCKET`, set at launch) from the agent's own harness: the daemon takes the caller's pid from the kernel (`LOCAL_PEERPID` on macOS, `SO_PEERCRED` on Linux), never from the request, and walks up from it to the first harness process (claude, codex, opencode). That process must be the harness running as the agent's tmux pane root, so a child harness the agent started is refused whatever it claims. A request over TCP, with no peer pid, or on any lookup error serves only the session already bound and never rebinds. |
| opencode | yes | yes: the `leo-turns` plugin asks the daemon before a session's first message (SessionStart, startup), after a compaction (SessionStart, compact) and on every message (UserPromptSubmit), and appends the answer to the message as a synthetic text part. The three requests of one message share a single 3 s budget, and the plugin makes them over the daemon's unix socket (`LEO_SOCKET`, node `http.request` with `socketPath`) so the daemon sees the kernel's peer pid and rebinds a new session only for the agent's own opencode process, by the same rule as codex. If the socket cannot be reached it falls back to TCP, which never rebinds. opencode cannot tell startup from resume; the session's earlier injections are already in its history. |

### Session rebind trust boundary

A harness that shares hooks or environment across processes (codex, opencode) can see a new session id from the agent's own harness or from a child it started, and both carry the agent's launch token. Only the first may take over the agent's brief and policy. The rules:

- The boundary is the agent's own process tree under its pane-root harness: the same principal as its launch token. Anything that holds the token already acts as the agent.
- Processes the agent spawns directly (a tool shell, `leo dispatch context` run by a hook) can rebind its own session.
- Nested harnesses (claude, codex or opencode started by the agent) are refused: each is the first harness ancestor of its own hooks, and that is not the pane-root harness.
- It fails closed on TCP, a missing peer pid, a caller reparented away from the tree (such as by a double fork), and any lookup error. None of these ever rebinds; a session already bound is still served.
- Accepted risk: there is no process-generation check. If the caller's pid is reused between the kernel accepting the connection and the daemon's process-table read, the request could be attributed to the wrong process.

## API

- `POST /api/agent/brief` (agent bearer): `{"token", "brief": {"goal"?, "status"?, "decisions"?, "open"?}}` → `200 {"ok": true, "brief": {...} | null}`. `400` invalid brief (unknown field, too long, too many items), `403` unknown or expired token, `404` no record, `409` briefs are off.
- `POST /api/agent/brief/offered` (agent bearer): `{"token", "offered"}` → `{"ok": true}`, `403` unknown or expired token. The agent's MCP server reports whether it currently offers `leo_brief` when it lists or flips the tool, and again on every event-stream (re)connection. The daemon files the report under the agent the token names (there is no agent field), per launch. The brief guidance (instruction, restored brief, staleness nudge) goes only to a session whose server reported `offered: true` while `brief.enabled` is on, so a session without the tool, such as one launched by a `leo` binary that predates briefs, is never told to use it. The state is in memory; a restarted daemon relearns it from the reconnecting servers.
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

  `agent_brief` carries the whole brief, or `brief: null` once cleared. `agent_spawned` carries the stored brief too, when an agent is respawned or renamed while running, so a stream consumer does not lose it. `brief_config_changed` fires when a config reload flips `brief.enabled`; it carries no brief text. An agent's own MCP server listens for it to list or withdraw `leo_brief`.
- `hello.features` advertises `agent_brief` whether the switch is on or off, so a client can show its setting; with it off the wire carries no briefs.

### Who can read it

A brief says what an agent is working on, so it follows the rule for other conversation-derived text (see [API clients](api-clients.md#agent-attention-and-held-work)): the operator token, a paired device, an SSO identity behind a trusted proxy and the unix socket see it. The agent token and API-client tokens never do: `brief` is absent from their `/state` rows and `agent_spawned` events, and `agent_brief` events are withheld from their streams (their `seq` has a gap, which clients already handle by refetching the snapshot).

## Known gaps

- Turning briefs off mid-session does not tell a running agent: its next `leo_brief` call fails with "briefs are off".
- An agent whose MCP server started before a daemon restart keeps the `leo_brief` listing it launched with until the next `brief_config_changed` event or relaunch.
