# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What is Leo

Leo is a Go CLI that supervises persistent Claude Code processes and schedules tasks. It manages multiple long-running Claude sessions (each in its own tmux session) and cron-based task scheduling. Channels (Telegram, Slack, webhook, etc.) are Claude Code plugins the user installs separately; Leo only knows them as opaque plugin IDs.

Three core primitives:
- **Process Supervisor** (`leo service`): manages N long-running Claude processes defined in config, each with its own workspace, channel plugin list, and restart logic
- **Task Scheduler** (`leo run <task>`): cron invokes claude with an assembled prompt; the agent handles outbound messaging via whatever channel plugin(s) are configured
- **Ephemeral Agents** (`leo agent`): spawn/list/attach/stop/logs for on-demand agents created from templates. Dual-purpose — runs locally against the daemon, or acts as a thin SSH client against a remote leo host when `client.hosts` is configured.

**Persistent Task Sessions** (`runtime: persistent` on a task): instead of spawning a fresh `claude -p` per firing, the task's prompt is injected into a long-running supervised claude inside `leo-session-<name>` tmux. Channel plugins load at session boot; delivery happens in-session. See `docs/configuration/persistent-tasks.md`.

## Build & Test Commands

```bash
make build          # Build binary to bin/leo
make install        # go install
make test           # go test -race -cover ./...
make lint           # go vet + staticcheck
make snapshot       # goreleaser snapshot

# Run a single test
go test -race -run TestFunctionName ./internal/config/

# Coverage report
go test -race -coverprofile=coverage.out ./... && go tool cover -html=coverage.out
```

Version is injected via ldflags: `-X github.com/blackpaw-studio/leo/internal/cli.Version=$(VERSION)`

## Architecture

```
cmd/leo/main.go          → cli.Execute() entry point
internal/cli/             → Cobra command definitions (root.go wires all subcommands)
internal/config/          → Config types + YAML loading/saving (leo.yaml)
internal/daemon/          → Daemon IPC server (Unix socket HTTP) + client for CLI passthrough
internal/agent/           → Ephemeral agent lifecycle (Manager): template resolution, workspace setup, supervisor + agentstore persistence. Shared by CLI, web, and HTTP callers.
internal/web/             → Web UI (htmx + Go html/template, embedded via embed.FS)
internal/service/         → Process supervisor (multi-process tmux management, launchd/systemd)
internal/run/             → Task runner: prompt assembly + claude invocation
internal/cron/            → In-process cron scheduler (robfig/cron wrapper)
internal/prompt/          → Interactive terminal helpers (colored prompts, yes/no)
internal/templates/       → embed.FS templates for user profile, CLAUDE.md, skills/
internal/setup/           → Setup wizard
internal/onboard/         → Onboarding flow (prereq checks → setup)
internal/prereq/          → Prerequisite checks (claude CLI, tmux)
internal/session/         → Session ID persistence (JSON key-value store)
internal/history/         → Task execution history tracking
internal/update/          → Self-update (binary download from GitHub releases)
internal/env/             → Shared environment capture for daemon/cron processes
```

Key design patterns:
- **Multi-process supervisor**: `RunSupervised()` spawns a goroutine per enabled process, each managing its own tmux session (`leo-<name>`) with restart loop and backoff
- **Dual listener daemon**: Unix socket for CLI IPC, optional TCP listener for web UI. Both served from the same daemon process.
- **Web UI**: htmx + Go `html/template`, embedded via `embed.FS`. Dark terminal theme (JetBrains Mono) with a sidebar nav — each section (Tasks, Agents, Processes, Sessions, Defaults, Templates, Settings, Service) is its own routed page, not a tab within one dashboard — and config editing goes through a schema-driven form component (`internal/web/schema`) instead of hand-rolled per-field markup.
- **Testability seams**: `run.execCommand`, `service.supervisedExecFn` etc. are package-level vars replaced in tests
- **Config resolution**: `FindConfig()` walks up from cwd, falls back to `~/.leo/leo.yaml`; settings cascade from `defaults` to per-process/task overrides
- **Templates**: embedded via `//go:embed *.md` in `internal/templates/`, rendered with `text/template`

## Config

Config lives at `~/.leo/leo.yaml` (the "leo home"). Key sections:

- `defaults` (model, harness, harness_options, max_turns, idle_suspend_after)
- `web` (enabled, port, bind — web UI configuration)
- `client` (default_host, hosts — remote-host definitions for `leo agent` CLI dispatch; empty on servers)
- `processes` (map of named process configs — workspace, channels, model, harness, harness_options, env, etc.)
- `templates` (map of agent template configs — blueprints for ephemeral agents; same fields as processes, plus `idle_suspend_after` for auto-suspending idle agents)
- `tasks` (map of named task configs — schedule, prompt_file, model, harness, harness_options, timeout, retries, channels, notify_on_fail, etc.)

Channels are strings like `plugin:telegram@claude-plugins-official`. Leo passes the resolved list to the spawned Claude process via the `LEO_CHANNELS` environment variable; the plugin owns its own credentials and routing. `channels`/`dev_channels` are only valid on a channel-supporting harness — `claude` is the only one today.

`harness` (on `defaults`, `processes.*`, `templates.*`, `sessions.*`, `tasks.*`) selects the coding-agent adapter and cascades from `defaults` down to the built-in default `claude`. The claude-specific knobs that used to be flat fields (`permission_mode`, `bypass_permissions`, `remote_control`, `agent`, `allowed_tools`, `disallowed_tools`, `append_system_prompt`) now live under that scope's `harness_options`, strictly validated by the adapter. There is no more `providers`/`provider` section — point a scope at a third-party Anthropic-compatible endpoint via its own `env:` map (`ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN`) instead. See `docs/configuration/harnesses.md`.

Each process and task can specify its own `workspace`. Default workspace is `~/.leo/workspace/`.

State (sessions, logs, daemon socket) lives in `~/.leo/state/`.

`Config.Validate()` checks model names (delegated to the resolved harness — for claude: sonnet/opus/haiku/sonnet[1m]/opus[1m]), cron schedule syntax, channel ID shape, web port range, and harness_options via the adapter's `DecodeOptions`. Called automatically by CLI on config load and by web UI before every save.

## Dependencies

- cobra for CLI subcommands
- gopkg.in/yaml.v3 for config
- fatih/color for terminal output
- robfig/cron/v3 for in-process task scheduling
- Runtime: `claude` CLI (authenticated), `tmux` (for supervised mode). Channel plugins (installed via `claude plugin install <id>`) handle their own runtime requirements.
