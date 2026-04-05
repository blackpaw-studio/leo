# Development

## Architecture

Leo is a Go CLI built with [Cobra](https://github.com/spf13/cobra). The entry point is `cmd/leo/main.go`, which calls `cli.Execute()`.

### Package Layout

```
cmd/leo/main.go          -> cli.Execute() entry point
internal/cli/             -> Cobra command definitions (root.go wires all subcommands)
internal/config/          -> Config types + YAML loading/saving (leo.yaml)
internal/run/             -> Task runner: prompt assembly + claude invocation
internal/cron/            -> Crontab management with marker-delimited blocks
internal/telegram/        -> Telegram Bot API helpers (SendMessage, PollChatID)
internal/service/         -> Background process and daemon management
internal/setup/           -> Interactive setup wizard
internal/onboard/         -> Onboarding flow with prerequisite checks
internal/migrate/         -> OpenClaw migration
internal/prompt/          -> Terminal helpers (colored prompts, yes/no, choices)
internal/templates/       -> Embedded template files (agent personas, heartbeat, user profile)
internal/prereq/          -> Prerequisite checks (claude CLI, curl)
```

### Key Design Patterns

**Testability seams**
:   `run.execCommand` and `cron.readCrontab`/`cron.writeCrontab` are package-level function variables that get replaced in tests. This allows testing command execution and crontab operations without side effects.

**Config resolution**
:   `config.FindConfig()` walks up from the current directory to find `leo.yaml`. Task settings cascade from `defaults` with per-task overrides for model and max turns.

**Cron markers**
:   Cron entries are delimited by `# === LEO:<agent> ===` / `# === END LEO:<agent> ===` comment blocks. This allows multiple agents to coexist in a single crontab.

**Embedded templates**
:   Agent personality templates, heartbeat checklist, and user profile are embedded via `//go:embed *.md` in `internal/templates/` and rendered with `text/template`.

**Prompt assembly**
:   `leo run` builds the final prompt by concatenating an optional silent preamble, the prompt file content, and the Telegram notification protocol. The Telegram `curl` template is injected at runtime (not stored in the agent file).

### Dependencies

| Package | Purpose |
|---------|---------|
| `github.com/spf13/cobra` | CLI command framework |
| `gopkg.in/yaml.v3` | YAML config parsing |
| `github.com/fatih/color` | Colored terminal output |

**Runtime dependencies:** `claude` CLI (authenticated), `curl` (for Telegram in agent prompts).

### Version Injection

The version is injected at build time via Go linker flags:

```
-X github.com/blackpaw-studio/leo/internal/cli.Version=$(VERSION)
```

`VERSION` defaults to the output of `git describe --tags --always --dirty`.
