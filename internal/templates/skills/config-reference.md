# Config Reference

Leo's config lives at `~/.leo/leo.yaml`.

## Full Structure

```yaml
defaults:
  model: <string>               # Default model: sonnet, opus, or haiku (required)
  max_turns: <int>              # Default max conversation turns (required)
  harness: <string>             # Coding-agent adapter (optional — defaults to "claude"; only "claude" is registered today)
  harness_options:               # Adapter-specific options, strictly validated (optional)
    permission_mode: <string>    # acceptEdits, auto, bypassPermissions, default, dontAsk, or plan
    bypass_permissions: <bool>   # Legacy fallback — only consulted when permission_mode is empty
    remote_control: <bool>       # Enable Remote Control for web/mobile access
    agent: <string>
    allowed_tools: [<string>]
    disallowed_tools: [<string>]
    append_system_prompt: <string>

processes:
  <process-name>:
    workspace: <path>           # Workspace directory (optional — defaults to ~/.leo/workspace)
    channels: [<string>]        # Channel plugin IDs, e.g. plugin:telegram@claude-plugins-official
    model: <string>             # Override defaults.model (optional)
    max_turns: <int>            # Override defaults.max_turns (optional)
    harness: <string>           # Override defaults.harness (optional)
    harness_options: {}         # Same keys as defaults.harness_options, merged over defaults (scope key wins)
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
    harness: <string>         # Override defaults.harness (optional)
    harness_options: {}       # Same keys as defaults.harness_options, merged over defaults (scope key wins)
    channels: [<string>]      # Channel plugin IDs used for notify_on_fail (optional)
    notify_on_fail: <bool>    # Spawn a child claude to notify configured channels on failure (optional)
    enabled: <bool>           # Whether cron runs this task (default: true)
    silent: <bool>            # Suppress narration, output NO_REPLY if nothing to report (optional)
```

`templates.*` (ephemeral agent blueprints) and `sessions.*` (persistent task sessions) also support `harness` and `harness_options`. Sessions do NOT inherit `defaults.harness_options` — only processes, templates, and tasks merge defaults in (same-harness scopes only; scope key wins on conflict).

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

`harness_options` merges shallowly: `defaults.harness_options` is layered under a scope's own `harness_options` (only when the scope uses the same harness as defaults), and keys set on the scope win over defaults. Unknown keys are rejected by the adapter — see `harness_options.` fields above for the claude harness.

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
