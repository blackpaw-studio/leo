# leo config

Inspect and edit the Leo configuration. Subcommands:

- [`leo config show`](#leo-config-show)
- [`leo config edit`](#leo-config-edit)
- [`leo config path`](#leo-config-path)
- [`leo config override`](#leo-config-override)
- [`leo config export --follow`](#leo-config-export-follow)

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

## leo config override

On a [follower](../configuration/follow-lead.md), copy an entry mirrored from the lead into this host's `leo.yaml` as a local override, so it can be edited there. The local entry replaces the lead's until it is deleted.

### Usage

```bash
leo config override {template|environment|profile} <name>
```

`leo template override <name>` and `leo delegation override <profile>` are shorthands. Overriding an entry that is not from the lead, is already overridden, or is invalid on this host is an error.

## leo config export --follow

Print this host's shared config (effective `templates`, `environments`, `delegation`, `defaults`, plus its config revision and Leo version) as one JSON document. A follower's daemon runs it on the lead over ssh; it always describes the machine it runs on and ignores `client.default_host` and `LEO_HOST`. It refuses with `lead_is_follower` on a host that follows another, and with `lead_config_invalid` when the config does not load.

### Usage

```bash
leo config export --follow
```

## See Also

- [Configuration reference](../configuration/config-reference.md)
- [`leo validate`](validate.md) — check the config against prerequisites
- [`leo sync`](sync.md) — pull a follower's lead config now
