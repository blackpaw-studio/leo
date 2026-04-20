# leo session

Manage stored Claude session IDs used for conversation persistence. Subcommands:

- [`leo session list`](#leo-session-list)
- [`leo session clear`](#leo-session-clear)

Leo records a session ID per task and supervised process so subsequent runs resume the same Claude conversation. Clearing a session forces the next run to start a fresh conversation.

Session keys look like `task:<name>` (scheduled tasks) and `service:<name>` (supervised processes).

## leo session list

List every stored session mapping. Each entry pairs a session key with a Claude session UUID and the timestamp of the last update.

### Usage

```bash
leo session list [--json]
```

### Flags

| Flag | Description |
|------|-------------|
| `--json` | Emit the list as a machine-readable JSON array. |

### Examples

```bash
leo session list
leo session list --json
```

## leo session clear

Clear a specific session by key, or wipe every stored session with `--all`. By default the command prompts for confirmation when a TTY is attached; pass `--yes` to skip the prompt (required for non-interactive use).

### Usage

```bash
leo session clear <key> [--yes]
leo session clear --all [--yes]
```

### Flags

| Flag | Description |
|------|-------------|
| `--all` | Clear every stored session. |
| `-y, --yes` | Skip the confirmation prompt. |

### Examples

```bash
leo session clear task:heartbeat
leo session clear task:heartbeat --yes
leo session clear --all
leo session clear --all --yes
```
