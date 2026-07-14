# Leo

**An agent supervisor and task scheduler for Claude Code**

Leo spawns and supervises [Claude Code](https://docs.anthropic.com/en/docs/claude-code) agents and schedules autonomous tasks — including tasks that inject into a long-running agent instead of spawning a fresh process each time. Leo is channel-agnostic — bring your own messaging channel via any Claude Code plugin (Telegram, Slack, webhook, etc.). A built-in web dashboard lets you manage everything from a browser.

---

<div class="grid cards" markdown>

-   :material-chat-outline:{ .lg .middle } **Persistent Tasks**

    ---

    Point a `runtime: persistent` task at a template and Leo injects its prompt into that agent's tmux session — spawning or resuming it on demand — instead of spawning `claude -p` per firing.

    [:octicons-arrow-right-24: Configuration](configuration/persistent-tasks.md)

-   :material-rocket-launch-outline:{ .lg .middle } **Agent Templates**

    ---

    Define reusable blueprints and spawn ephemeral coding agents from the HTTP API, a channel plugin, or the web UI. Agents clone repos, run in isolated sessions, and appear in claude.ai.

    [:octicons-arrow-right-24: Agent guide](guides/agents.md)

-   :material-clock-outline:{ .lg .middle } **Scheduled Tasks**

    ---

    Cron-driven tasks that invoke Claude in non-interactive mode. Write a prompt, set a schedule, and Leo handles prompt assembly and execution.

    [:octicons-arrow-right-24: Set up scheduling](guides/scheduling.md)

-   :material-monitor-dashboard:{ .lg .middle } **Web Dashboard**

    ---

    Monitor agents, manage tasks, spawn agents, edit config, and preview cron schedules from a browser on your LAN.

    [:octicons-arrow-right-24: Configuration](configuration/config-reference.md)

</div>

---

## How It Works

Leo operates in three modes, all invoking the stock `claude` CLI:

### Persistent Tasks

```
User (channel) --> Channel plugin --> claude --> Agent
                                                   |
User (channel) <-- Channel plugin <-- claude <----+
```

`leo service start` boots the daemon, which supervises every configured agent — including the ones backing `runtime: persistent` tasks. Rather than spawning a session at boot, a persistent task's target agent is spawned or resumed on its first firing (an "ensure-exists" step), then reused across subsequent firings: a long-running Claude process with its own workspace, model, and channel plugin list, restarted on crash with exponential backoff.

### Agent Templates

```
HTTP / channel plugin / CLI --> Leo daemon --> tmux session
                                                    |
User (claude.ai) <-- --remote-control --name leo-<template>-... <------+
```

Templates let you spawn ephemeral agents on demand. Post to `/api/agent/spawn` (or use a channel plugin that exposes agent commands) and Leo clones the repo and starts a new Claude session you can connect to from claude.ai or the Claude app.

### Scheduled Tasks

```
cron scheduler --> leo run <task> --> claude -p "<prompt>" --> Agent
                                                                |
                      Channel plugin <-- MCP tool call <-------+
```

The in-process cron scheduler runs tasks on a schedule. Each task reads a prompt file, assembles arguments, and invokes `claude -p`. The agent delivers its final message via whatever channel plugin(s) are configured, or outputs `NO_REPLY` when there is nothing to report.

---

## Quick Install

=== "Homebrew"

    ```bash
    brew install blackpaw-studio/tap/leo
    ```

=== "Install Script"

    ```bash
    curl -fsSL leo.blackpaw.studio/install | sh
    ```

=== "Go"

    ```bash
    go install github.com/blackpaw-studio/leo/cmd/leo@latest
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
