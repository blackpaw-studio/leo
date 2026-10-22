# Client chat post (`can_post`)

Requested by Rocket on Evan's behalf, 2026-10-21. Windmill pipelines (brooklyn_cam, wharf_cam) send notifications through the retired Telegram bot. They should post straight into a Leo Chat thread instead, with no agent turn: today a `leo client` token can only start one through POST /web/agent/<target>/message.

## Scope
- New client scope, `can_post`: a list of agent names or `path.Match` globs, validated the same way as `can_message`.
  - Set with `leo client add <name> --can-post <glob>`, which is repeatable.
  - A client needs at least one of `--can-message` or `--can-post`.
  - The two scopes are independent: `can_post` never grants `message`, and `can_message` never grants `chat/post`.
- Stored in `leo.yaml` as `api_clients.<name>.can_post`. The existing token file is unchanged.

## Endpoint
`POST /web/agent/<target>/chat/post`
- Auth: `Authorization: Bearer <client token>`.
- Body: `{"from": "<client name>", "text": "<message>"}`.
  - `from` follows the same rule as `/message`: it must equal the client name, or `<name>#<session>`.
  - `text` is sanitized like `/message` and is required (non-empty). It uses the same size cap as `leo_chat_post`.
- Success: `200 {"ok":true,"data":{"id":"<message id>"}}`.
- Errors:
  - 401 for a bad or missing token.
  - 403 when the target isn't allowed by `can_post`, and for any other path, method or `/api` route.
  - 400 for a bad `from` or empty text.
  - 404 when the target isn't a known agent.
- Effect: the same as `leo_chat_post`. The message is stored in `<target>`'s thread as an outbound message and pushed to devices. No turn is injected and no agent is woken.
- Attachments are out of scope for now.

## Sender
- A new nullable `sender` column on chat messages (an additive migration), exposed as `"sender"` in message JSON.
  - It is `client:<name>` for client posts and empty for agent-authored messages. The `#session` suffix isn't included.
- The push notification body is prefixed with `<name>: `, so the sender is visible before the apps render the field.
- leoterm is told about the new `sender` field so it can render it.

## Hot reload
- `daemon.ReloadConfig` (the path behind `leo service reload` and `/config/reload`) also rebuilds the client policies and swaps them into the web server under a lock.
- `leo client add` and `leo client rm` trigger that reload when the daemon is running.
  - New and changed clients take effect, and removed clients lose access, without a restart.
  - If the daemon isn't running, the change applies at the next start, as it does today.

## Tests (test-first)
- A `can_post` client:
  - can post to an allowed target, and the message lands with `sender` set;
  - gets 403 on a disallowed target, on `/message`, and on `/api/*`.
- A `can_message`-only client gets 403 on `chat/post`.
- The post path never calls the turn-injection or delivery code.
- `sender` round-trips through the store, including after the migration on an existing database.
- Reload: a client added after start works without a restart, and a removed client gets 401.
- CLI: the `--can-post` flag and its validation.

## Ships in
The next minor release, after Evan confirms the release.
