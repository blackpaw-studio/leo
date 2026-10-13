# Leo Chat

Leo Chat is a message thread per agent between you and your agents, served by
the daemon for the Leo iOS and Mac apps. The daemon owns the state; apps are
clients. Messages live in SQLite at `~/.leo/state/chat.db` and are kept
indefinitely; attachments live under
`~/.leo/state/chat/attachments/<agent>/<id>/<name>` and are pruned after 90
days (the row stays, marked unavailable). The database is versioned
(`PRAGMA user_version`) and upgraded in place, each step in a transaction, when
the daemon opens it; a database from a newer leo is refused.

## Config

```yaml
chat:
  whisper_model: ~/.leo/models/ggml-large-v3-turbo.bin
  permission_timeout: 120s

tasks:
  daily-news:
    owner: assistant
```

| Field | Default | Description |
|-------|---------|-------------|
| `chat.whisper_model` | unset | whisper.cpp ggml model used to transcribe voice notes. Unset or missing: a voice note is delivered with a "transcription failed" note. |
| `chat.permission_timeout` | `120s` | How long a Claude permission prompt waits for an answer from chat (at most `1h`). |
| `tasks.<name>.owner` | see below | Agent whose thread gets this task's cards. Validated as an agent name only; agents are runtime state, resolved when the card posts. |
| `push.chat` (defaults and templates) | on | Whether an agent's chat messages push to paired phones. |

A task's owner, when `owner` is unset: a `runtime: persistent` task's own
agent; otherwise the agent whose workspace equals the task's workspace;
otherwise `assistant`. An `owner` that names no agent posts to `assistant`
with a system note naming it.

### Voice notes

Transcription runs locally: `ffmpeg` converts the note to 16 kHz WAV and
`whisper-cli` (whisper.cpp) transcribes it. Both and the model are optional,
and `leo doctor` reports them. With Homebrew:

```bash
brew install whisper-cpp ffmpeg
mkdir -p ~/.leo/models
curl -L -o ~/.leo/models/ggml-large-v3-turbo.bin \
  https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-large-v3-turbo.bin
```

then set `chat.whisper_model`. No restart is needed.

## Delivery

A message you send is stored `pending`, transcribed if it is a voice note,
then delivered as a user turn, stopping at the first route that works: the
claude mod bridge, the agent's peer inbox, or a tmux paste. A stopped agent
is started first, with a "Starting…" note in the thread. The agent sees:

```
[Leo Chat] <text or transcript>
Attachments: /abs/path/a.png, /abs/path/b.pdf
In reply to task daily-news run <run id> (<time>): <card text, up to 1500 characters>
```

On success the message is `delivered`; on failure it is `failed`, with a
system note saying why. A message still `pending` when the daemon restarts
is `delivered` if it waits in the agent's bridge outbox, which delivers it
after the restart. Otherwise it is marked `failed` with a note to send it
again, never replayed twice.

**Replies.** For Claude agents the daemon captures the reply: on the agent's
`Stop` hook it finds the delivered `[Leo Chat]` prompt in the transcript and
posts that turn's last assistant message. It posts nothing when the agent
called `leo_chat_post` during that turn, when another prompt (a message, a task,
a task-notification) took over the turn, or when the reply is empty. A prompt
not yet in the transcript, for example one queued behind a running turn,
waits up to 30 minutes. Codex and opencode agents reply with `leo_chat_post`.

## Agent tools

Supervised agents and task runs get six MCP tools; `leo_dispatch`
subagents are refused.

| Tool | Arguments | Result |
|------|-----------|--------|
| `leo_chat_post` | `text`, `files` (paths, up to 10, 50 MB each; relative to the workspace) | `{id, agent}` |
| `leo_chat_ask` | `question`, `input` (below), `timeout_s` (default 300, max 3600); or `message_id` alone to resume | `{values, labels, text, timed_out, message_id, pending?}` |
| `leo_chat_history` | `limit` (max 200), `before`, `task` | a page (below) |
| `leo_chat_schedule` | `text` (Markdown), `at` (RFC3339 with an offset) | `{id, at}` |
| `leo_chat_schedule_list` | none | your pending reminders, soonest first: `[{id, at, text}]` |
| `leo_chat_schedule_cancel` | `id` | `{id, cancelled: true}` |

A headless task run has `LEO_TASK` and `LEO_RUN_ID` set, so its posts become
`task` cards in the owner's thread. A `runtime: persistent` task runs in an
agent that serves every turn, so the daemon files that agent's posts and asks
under the task run whose turn is in flight.

To show a file, post it: `leo_chat_post` with `text` as the caption and `files`.
To ask about a file, post it first and then call `leo_chat_ask`: an ask carries
no files, and the thread keeps the two adjacent.

### Ask inputs

`input` is `{type, options?, allow_text?, min?, max?, placeholder?}`, one per ask:

| `type` | Answered by | Options |
|--------|-------------|---------|
| `buttons` | tapping a choice, which submits at once | 1–12, required |
| `radio` | selecting one choice, then submitting | 1–12, required |
| `checkboxes` | selecting `min`–`max` choices, then submitting | 1–12, required |
| `text` | one free-text field | forbidden |

`min` and `max` apply to `checkboxes` only: `min` defaults to 0 and `max`
defaults to the number of options. An ask is rejected when `min < 0`,
`max < 1`, `min > max` or `max` exceeds the option count, or when a nonzero `min`
or any `max` is set on another input type.

Bare-string options are accepted only as MCP tool input; on the wire (API
and events) options are always `{value, label}` objects. `allow_text` (choice
types) also accepts a free-text answer, `placeholder` hints the text field. Omitting
`input` and giving top-level `options` asks `buttons`; giving both is an error.

An answer is `{values, text}`:

- `buttons`/`radio`: exactly one known value, or, with `allow_text`, no value
  and non-empty `text`.
- `checkboxes`: known, distinct values, `min ≤ count ≤ max`; with `allow_text`,
  `text` may accompany the values or replace them (an empty selection is
  allowed only with `text`, even if `min > 0`). Without `allow_text`, an empty
  selection is a valid answer when `min` is 0.
- `text`: no values, non-empty `text`.
- `text` is at most 4000 characters, and refused on a choice type without
  `allow_text`.

The tool returns `values` with their `labels` (same order) and the `text`;
on timeout `values` is empty and `timed_out` is true.

An ask's `timeout_s` deadline is kept by the daemon, not by the tool call.
One `leo_chat_ask` call waits at most 30 minutes, under Leo's 32-minute MCP
tool ceiling. If the question is still open then, the call returns
`pending: true` with its `message_id`, and calling `leo_chat_ask` again with
only that `message_id` keeps waiting. A call the harness abandons leaves the
ask open until its own deadline.

### Reminders

`leo_chat_schedule` posts `text` into your thread at `at`, which must be an
RFC3339 time with an offset (`2026-10-14T09:00:00-04:00`), strictly in the
future and at most a year out; anything else is rejected. Replies give `at` in
the daemon's local offset.

- **Thread.** A reminder belongs to the thread it will post into: your own
  for an agent, the task owner's for a task run (resolved when you schedule).
  List and cancel see only that thread's reminders. Cancelling an unknown or
  another thread's id is an error, as is cancelling one that has already
  fired or been cancelled.
- **Firing.** It posts a normal outgoing `text` message, so it pushes. From a
  task run it posts as a `task` card carrying the scheduling run's `task` and
  `run_id`.
- **Restarts.** Reminders are stored in `chat.db`, and the daemon fires any that
  came due while it was down, once each. One more than a minute overdue ends
  with a line such as `_(late — scheduled for Tue 14 Oct 2026 09:00 EDT)_`
  (daemon local time).

Recurring and editable reminders are not supported: cancel and schedule again.

## Permission prompts

Supervised Claude agents get a `PermissionRequest` hook. When Claude asks
for permission, the prompt posts to the agent's thread as a `permission`
message with `allow` and `deny` buttons, and the hook waits up to
`chat.permission_timeout` for your answer. Claude's own terminal dialog stays
up meanwhile, so either side can answer:

- Answered in chat: the hook returns the decision and the dialog closes.
- Answered in the terminal: Claude ends the hook and runs or skips the tool;
  the message becomes `answered` with answer `{values: ["terminal"]}`.
- No answer in time: the hook returns no decision, the dialog stays, and the
  message becomes `expired`.

Agents with `bypass_permissions` never ask.

## Task failure cards

When a `runtime: oneshot` task ends in failure (every attempt used, not
interrupted) and has `notify_on_fail: true`, the daemon posts a card into the
task owner's thread, whether or not the task has `channels`. Cron firings and
`leo run` both count. The card is a `task` message (`task`, `run_id` set) and
pushes like any other agent message:

```
**Task failed: <name>**
Run `<run_id>` · <n> attempt(s)
<first 500 characters of the error, in a code block>
Log: `<log path>`
```

The thread is the task's owner, resolved as for any task output, including the
fallback to `assistant` with a system note when the owner is not an agent. If
the task also has `channels`, the channel notification still fires; drop
`channels` at cutover to end the duplicate. Persistent tasks run inside their
agent and post no card, and interrupted runs post nothing. With the daemon
down or chat off, `leo run` prints a warning and the run's result is
unchanged.

## API

Every route takes the operator token or a paired-device token (agent and
API-client tokens are refused) and answers with the usual
`{ok, data, error?}` envelope. The daemon's unix socket serves the same paths
without a token for the Mac app.

| Route | Body | `data` |
|-------|------|--------|
| `GET /api/v1/chat/threads` | | `{threads: [Thread]}` |
| `GET /api/v1/chat/{agent}/messages?before=&limit=&task=` | | `{messages: [Message], has_more}`, newest first, `limit` ≤ 200 |
| `POST /api/v1/chat/{agent}/messages` | `{text, reply_to?, attachment_ids?}` | `Message` (`202`) |
| `POST /api/v1/chat/uploads` | multipart: one `file` part, optionally after an `agent` field; 50 MB | `Attachment` |
| `GET /api/v1/chat/attachments/{id}` | | the file, with `Range` support; `410` once pruned |
| `POST /api/v1/chat/{agent}/messages/{id}/answer` | `{values, text?}` | `Message`; `400` when the answer does not fit the message's `input`, `409` once settled |
| `POST /api/v1/chat/{agent}/read` | `{up_to}` | |

The socket also serves `POST /chat/task-failure`
`{task, run_id, attempts, error, log_path}` → `{id, agent}`, which `leo run`
uses to report a failed run for a [failure card](#task-failure-cards). It is
never served on TCP.

An upload without an `agent` belongs to the first message that sends it.
Audio uploads make that message a `voice` note.

**Message:** `{id, agent, direction (in|out), kind (text|voice|file|task|ask|permission|system),
text, transcript?, reply_to?, task?, run_id?, state, answer?: {values, text}, input?: {type, options?, allow_text?, min?, max?, placeholder?}, permission?: {tool, summary},
attachments: [{id, filename, mime, bytes, available}], ts}`; `state` is always
present (empty on outbound text) and `attachments` is `[]` when there are none. States: inbound
`pending|delivered|failed`; `ask` and `permission` `open|answered|expired`.

**Thread:** `{agent, last_message, unread, pinned}`. Only `assistant` is
pinned.

### Events

`GET /api/v1/events` (and the socket's `/events`) carries:

| Event | Payload beyond `seq`/`at` |
|-------|---------------------------|
| `chat.message` | `agent`, `message` |
| `chat.updated` | `agent`, `message`, `change` (`transcript`, `state` or `answer`) |

### Push

An agent's outbound messages (not permission prompts or system notes) push
as kind `chat`: title the agent, body the text or file names, plus
`message_id`, `message_kind`, `input_type` for an ask (always sent; omitted on
posts and permission prompts) and, for a `buttons` ask, `options` when they fit. A
permission prompt keeps the `needs_input` attention push as its only alert,
which carries the prompt's `message_id` with `message_kind: "permission"`.
Both go through the per-state [push policy](config-reference.md#pairing-a-phone).

## Trust boundary

The agent routes name the caller in the path, and every agent shares one
agent token, so an agent could post into another agent's thread by lying
about its name. The tools keep honest agents in their own thread; they are
not a security boundary between agents.
