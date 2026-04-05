# Telegram Setup

This guide walks you through creating a Telegram bot and connecting it to Leo.

## Step 1: Create a Bot with BotFather

1. Open Telegram and search for [@BotFather](https://t.me/BotFather)
2. Send `/newbot`
3. Choose a **display name** for your bot (e.g., "Leo Assistant")
4. Choose a **username** ending in `bot` (e.g., `leo_assistant_bot`)
5. BotFather replies with your **bot token** — copy it

The token looks like: `123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11`

!!! warning "Keep your token secret"
    The bot token grants full control of your bot. Never commit it to version control or share it publicly.

## Step 2: Get Your Chat ID

During `leo setup`, Leo auto-detects your chat ID:

1. The wizard prompts you to send any message to your bot on Telegram
2. Leo polls the Telegram `getUpdates` API
3. When it receives your message, it extracts your chat ID

You can also find your chat ID manually by sending a message to your bot and visiting:

```
https://api.telegram.org/bot<YOUR_TOKEN>/getUpdates
```

Look for `"chat":{"id":123456789}` in the response.

## Step 3: Configure in Leo

The setup wizard handles this automatically. If configuring manually, add to `leo.yaml`:

```yaml
telegram:
  bot_token: "123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11"
  chat_id: "123456789"
```

## Optional: Forum Groups and Topics

Telegram forum groups (supergroups with topics enabled) let you organize messages into threads. Leo can route different tasks to different topics.

### Setup

1. Create a Telegram group and enable "Topics" in group settings
2. Add your bot to the group as an admin
3. Create topics for your use case (e.g., "Alerts", "News", "Reports")
4. Get the group ID and topic thread IDs

### Configuration

```yaml
telegram:
  bot_token: "YOUR_TOKEN"
  chat_id: "YOUR_PERSONAL_CHAT_ID"
  group_id: "-1001234567890"
  topics:
    alerts: 1
    news: 3
    reports: 7
```

### Task Routing

Reference topic names in your task definitions:

```yaml
tasks:
  heartbeat:
    topic: alerts       # routes to telegram.topics.alerts (thread ID 1)
  daily-briefing:
    topic: news         # routes to telegram.topics.news (thread ID 3)
```

## Troubleshooting

### Bot not responding

- Make sure you've started a conversation with your bot (send `/start`)
- Check that the bot token is correct
- Verify the chat ID matches your conversation

### Messages going to the wrong place

- Personal messages use `chat_id`
- Group messages use `group_id` with optional `topic` routing
- If `topic` is set but `group_id` is empty, messages go to your personal chat

### Test message failed

Run a manual test with `curl`:

```bash
curl -s -X POST "https://api.telegram.org/bot<TOKEN>/sendMessage" \
  -d chat_id=<CHAT_ID> \
  -d text="Test from Leo"
```

If this works but `leo setup` doesn't, check your config file for typos.
