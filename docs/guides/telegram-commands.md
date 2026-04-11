# Telegram Commands & Tools

Leo's Telegram plugin provides bot commands for controlling Leo and a set of MCP tools that Claude can use during conversations.

## Bot Commands

These commands are available in your Telegram chat with the bot.

| Command | Description |
|---------|-------------|
| `/agent [template] [repo]` | Spawn an ephemeral agent. With no args, shows template picker. With args, spawns directly. |
| `/agents` | List running agents with inline stop buttons. |
| `/tasks` | List all tasks with run and enable/disable buttons. |
| `/stop` | Interrupt the current Claude process (sends Ctrl+C). |
| `/clear` | Clear conversation context and history. |
| `/compact` | Compact the conversation context to free up token space. |

### Agent Dispatch

`/agent` supports two modes:

**Interactive** — send `/agent` with no arguments to get an inline keyboard of configured templates. Pick a template, then type the repo name.

**Direct** — send `/agent coding owner/repo` to spawn immediately. The template name and repo are positional.

### Task Control

`/tasks` displays all configured tasks with inline buttons:

- **Run** — trigger a task immediately
- **Enable/Disable** — toggle a task's enabled state

## MCP Tools

The Telegram plugin registers these tools that Claude can use during a conversation.

### `reply`

Send a message back to the chat. Supports MarkdownV2 formatting and file attachments.

- `text` — message content (MarkdownV2 parsed)
- `files` — array of absolute file paths to attach (images, documents, up to 50MB each)
- `reply_to` — message ID to quote-reply (optional)
- `thread_id` — forum topic thread ID (optional)

### `react`

Add an emoji reaction to a message.

- `message_id` — the message to react to
- `emoji` — a single emoji from the supported set

### `edit_message`

Edit a previously sent message. Useful for progress updates without sending new notifications.

- `message_id` — the message to edit
- `text` — new message content

### `ask_user`

Send a question with inline buttons and wait for the user's choice. Times out after 120 seconds.

- `question` — the question text
- `options` — array of button labels

### `get_history`

Retrieve recent message history from the chat.

- `limit` — number of messages (default 50, max 200)

### `search_messages`

Search message history by text pattern (case-insensitive).

- `query` — search text

### `clear_history`

Clear stored chat history. Optionally signals the daemon to restart the Claude session.

### `save_memory`

Store a short (2-3 sentence) summary for persistence across session resets. Loaded automatically on startup.

### `schedule`

Create, list, or delete scheduled messages.

- **`at`** — one-shot: ISO 8601 timestamp or relative (e.g., `+2h`)
- **`every`** — recurring: interval in milliseconds

### `voice_reply`

Send a text-to-speech voice message. Requires `ELEVENLABS_API_KEY` in the process environment.

- `text` — content to speak (keep under 500 characters)

### `create_telegraph_page`

Publish long-form content (3000+ characters) to Telegraph for Instant View rendering. Requires `TELEGRAPH_ENABLED` in config.

- `title` — page title
- `content` — HTML or markdown content
