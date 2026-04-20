# leo status

Show overall Leo status.

## Usage

```bash
leo status [--json]
```

## Description

Displays a consolidated summary pulled from the daemon:

- Service (launchd/systemd) state
- Daemon socket health
- Web UI state (if enabled)
- Configured processes and per-process runtime state
- Scheduled tasks and their next run time
- Installed agent templates
- The next upcoming task run across the whole config

## Flags

| Flag | Description |
|------|-------------|
| `--json` | Emit the structured `StatusReport` document for scripting. |

## Examples

```bash
# Human-readable overview
leo status

# Machine-readable report
leo status --json | jq '.processes[] | select(.running == false)'
```

## See Also

- [`leo validate`](validate.md) — deeper health checks (prereqs, workspaces, MCP configs)
- [`leo service status`](service.md) — service-manager state only
