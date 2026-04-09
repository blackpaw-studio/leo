---
hide:
  - navigation
---

# Leo

**Manage a persistent Claude Code assistant**

Leo is a CLI tool that sets up and manages persistent, mobile-accessible [Claude Code](https://docs.anthropic.com/en/docs/claude-code) sessions. It handles workspace scaffolding, Telegram integration, and cron scheduling -- giving your assistant continuity across sessions and the ability to work on a schedule or respond to messages from your phone.

Memory is not built-in -- users configure their preferred memory MCP server in the standard `~/.claude/mcp-servers.json` or the workspace-specific `config/mcp-servers.json`.

---

<div class="grid cards" markdown>

-   :material-chat-outline:{ .lg .middle } **Interactive Service**

    ---

    Define multiple persistent Claude sessions with different channels, workspaces, and settings. Leo supervises them with auto-restart.

    [:octicons-arrow-right-24: Start chatting](guides/telegram-setup.md)

-   :material-clock-outline:{ .lg .middle } **Scheduled Tasks**

    ---

    Define background tasks with cron expressions. Leo assembles prompts and invokes `claude` on a schedule -- no daemon required.

    [:octicons-arrow-right-24: Set up scheduling](guides/scheduling.md)

-   :material-file-cog-outline:{ .lg .middle } **Configuration**

    ---

    A single `leo.yaml` at `~/.leo/` controls Telegram credentials, defaults, processes, and task schedules. Full field-by-field reference included.

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

`leo service start` starts all enabled processes in supervised mode. Each process is a long-running Claude session that can use different channels (Telegram, etc.), workspaces, and models.

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
