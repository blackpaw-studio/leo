# Configuration

Leo is configured via a single `leo.yaml` file in the Leo home directory (`~/.leo/`).

## Location

The config file lives at `~/.leo/leo.yaml`. Leo auto-detects it by walking up from the current working directory, falling back to `~/.leo/leo.yaml`. You can also specify it explicitly:

```bash
leo --config /path/to/leo.yaml <command>
```

## Example Configuration

```yaml
defaults:
  model: sonnet
  max_turns: 15
  harness_options:
    remote_control: true

templates:
  assistant:
    workspace: ~/agents/assistant
    channels:
      - plugin:telegram@claude-plugins-official

tasks:
  daily-news-briefing:
    schedule: "0 7 * * *"
    timezone: America/New_York
    prompt_file: reports/daily-news-briefing.md
    model: opus
    max_turns: 20
    channels:
      - plugin:telegram@claude-plugins-official
    notify_on_fail: true
    enabled: true
    silent: true
```

## Sections Overview

### `defaults`

Default model, max turns, harness, and other settings applied to all agents and tasks unless overridden.

### `harness` / `harness_options`

Every scope picks a coding-agent adapter (`harness:`, `claude` today) and
configures it through a strictly validated `harness_options:` map. See
[Harnesses](harnesses.md) for the full config shape, cascade rules, and the
`claude` option reference.

### `templates`

Named blueprints for on-demand ephemeral agents, spawned via `leo agent spawn` or the web UI. Each template specifies its own workspace, channels, model, and settings. Templates also back **persistent tasks** (`runtime: persistent`) — a task can target a template's agent instead of spawning `claude -p` per firing. A template can also narrow the leo MCP tools its agents get, and which agents they may message, spawn, or consult — see [Permissions](permissions.md). See the [Agent guide](../guides/agents.md) and [Persistent Tasks](persistent-tasks.md).

### `tasks`

Named tasks with cron schedules, prompt files, and optional overrides. Each task can override the default model and max turns, specify its own channels for `notify_on_fail`, use its own workspace, and optionally run `runtime: persistent` to deliver into a supervised agent instead of a fresh process. See [Persistent Tasks](persistent-tasks.md).

### `api_clients`

Scoped bearer tokens for agents Leo does **not** supervise — an opencode
container, a CI job — that need to message one Leo agent. Default-deny: one
route, the targets you name, nothing else, with the sender identity enforced by
the daemon rather than asserted by the caller. Distinct from `client:`, which
points this machine's CLI at a remote leo host. See [API Clients](api-clients.md).

### Channels

Channels are Claude Code plugin IDs (e.g., `plugin:telegram@claude-plugins-official`). Install the plugin via `claude plugin install <id>` and reference it in a template or task `channels:` list. Leo passes the list to the spawned Claude process via `LEO_CHANNELS`; the plugin owns its own credentials and routing.

For plugins not yet published to a registry, use `dev_channels:` instead. Leo passes them via `--dangerously-load-development-channels` and auto-accepts the in-terminal confirmation prompt for supervised agents. See the [Config Reference](config-reference.md#development-channels) for details.

---

See [Config Reference](config-reference.md) for the full field-by-field specification.
