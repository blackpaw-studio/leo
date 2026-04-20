# leo validate

Check config, prerequisites, and workspace health.

## Usage

```bash
leo validate [--json]
```

## Description

Runs a battery of diagnostic checks and prints findings ranked by severity (`ERROR`, `WARN`, `INFO`). The same critical warnings are surfaced automatically whenever the service starts.

Checks include:

- Config loads and passes schema validation
- `claude` CLI is installed and its version is reported
- `tmux` is installed (required for background service)
- Default workspace exists
- Each process's workspace exists
- Prompt files referenced by enabled tasks resolve on disk
- MCP configs referenced by processes are valid JSON
- Web UI bind exposure — warns when `bind` is non-loopback
- Daemon socket presence and health
- Service-manager (launchd/systemd) status
- Service log size — warns above 50 MB

## Exit Code

Non-zero if any finding has severity `ERROR`.

## Flags

| Flag | Description |
|------|-------------|
| `--json` | Emit findings as a structured JSON document for CI checks and scripting. |

## Examples

```bash
# Text output with tally
leo validate

# Use in CI — fails the job on any ERROR finding
leo validate --json | jq -e '.errors == 0'
```

## See Also

- [`leo status`](status.md) — runtime overview
- [`leo config show`](config.md#leo-config-show) — inspect the resolved config
