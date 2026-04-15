# Config Reference

Leo's config lives at `~/.leo/leo.yaml`.

## Full Structure

```yaml
defaults:
  model: <string>             # Default model: sonnet, opus, or haiku (required)
  max_turns: <int>            # Default max conversation turns (required)
  bypass_permissions: <bool>  # Skip permission prompts (optional)
  remote_control: <bool>      # Enable Remote Control for web/mobile access (optional)

processes:
  <process-name>:
    workspace: <path>           # Workspace directory (optional — defaults to ~/.leo/workspace)
    channels: [<string>]        # Channel plugin IDs, e.g. plugin:telegram@claude-plugins-official
    model: <string>             # Override defaults.model (optional)
    max_turns: <int>            # Override defaults.max_turns (optional)
    bypass_permissions: <bool>  # Override defaults.bypass_permissions (optional — pointer, unset != false)
    remote_control: <bool>      # Override defaults.remote_control (optional — pointer, unset != false)
    mcp_config: <path>          # MCP server config path — relative to workspace or absolute (optional)
    add_dirs: [<path>]          # Additional directories passed via --add-dir (optional)
    enabled: <bool>             # Whether this process is active (default: true)

tasks:
  <task-name>:
    workspace: <path>         # Workspace directory (optional — defaults to ~/.leo/workspace)
    schedule: <cron-expr>     # 5-field cron expression (required)
    timezone: <string>        # IANA timezone, e.g. America/New_York (optional)
    prompt_file: <path>       # Path relative to workspace (required)
    model: <string>           # Override defaults.model (optional)
    max_turns: <int>          # Override defaults.max_turns (optional)
    channels: [<string>]      # Channel plugin IDs used for notify_on_fail (optional)
    notify_on_fail: <bool>    # Spawn a child claude to notify configured channels on failure (optional)
    enabled: <bool>           # Whether cron runs this task (default: true)
    silent: <bool>            # Suppress narration, output NO_REPLY if nothing to report (optional)
```

## Channels

Channels are Claude Code plugin IDs (not bot tokens or chat IDs). Install the plugin via `claude plugin install <id>`, configure it with the plugin's own setup flow, then reference the plugin ID in the `channels:` list on a process or task.

Example:

```bash
claude plugin install telegram@claude-plugins-official
```

Then in `leo.yaml`:

```yaml
processes:
  assistant:
    channels:
      - plugin:telegram@claude-plugins-official
```

Leo passes the resolved list to the spawned Claude process via the `LEO_CHANNELS` environment variable. The plugin owns its own credentials and routing.

## Override Cascade

Process and task settings inherit from defaults and can be overridden individually:

```
effective model     = process.model     OR task.model     OR defaults.model
effective max_turns = process.max_turns OR task.max_turns OR defaults.max_turns
```

For `bypass_permissions` and `remote_control` in processes, a pointer type is used so that an unset value correctly falls through to defaults (rather than being treated as `false`).

## Valid Models

- `sonnet` — Best for general coding and development tasks
- `opus` — Deepest reasoning, best for complex analysis
- `haiku` — Fastest and cheapest, good for simple checks

## Processes vs Tasks

**Processes** are long-running interactive sessions. They subscribe to channel plugins and run via `leo service`.

**Tasks** are one-shot scheduled invocations triggered by cron. They run via `leo run <task>` with an assembled prompt.

## Paths

- Paths in `workspace` fields support `~` expansion
- `prompt_file` is relative to the workspace directory
- `mcp_config` is relative to the workspace directory or an absolute path
- Config location is `~/.leo/leo.yaml`, or specify with `--config`

## Validation

```bash
leo validate
```

Checks: required fields, model names, cron syntax, channel ID shape, file existence.
