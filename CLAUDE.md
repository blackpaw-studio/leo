# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What is Leo

Leo is a Go CLI that sets up and manages a persistent, mobile-accessible Claude Code personal assistant. It handles workspace scaffolding, Telegram integration, and cron scheduling. Memory is not built-in — users configure their preferred memory MCP server in the standard `~/.claude/mcp-servers.json` or the workspace-specific `config/mcp-servers.json`. After setup, system cron runs `claude` directly — Leo manages the config and cron entries, not a daemon. Leo is not a multi-agent orchestration framework.

Two runtime modes (both invoke stock `claude` CLI):
- **Interactive** (`leo service`): long-running Telegram session via channel plugin
- **Scheduled** (`leo run <task>`): cron invokes claude with an assembled prompt; outbound Telegram uses curl (injected into prompt at runtime)

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
internal/run/             → Task runner: prompt assembly (silent preamble + prompt file + telegram protocol) + claude invocation
internal/cron/            → Crontab management with marker-delimited blocks per agent
internal/telegram/        → Telegram Bot API helpers (test message, getUpdates polling)
internal/prompt/          → Interactive terminal helpers (colored prompts, yes/no, choice parsing)
internal/templates/       → embed.FS templates for heartbeat, user profile, CLAUDE.md, skills/
internal/setup/           → Setup wizard (wizard.go, telegram.go, daemon.go)
internal/migrate/         → OpenClaw migration
internal/onboard/         → Onboarding flow
internal/prereq/          → Prerequisite checks (claude CLI, etc.)
internal/update/          → Self-update (binary download from GitHub releases + workspace file refresh)
internal/env/             → Shared environment capture for daemon/cron processes
```

Key design patterns:
- **Testability seams**: `run.execCommand` and `cron.readCrontab`/`cron.writeCrontab` are package-level vars replaced in tests
- **Config resolution**: `FindConfig()` walks up from cwd; task settings cascade from `defaults` with per-task overrides for model and max_turns
- **Cron markers**: blocks delimited by `# === LEO ===` / `# === END LEO ===` identify Leo-managed entries in the crontab
- **Templates**: embedded via `//go:embed *.md` in `internal/templates/`, rendered with `text/template`

## Config

Workspace config lives at `<workspace>/leo.yaml`. Key sections: `agent` (workspace), `telegram` (bot_token, chat_id, group_id, topics), `defaults` (model, max_turns, remote_control), `tasks` (per-task schedule, prompt_file, overrides).

`Config.Validate()` checks required fields, model names (sonnet/opus/haiku), cron schedule syntax, telegram consistency, and topic references. Called automatically by CLI on config load.

## Dependencies

- cobra for CLI subcommands
- gopkg.in/yaml.v3 for config
- fatih/color for terminal output
- Runtime: `claude` CLI (authenticated), `curl` (for Telegram in agent prompts)
