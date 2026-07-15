# Repository agent guidance

This file provides shared guidance to coding agents working in this repository. `AGENTS.md` is a symlink to this file so Claude Code and Codex use one source of truth.

## Shared project memory

Claude Code's existing project memory is the canonical memory store for this repository:

```text
$HOME/.claude/projects/-Users-evan--leo-agents-leo/memory/
```

At the start of a task, read `MEMORY.md` there and follow links relevant to the task. When durable project knowledge is learned, update that same memory store rather than creating a Codex-specific copy. Keep `MEMORY.md` as a concise index and put detailed notes in topic files alongside it. Never store credentials or secret values in memory.

## What is Leo

Leo is a Go CLI that supervises Claude Code agents and schedules tasks. `leo service` runs the daemon — web UI, cron, agent supervision, and the IPC socket — which keeps ephemeral agents alive, each in its own tmux session. Channels (Telegram, Slack, webhook, etc.) are Claude Code plugins the user installs separately; Leo only knows them as opaque plugin IDs.

Two core primitives:
- **Agents** (`leo agent`): spawn/list/attach/stop/suspend/resume/reset/logs for on-demand agents created from templates, supervised by the daemon with restart-on-crash. Dual-purpose — runs locally against the daemon, or acts as a thin SSH client against a remote leo host when `client.hosts` is configured. Agents can idle-suspend after inactivity and auto-resume on the next message.
- **Tasks** (`leo run <task>`, cron-scheduled): default `runtime: oneshot` invokes a fresh `claude -p` per firing; the agent handles outbound messaging via whatever channel plugin(s) are configured. `runtime: persistent` instead injects the prompt into a supervised agent — `template: <name>` targets that template's agent explicitly (shareable across tasks), or an implicit agent named after the task is spawned/resumed on demand ("ensure-exists") when no `template:` is set. See `docs/configuration/persistent-tasks.md`.

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
internal/harness/         → Coding-agent adapter registry + claude, codex, opencode adapters. All three run every leo primitive (scheduled tasks, ephemeral agents, persistent tasks) — see docs/configuration/harnesses.md
internal/agent/           → Ephemeral agent lifecycle (Manager): template resolution, workspace setup, supervisor + agentstore persistence. Shared by CLI, web, and HTTP callers.
internal/web/             → Web UI (htmx + Go html/template, embedded via embed.FS)
internal/service/         → Daemon lifecycle + agent supervisor (tmux management, launchd/systemd)
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
- **Agent supervisor**: `RunSupervised()` starts the daemon, then restores and supervises every ephemeral agent — including the ones backing `runtime: persistent` tasks — each in its own tmux session (`leo-<name>`) with restart loop and backoff
- **Dual listener daemon**: Unix socket for CLI IPC, optional TCP listener for web UI. Both served from the same daemon process.
- **Web UI**: htmx + Go `html/template`, embedded via `embed.FS`. Dark terminal theme (JetBrains Mono) with a sidebar nav — each section (Tasks, Agents, Defaults, Templates, Settings, Service) is its own routed page, not a tab within one dashboard — and config editing goes through a schema-driven form component (`internal/web/schema`) instead of hand-rolled per-field markup.
- **Testability seams**: `run.execCommand`, `service.supervisedExecFn` etc. are package-level vars replaced in tests
- **Config resolution**: `FindConfig()` walks up from cwd, falls back to `~/.leo/leo.yaml`; settings cascade from `defaults` to per-task/template overrides
- **Templates**: embedded via `//go:embed *.md` in `internal/templates/`, rendered with `text/template`

## Config

Config lives at `~/.leo/leo.yaml` (the "leo home"). Key sections:

- `defaults` (model, harness, harness_options, max_turns, idle_suspend_after)
- `web` (enabled, port, bind — web UI configuration)
- `client` (default_host, hosts — remote-host definitions for `leo agent` CLI dispatch; empty on servers)
- `templates` (map of agent template configs — blueprints for ephemeral agents, plus `idle_suspend_after` for auto-suspending idle agents; also the target of `runtime: persistent` tasks via `tasks.*.template`)
- `tasks` (map of named task configs — schedule, prompt_file, model, harness, harness_options, timeout, retries, channels, notify_on_fail, runtime, template, queue_max, etc.)

Channels are strings like `plugin:telegram@claude-plugins-official`. Leo passes the resolved list to the spawned Claude process via the `LEO_CHANNELS` environment variable; the plugin owns its own credentials and routing. `channels`/`dev_channels` are only valid on a channel-supporting harness — `claude` is the only one today (codex/opencode have no channel-plugin concept; they message via leo's `leo_send_message` MCP tool).

`harness` (on `defaults`, `templates.*`, `tasks.*`) selects the coding-agent adapter — `claude`, `codex`, or `opencode` — and cascades from `defaults` down to the built-in default `claude`. All three harnesses support every leo primitive (scheduled tasks, ephemeral agents, persistent tasks). The claude-specific knobs that used to be flat fields (`permission_mode`, `bypass_permissions`, `remote_control`, `agent`, `allowed_tools`, `disallowed_tools`, `append_system_prompt`) now live under that scope's `harness_options`, strictly validated by the adapter. There is no more `providers`/`provider` section — point a scope at a third-party Anthropic-compatible endpoint via its own `env:` map (`ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN`) instead. See `docs/configuration/harnesses.md`.

Each task and template can specify its own `workspace`. Default workspace is `~/.leo/workspace/`.

State (session ids, logs, daemon socket) lives in `~/.leo/state/`.

`Config.Validate()` checks model names (delegated to the resolved harness — claude: sonnet/opus/haiku/sonnet[1m]/opus[1m]; codex: format check only, validated server-side; opencode: must be `provider/model`), cron schedule syntax, channel ID shape, web port range, and harness_options via the adapter's `DecodeOptions`. Called automatically by CLI on config load and by web UI before every save.

## Dependencies

- cobra for CLI subcommands
- gopkg.in/yaml.v3 for config
- fatih/color for terminal output
- robfig/cron/v3 for in-process task scheduling
- Runtime: `claude` CLI (authenticated), `tmux` (for supervised mode). Channel plugins (installed via `claude plugin install <id>`) handle their own runtime requirements.
