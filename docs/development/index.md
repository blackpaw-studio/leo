# Development

## Architecture

Leo is a Go CLI built with [Cobra](https://github.com/spf13/cobra). The entry point is `cmd/leo/main.go`, which calls `cli.Execute()`.

### Package Layout

```
cmd/leo/main.go          -> cli.Execute() entry point
internal/cli/             -> Cobra command definitions (root.go wires all subcommands)
internal/config/          -> Config types + YAML loading/saving (leo.yaml)
internal/run/             -> Task runner: prompt assembly + claude invocation
internal/cron/            -> In-process cron scheduler (robfig/cron wrapper)
internal/service/         -> Background process and daemon management
internal/setup/           -> Interactive setup wizard
internal/onboard/         -> Onboarding flow with prerequisite checks
internal/prompt/          -> Terminal helpers (colored prompts, yes/no, choices)
internal/templates/       -> Embedded template files (user profile, etc.)
internal/prereq/          -> Prerequisite checks (claude CLI, tmux)
internal/session/         -> Session ID persistence
internal/env/             -> Shared environment capture for daemon/cron processes
internal/update/          -> Self-update (binary download from GitHub releases)
```

### Key Design Patterns

**Testability seams**
:   `run.execCommand` and `cron.readCrontab`/`cron.writeCrontab` are package-level function variables that get replaced in tests. This allows testing command execution and crontab operations without side effects.

**Config resolution**
:   `config.FindConfig()` walks up from the current directory to find `leo.yaml`, falling back to `~/.leo/leo.yaml`. Task settings cascade from `defaults` with per-task overrides for model and max turns. Processes follow the same cascade.

**Leo home**
:   Config lives at `~/.leo/leo.yaml`, state at `~/.leo/state/`, default workspace at `~/.leo/workspace/`.

**Multi-process**
:   The `processes` config section defines named long-running Claude sessions. `leo service start` runs all enabled processes under supervision.

**Cron markers**
:   Cron entries are delimited by `# === LEO ===` / `# === END LEO ===` comment blocks to identify Leo-managed entries in the crontab.

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
