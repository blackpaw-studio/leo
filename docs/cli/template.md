# leo template

Inspect and remove agent templates. Templates are reusable blueprints for spawning ephemeral coding agents from Telegram (`/agent <template> <repo>`) or the web UI.

## Usage

```bash
leo template list           # list configured templates
leo template show <name>    # show a template's full configuration
leo template remove <name>  # remove a template from the config
```

Template names support shell tab-completion for `show` and `remove`.

!!! note "Creating templates"
    Template creation has too many fields for a clean CLI prompt. Create templates through the web UI (**Config → Templates → Add**) or by editing `leo.yaml` directly. `leo config edit` opens the file in `$EDITOR`.

## Subcommands

### `leo template list`

Shows a compact table of configured templates:

```
  NAME                 MODEL      AGENT                WORKSPACE
  coding               sonnet     coding-agent         ~/agents
  research             opus       -                    (default)
```

### `leo template show <name>`

Prints every populated field for the template:

```
Template: coding
  Workspace:             ~/agents
  Model:                 sonnet
  Agent:                 coding-agent
  Permission mode:       bypassPermissions
  Remote control:        true
  Max turns:             25
  Channels:              plugin:telegram@claude-plugins-official
  Allowed tools:         Bash, Read, Edit, Write
  Append system prompt:  You are a focused coding agent.
```

Empty fields are omitted.

### `leo template remove <name>`

Removes the named template. Running agents spawned from the template are unaffected — only future spawn requests will fail with "template not found".

## See Also

- [Agents guide](../guides/agents.md) — how templates turn into running agents
- [Config Reference](../configuration/config-reference.md#templates) — full template field specification
