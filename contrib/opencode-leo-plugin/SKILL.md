---
name: reply-to-container-agent
description: Reply to the containerized opencode agent that messages this agent via Leo. Use when a message arrives prefixed with a name containing "#ses_" (e.g. "docker-scout#ses_abc123"), or when you need to send that agent work unprompted. Delivers into the exact session that wrote to you.
---

# Replying to the container agent

The container runs an opencode server. You reply by posting into a **specific
session** on it — not by "continuing the last session", which lands in whatever
conversation happens to be newest and may not be the one that wrote to you.

## Configuration

Two values, supplied by your template's env:

- `CONTAINER_OPENCODE_URL` — e.g. `http://127.0.0.1:4096`
- `CONTAINER_CHANNEL_SESSION` — the pinned session created at container boot,
  used only when you are starting a conversation

## Replying to a message you received

Incoming messages are prefixed with the sender's reply address:

```
docker-scout#ses_00274ca11ffecQRpxqfQFjKbAk: build finished, 3 tests failing
```

Everything after `#` is the session id. Post to it:

```bash
curl -sS -X POST "$CONTAINER_OPENCODE_URL/session/ses_00274ca11ffecQRpxqfQFjKbAk/prompt_async" \
  -H 'Content-Type: application/json' \
  -d '{"parts":[{"type":"text","text":"Which 3? Paste the failing test names."}]}'
```

The message appears as a new turn in that session and the agent answers there.
Its answer comes back to you as another Leo message, not as this command's
output — this is message passing, not a request/response call.

## Starting a conversation

No reply address means no session to answer. Use the pinned channel session:

```bash
curl -sS -X POST "$CONTAINER_OPENCODE_URL/session/$CONTAINER_CHANNEL_SESSION/prompt_async" \
  -H 'Content-Type: application/json' \
  -d '{"parts":[{"type":"text","text":"..."}]}'
```

## Rules that are not optional

- **Use `/session/...`, never `/api/session/...`.** The `/api` mirror of these
  routes returns HTTP 200 with an `admittedSeq` and then silently never runs the
  turn. It looks like success and delivers nothing.
- **Never use `opencode run --attach --continue`.** It targets "the server's last
  session", which is a race with whatever the container's user is doing, and it
  returns no output anyway.
- **Post one message at a time** to a given session. Do not fan out concurrent
  posts into the same session.

## When it fails

- **404** — the session is gone (the container restarted). Fall back to
  `CONTAINER_CHANNEL_SESSION` and say that the previous thread was lost.
- **Connection refused** — the container is down. Report it; do not retry in a
  loop.
- **Delivered but no answer comes back** — the container agent may be mid-turn.
  The message is queued, not lost. Wait rather than re-sending.

## Reading a session's history

To see what was said in a session (yours or theirs):

```bash
curl -sS "$CONTAINER_OPENCODE_URL/session/<session-id>/message"
```
