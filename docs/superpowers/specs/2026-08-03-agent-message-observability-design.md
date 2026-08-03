# Agent-to-agent message activity on the observability API

Status: approved
Date: 2026-08-03

## Problem

The observability API (`GET /api/v1/state`, `GET /api/v1/events`) reports what
each agent *is*, never that two of them are talking. The Den kiosk wants to walk
two characters into a conference room when a pair is actually messaging, and has
no signal to drive it from.

Requested by Evan via the Den agent; shape agreed with Den before implementation.

## Goals

- A consumer can tell, live, that agent A messaged agent B.
- A consumer reconnecting mid-conversation can seed the pairs it missed.
- No message content is ever exposed.

Non-goals: message bodies; delivery/read receipts; conversation threading;
anything an authenticated identity would be needed for (see Trust below).

## The wrinkle: leo doesn't know who sent a message

`leo_send_message` bakes the sender's name into the message **text**
(`msgPrefixFormat` in internal/mcp/tools.go) and posts only `{"text": …}` to
`POST /web/agent/{name}/message`. Nothing structural identifies the sender, and
recovering it by parsing the body would mean inspecting content — exactly what
this feature must not do.

So sender identity is threaded through as a real field: the MCP tool sends its
own `LEO_PROCESS_NAME` as `from`, and the handler reads it. The message text is
unchanged (recipients still see the prefix).

## Design

### Event

New SSE event on the existing stream, additive per the contract's
"ignore unknown types" rule:

```
event: agent_message
data: {"seq": 41, "at": "2026-08-03T19:47:10Z", "from": "chronicle", "to": "plex"}
```

```go
const EventAgentMessage EventType = "agent_message"

type AgentMessagePayload struct {
    Meta                 // supplies seq + at
    From string `json:"from,omitempty"`
    To   string `json:"to"`
}
```

`seq`/`at` come from the `Meta` every payload embeds — no separate timestamp.

**`from` is omitted when the sender is not an agent** (a human messaging from
the web UI). Consumers wanting agent-to-agent activity require both fields;
Den does. Leo does not invent a sender.

Published by the web handler once a message is accepted for delivery, so a
rejected message (unknown target, delivery failure) emits nothing.

### Snapshot

`Snapshot` gains one field, additively — `SnapshotVersion` stays 1:

```go
RecentMessages []AgentMessage `json:"recent_messages"`

type AgentMessage struct {
    From string    `json:"from,omitempty"`
    To   string    `json:"to"`
    At   time.Time `json:"at"`
}
```

Newest-last, capped at 50 entries, entries older than 10 minutes dropped on
read. Den applies its own tighter (~3–5 min) window on top, so these bounds only
need to exceed it.

### Recording

`MessageLog` mirrors `RunLog` exactly: a `Publisher` that wraps the next
`Publisher`, recording `AgentMessagePayload` as it passes through and forwarding
everything unchanged. Same reasoning as RunLog's doc comment — a bus
*subscriber* can be dropped when slow, which would silently lose entries and
make `recent_messages` untrustworthy; wrapping makes recording synchronous with
publish.

Wiring becomes `messageLog → runLog → bus`, and `web.WithMessageLog` supplies
the snapshot's read seam alongside `WithRunLog`. Both seams stay optional: nil
yields an empty list, as `runLog` already does.

### Trust

`from` is **self-asserted** by the calling agent — an agent could send any name.
That is acceptable for animating a kiosk and is documented as such on both
sides; it must not be used for authorization or attribution. Same bearer-token
gating as the rest of `/api/v1`.

## Testing

- Payload/event: JSON shape, `from` omitted when empty, `Meta` stamping.
- `MessageLog`: records only message payloads, forwards everything, newest-last
  ordering, capacity trim, age filtering on read, and concurrent publish under
  `-race`.
- Handler: publishes on accepted delivery with both names; publishes with `from`
  omitted when unset; publishes nothing when delivery is rejected; **never
  includes message text in the payload**.
- MCP: `leo_send_message` sends its process name as `from`, and the delivered
  text is byte-identical to today.
- Snapshot: `recent_messages` present and bounded; empty (not null) with no log.

## Risks

- **A chatty pair floods the stream.** Bounded by the cap on the snapshot side;
  the event side is one event per message, matching how `agent_activity`
  already behaves. Not throttled — if it becomes a problem, throttling belongs
  in the bus, not here.
- **Spoofed `from`.** Accepted and documented; animation-only.
