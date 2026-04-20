# leo mcp-server

Run the Leo MCP server on stdin/stdout.

!!! note "Internal command"
    You do not invoke this directly. Leo wires it into every supervised Claude process's MCP config automatically. It is documented here so operators know what it does and where to look when debugging.

## Usage

```bash
leo mcp-server
```

## Description

Speaks the [Model Context Protocol](https://modelcontextprotocol.io) over stdin/stdout. Supervised Claude processes dispatch the universal channel slash commands — `/clear`, `/compact`, `/stop`, `/tasks`, `/agent`, `/agents` — by calling the `leo_*` tools this server exposes.

### Environment

`mcp-server` reads two variables injected by the Leo supervisor:

| Variable | Purpose |
|----------|---------|
| `LEO_PROCESS_NAME` | Identifies the calling supervised process so tool actions target the right session. |
| `LEO_WEB_PORT` | Daemon HTTP API port used by `mcp-server` to drive process/agent/task actions. |

## See Also

- [`leo channels register-commands`](channels.md#leo-channels-register-commands) — publish the matching slash commands to channel autocomplete menus
