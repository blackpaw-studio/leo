# Development

## Architecture

Leo is a Go CLI built with [Cobra](https://github.com/spf13/cobra). The entry point is `cmd/leo/main.go`, which calls `cli.Execute()`.

### Package Layout

```
cmd/leo/main.go           -> cli.Execute() entry point
internal/agent/           -> Ephemeral agent lifecycle (Manager): template
                             resolution, workspace setup, supervisor +
                             agentstore persistence. Shared by CLI, web,
                             and HTTP callers.
internal/agentstore/      -> On-disk store for ephemeral agent metadata
internal/channels/        -> Channel ID parsing/validation (plugin IDs)
internal/cli/             -> Cobra command definitions (root.go wires all
                             subcommands)
internal/config/          -> Config types + YAML loading/saving (leo.yaml)
internal/cron/            -> In-process cron scheduler (robfig/cron wrapper)
internal/daemon/          -> Daemon IPC server (Unix socket HTTP, optional
                             TCP listener) + client for CLI passthrough
internal/env/             -> Shared environment capture for daemon/cron
                             processes
internal/git/             -> Git helpers used by agent workspace setup
internal/history/         -> Task execution history tracking
internal/leomcp/          -> `leo mcp` server wiring (in-process MCP)
internal/mcp/             -> MCP client used to talk to the daemon
internal/prereq/          -> Prerequisite checks (claude CLI, tmux)
internal/prompt/          -> Interactive terminal helpers (colored prompts,
                             yes/no, choices)
internal/run/             -> Task runner: prompt assembly + claude invocation
internal/service/         -> Process supervisor (multi-process tmux
                             management, launchd/systemd integration)
internal/session/         -> Session ID persistence (JSON key-value store)
internal/setup/           -> Setup wizard
internal/templates/       -> embed.FS templates for user profile, CLAUDE.md,
                             skills/
internal/tmux/            -> tmux helpers (dedicated `-L leo` socket,
                             attach/popup/control-mode, session picker)
internal/update/          -> Self-update (binary download + cosign
                             verification)
internal/web/             -> Web UI (htmx + Go html/template, embedded via
                             embed.FS)
```

### Key Design Patterns

**Multi-process supervisor**
:   `service.RunSupervised()` spawns a goroutine per enabled process, each managing its own tmux session (`leo-<name>`) with restart loop and backoff.

**Dual listener daemon**
:   The daemon serves a Unix socket for CLI IPC and an optional TCP listener for the web UI from the same process. Bearer-token auth and allowed-hosts pinning gate non-loopback binds.

**Testability seams**
:   Package-level function variables (`run.execCommand`, `service.supervisedExecFn`, and similar) are replaced in tests so command execution and side effects can be stubbed.

**Config resolution**
:   `config.FindConfig()` walks up from the current directory to find `leo.yaml`, falling back to `~/.leo/leo.yaml`. Settings cascade from `defaults` to per-process and per-task overrides.

**Leo home**
:   Config lives at `~/.leo/leo.yaml`, state at `~/.leo/state/`, default workspace at `~/.leo/workspace/`.

**In-process scheduling**
:   Cron-scheduled tasks run inside the Leo daemon via `robfig/cron/v3`. Leo no longer edits the system crontab.

**Embedded templates**
:   Workspace templates are embedded via `//go:embed *.md` in `internal/templates/` and rendered with `text/template`.

**Prompt assembly**
:   `leo run` builds the final prompt by concatenating an optional silent preamble with the prompt file content. The agent handles outbound messaging via whatever channel plugin(s) are configured (surfaced via the `LEO_CHANNELS` env var).

### Dependencies

| Package | Purpose |
|---------|---------|
| `github.com/spf13/cobra` | CLI command framework |
| `gopkg.in/yaml.v3` | YAML config parsing |
| `github.com/fatih/color` | Colored terminal output |

**Runtime dependencies:** `claude` CLI (authenticated), `tmux` (for supervised mode). Channel plugins (installed via `claude plugin install <id>`) bring their own runtime requirements.

### Version Injection

The version is injected at build time via Go linker flags:

```
-X github.com/blackpaw-studio/leo/internal/cli.Version=$(VERSION)
```

`VERSION` defaults to the output of `git describe --tags --always --dirty`.
