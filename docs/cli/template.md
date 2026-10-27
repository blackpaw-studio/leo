# leo template

Add, inspect, and remove agent templates. Templates are reusable blueprints for spawning ephemeral coding agents via the HTTP API, a channel plugin that exposes agent commands, or the web UI.

## Usage

```bash
leo template list                       # list configured templates
leo template list --json                # list as JSON
leo template show <name>                # show a template's literal (as-written) config
leo template show <name> --resolved     # show the effective config with defaults cascaded in
leo template show <name> --json         # emit the config as JSON (combine with --resolved)
leo template add [flags]                # add a new template (minimal fields via flags; web UI for the full form)
leo template remove <name>              # remove a template from the config (prompts for confirmation)
leo template override <name>            # follower: copy a lead template into leo.yaml to edit it
```

Template names support shell tab-completion for `show` and `remove`.

## Subcommands

### `leo template list`

Shows a compact table of configured templates:

```
  NAME                 MODEL      AGENT                WORKSPACE
  coding               sonnet     coding-agent         ~/agents
  research             opus       -                    (default)
```

With a `templates_file` or a [`follow`](../configuration/follow-lead.md) block, a SOURCE column shows where each template comes from: `local`, `shared`, `local · overrides shared`, `lead`, `local · overrides lead`, `shared · overrides lead`, or `lead · invalid` for a lead template unusable on this host. `--json` adds `source` (`local`, `shared`, `local-override`, `lead`, `local-override-lead`, `shared-override-lead`) and, for an invalid lead template, `invalid` with the reason.

### `leo template show <name>`

Prints every populated field for the template:

```
Template: coding
  Workspace:             ~/agents
  Model:                 sonnet
  Agent:                 coding-agent
  Permission mode:       auto
  Remote control:        true
  Max turns:             25
  Channels:              plugin:telegram@claude-plugins-official
  Allowed tools:         Bash, Read, Edit, Write
  Append system prompt:  You are a focused coding agent.
```

Empty fields are omitted.

Credential-looking env values (`*TOKEN*`, `*SECRET*`, `*KEY*`, `*PASSWORD*`, `*AUTH*`, `OP_*`, `AWS_*`, …) render as `<redacted>` in both the table and `--json` — agents run this command, and its output ends up in their transcripts. Read `leo.yaml` when you need the actual value.

**Flags:**

| Flag | Description |
|------|-------------|
| `--resolved` | Show the effective config — each unset field is filled in from `defaults`. The raw view (no flag) shows only what is literally written under `templates.<name>`. |
| `--json` | Emit JSON instead of the human-readable table. Combine `--resolved --json` for a fully-resolved structured document suitable for scripting. |

### `leo template add`

Add a template with the minimum set of fields via flags. Templates have many fields (tools, MCP config, permission mode, env, system-prompt appendices, …); use the web UI (**Config → Templates → Add**) or `leo config edit` when you need the full form.

**Flags:**

| Flag | Description |
|------|-------------|
| `--name <name>` | Template name (required). |
| `--workspace <path>` | Base workspace directory (blank = default). |
| `--channels <csv>` | Comma-separated channel plugin IDs. |
| `--model <model>` | Model override (blank = `defaults.model`). |
| `--agent <id>` | Agent identifier (optional). |
| `--permission-mode <mode>` | One of `default`, `acceptEdits`, `auto`, `bypassPermissions`, `dontAsk`, `plan`. |

### `leo template remove <name>`

Removes the named template. Running agents spawned from the template are unaffected — only future spawn requests will fail with "template not found".

Prompts for confirmation by default. Pass `-y / --yes` to skip the prompt (required when stdin is not a TTY).

On a follower, removing (or otherwise editing) a template mirrored from the lead is refused with `owned_by_lead`; the error names the override command.

### `leo template override <name>`

On a [follower](../configuration/follow-lead.md), copies a template mirrored from the lead into this host's `leo.yaml` as a local override, which replaces the lead's template until it is deleted. Edit it afterwards with `leo config edit` or the web UI. A lead template that is invalid on this host cannot be overridden; fix it on the lead. `leo delegation override <profile>` does the same for a delegation profile, and [`leo config override`](config.md#leo-config-override) for any kind.

## See Also

- [Agents guide](../guides/agents.md) — how templates turn into running agents
- [Config Reference](../configuration/config-reference.md#templates) — full template field specification
