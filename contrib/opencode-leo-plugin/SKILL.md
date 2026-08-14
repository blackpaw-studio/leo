---
name: reply-to-container-agent
description: Reply to the containerized opencode agent that messages this agent via Leo. Use when a message arrives as "[message from <name>#ses_...]", or when you need to send that agent work unprompted. Delivers into the exact session that wrote to you.
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

Incoming messages carry the sender's reply address in Leo's standard prefix:

```
[message from docker-scout#ses_00274ca11ffecQRpxqfQFjKbAk] build finished, 3 tests failing
```

The part after `#` and before `]` is the session id. Post to it — build the JSON
with `jq` so quotes, backticks, `$`, and newlines in your reply can't break the
command or inject shell:

```bash
SES=ses_00274ca11ffecQRpxqfQFjKbAk

# Quoted heredoc: nothing inside is expanded, so $, backticks and quotes in your
# reply are just characters. Never write REPLY="...your text..." — a reply that
# happens to contain $(...) or a quote would run on this machine.
REPLY=$(cat <<'LEOEOF'
Which 3? Paste the failing test names.
LEOEOF
)

jq -n --arg t "$REPLY" '{parts:[{type:"text",text:$t}]}' \
  | curl -sS --fail-with-body -X POST \
      "$CONTAINER_OPENCODE_URL/session/$SES/prompt_async" \
      -H 'Content-Type: application/json' --data-binary @- \
  && echo "delivered"
```

`jq` builds the JSON so quotes and newlines can't break it; the quoted heredoc
stops the shell touching the text before jq sees it. `--fail-with-body` makes
curl exit non-zero on 4xx/5xx — without it curl exits 0 on a 404 and you will
read an error body as success.

The message appears as a new turn in that session and the agent answers there.
Its answer comes back to you as another Leo message, not as this command's
output — this is message passing, not a request/response call.

## Starting a conversation

No reply address means no session to answer. Use the pinned channel session —
same command shape, with `$CONTAINER_CHANNEL_SESSION` in place of `$SES`, and
the same quoted heredoc for the text.

## Rules that are not optional

- **Use `/session/...`, never `/api/session/...`.** The `/api` mirror of these
  routes returns HTTP 200 with an `admittedSeq` and then silently never runs the
  turn. It looks like success and delivers nothing.
- **Never use `opencode run --attach --continue`.** It targets "the server's last
  session", which is a race with whatever the container's user is doing, and it
  returns no output anyway.
- **Always build the body with `jq`, and always put the text in a quoted
  heredoc.** Never interpolate message text into a shell string — neither
  `'{"text":"..."}'` nor `REPLY="..."`. Both let the container's text run
  commands here.
- **Post one message at a time** to a given session. Do not fan out concurrent
  posts into the same session.

## When it fails

Check curl's exit status, not just its output.

- **404** — the session is gone (the container restarted). Fall back to
  `CONTAINER_CHANNEL_SESSION` and say that the previous thread was lost.
- **Connection refused / exit 7** — the container is down. Report it; do not
  retry in a loop.
- **Delivered but no answer comes back** — the container agent may be mid-turn.
  The message is queued, not lost. Wait rather than re-sending.

## Reading a session's history

```bash
curl -sS --fail-with-body "$CONTAINER_OPENCODE_URL/session/<session-id>/message"
```
