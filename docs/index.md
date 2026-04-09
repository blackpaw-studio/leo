---
hide:
  - navigation
---

# Leo

**Manage a persistent Claude Code assistant**

Leo is a CLI tool that sets up and manages a persistent, mobile-accessible [Claude Code](https://docs.anthropic.com/en/docs/claude-code) personal assistant. It handles workspace scaffolding, persistent memory, Telegram integration, and cron scheduling — giving your assistant continuity across sessions and the ability to work on a schedule or respond to messages from your phone.

Leo is not a multi-agent orchestration framework. It manages one workspace, one config file.

---

<div class="grid cards" markdown>

-   :material-chat-outline:{ .lg .middle } **Interactive Service**

    ---

    Start a long-running Telegram session where your assistant listens and responds to messages in real time via the official channel plugin.

    [:octicons-arrow-right-24: Start chatting](guides/telegram-setup.md)

-   :material-clock-outline:{ .lg .middle } **Scheduled Tasks**

    ---

    Define background tasks with cron expressions. Leo assembles prompts and invokes `claude` on a schedule — no daemon required.

    [:octicons-arrow-right-24: Set up scheduling](guides/scheduling.md)

-   :material-file-cog-outline:{ .lg .middle } **Configuration**

    ---

    A single `leo.yaml` controls your assistant, Telegram credentials, defaults, and task schedules. Full field-by-field reference included.

    [:octicons-arrow-right-24: Config reference](configuration/config-reference.md)

</div>

---

## How It Works

Leo operates in two modes, both invoking the stock `claude` CLI:

### Interactive Mode

```
User (Telegram) --> Telegram Bot API --> claude (channel plugin) --> Agent
                                                                      |
User (Telegram) <-- Telegram Bot API <-- claude (channel plugin) <----+
```

`leo service start` starts a long-running Claude session with the official Telegram channel plugin. Messages flow through Telegram in both directions.

### Scheduled Tasks

```
cron --> leo run <task> --> claude -p "<assembled prompt>" --> Agent
                                                                |
                          User (Telegram) <-- curl Bot API <----+
```

System cron calls `leo run <task>`, which reads the config, assembles a prompt, and invokes `claude -p` in non-interactive mode. If the agent has something to report, it sends a Telegram message via `curl`.

---

## Quick Install

=== "Install Script"

    ```bash
    curl -fsSL https://raw.githubusercontent.com/blackpaw-studio/leo/refs/heads/main/install.sh | sh
    ```

=== "Go"

    ```bash
    go install github.com/blackpaw-studio/leo@latest
    ```

=== "Source"

    ```bash
    git clone https://github.com/blackpaw-studio/leo.git
    cd leo && make install
    ```

Then run the setup wizard:

```bash
leo setup
```

[:octicons-arrow-right-24: Full installation guide](getting-started/installation.md){ .md-button }
[:octicons-arrow-right-24: Quick start](getting-started/index.md){ .md-button .md-button--primary }
