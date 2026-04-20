# leo config

Inspect and edit the Leo configuration. Subcommands:

- [`leo config show`](#leo-config-show)
- [`leo config edit`](#leo-config-edit)
- [`leo config path`](#leo-config-path)

## leo config show

Display the effective config with defaults applied.

### Usage

```bash
leo config show [--raw | --json]
```

### Flags

| Flag | Description |
|------|-------------|
| `--raw` | Print the YAML file verbatim without resolving defaults. Mutually exclusive with `--json`. |
| `--json` | Emit the resolved config as indented JSON (useful for piping to `jq`). |

### Examples

```bash
# Effective YAML (defaults applied)
leo config show

# Raw file as written on disk
leo config show --raw

# Query a single task's model
leo config show --json | jq '.tasks.heartbeat.model'
```

## leo config edit

Open `leo.yaml` in `$EDITOR` (falling back to `vi`). After the editor exits, Leo re-loads and re-validates the file; an invalid save surfaces a non-zero exit and the error, leaving the file on disk.

### Usage

```bash
leo config edit
```

## leo config path

Print the absolute path to the `leo.yaml` that will be used, honoring `--config` and the normal resolution rules ([config auto-detection](index.md#config-auto-detection)).

### Usage

```bash
leo config path
```

### Example

```bash
cat "$(leo config path)"
```

## See Also

- [Configuration reference](../configuration/config-reference.md)
- [`leo validate`](validate.md) — check the config against prerequisites
