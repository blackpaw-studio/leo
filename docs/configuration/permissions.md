# Template Permissions

By default every agent Leo spawns gets the full `leo` MCP tool surface: it can
spawn more agents, stop its siblings, toggle scheduled tasks, and message
anyone. A template's `permissions` block narrows that.

```yaml
templates:
  scout:
    workspace: ~/agents/scout
    permissions:
      deny_tools:  [leo_spawn_agent, leo_stop_agent, leo_toggle_task]
      can_message: [rocket, olympus, "scout-*"]
      can_spawn:   [codex]
      can_consult: [fable, opus]
```

`permissions` is optional, and so is every key inside it. A template without
one behaves exactly as it did before this feature existed.

## Both doors: MCP tools and the `leo` CLI

Permissions are applied in two places, from one policy:

- The agent's **`leo` MCP server**, which omits denied tools and checks the
  allowlists before calling the daemon.
- The **`leo` CLI**, which every agent can run from a shell. This matters more
  than it looks: `leo_skill` points agents at the CLI for several of these
  operations, so shelling out is the path a *cooperative* agent takes by
  default, not an evasion route a hostile one has to go looking for. Denying
  `leo_spawn_agent` without gating `leo agent spawn` would not reduce spawning
  at all — it would just move it.

| CLI command | Governed by |
|-------------|-------------|
| `leo agent spawn` | `leo_spawn_agent` + `can_spawn` |
| `leo agent stop` / `suspend` / `reset` / `restart` / `rename` / `prune` | `leo_stop_agent` |
| `leo run <task>` | `leo_run_task` |
| `leo task enable` / `disable` | `leo_toggle_task` |

Lifecycle commands with no exact tool equivalent map to `leo_stop_agent`:
denying "stop other agents" plainly means to deny disrupting them.

## A guardrail, not a security boundary

Both checks read the policy from an environment variable in the agent's own
process, and every agent runs as the same user holding the same daemon token.
An agent that means to get around them can clear the variable, call the
daemon's HTTP API directly, or drive another agent's tmux session.

Use them to shape what an agent reaches for — to stop a reporting template from
spinning up fleets, or keep a noisy worker from paging every other agent. Do
not use them to contain an agent you do not trust.

## Fields

| Field | Meaning |
|-------|---------|
| `deny_tools` | Leo MCP tool names removed from this template's agents. |
| `can_message` | Allowed `to` targets for `leo_send_message`. |
| `can_spawn` | Allowed `template` values for `leo_spawn_agent`. |
| `can_consult` | Allowed `template` values for `leo_consult`. |

A denied tool is never registered, so it does not appear in the agent's tool
list at all. Calling it anyway — a model may remember it from an earlier
session — returns `tool "leo_spawn_agent" is not permitted for this agent`.

The three allowlists are checked before the daemon is called, and are appended
to the tool's description so the model sees the boundary instead of finding it
by failing:

```
not permitted to message "leo"; allowed targets: rocket, olympus, scout-*
```

### Empty means unrestricted

An absent **or empty** allowlist places no restriction. To take a capability
away completely, deny the tool:

```yaml
permissions:
  deny_tools: [leo_send_message]   # cannot message anyone
```

This is deliberate: YAML and JSON both collapse an empty list on a round trip,
so `can_message: []` could not reliably mean "nobody" — it would quietly become
"anybody" the first time the config was saved from the web UI.

### Glob patterns

Allowlist entries match exactly, or as a glob (`*`, `?`, `[...]`), so generated
agent names stay addressable — `scout-*` covers `scout-leo` and
`scout-olympus`. Matching is case-sensitive.

`leo_send_message` accepts shorthand agent names that the daemon resolves, but
the permission check runs on the literal argument. An allowlist of `[rocket]`
rejects `to: "rock"`. Fail-closed, with the allowed targets in the error.

### `leo_skill` cannot be denied

Every agent's system context tells it to call `leo_skill` to operate Leo, so a
template that could deny it would only produce a confusing dead end. Listing it
in `deny_tools` is a config error.

## Validation

`leo` validates the block on every config load and before every web-UI save:

- `deny_tools` entries must be real tool names. A typo would otherwise leave
  the tool available — the exact opposite of what was asked for — so it is an
  error, and the message lists the valid names.
- `can_spawn` and `can_consult` entries that are not globs must name a defined
  template.
- `can_message` is **not** validated. Agent names are generated at spawn time,
  so there is nothing to check them against.

Because those references are checked, a template named in another template's
`can_spawn`/`can_consult` cannot be deleted until the reference is removed —
the same rule a persistent task's `template:` already follows. Renaming is
handled for you: the new name is cascaded into every allowlist that named it.

## Applying a change

Permissions are resolved from config and handed to the agent's MCP server in
its environment at process start. Editing a template's `permissions` therefore
affects newly spawned agents immediately, but an already-running agent keeps
the set it started with until it is restarted:

```bash
leo agent restart scout
```

A restart re-resolves permissions from current config in both directions — a
restriction you added is applied, and one you removed is dropped.

## Scope

Permissions apply to **ephemeral agents spawned from a template**, and to
persistent tasks that target one via `template:`.

They do **not** apply to:

- **Scheduled (`runtime: oneshot`) tasks**, which invoke `claude -p` through
  their own launch path.
- **Consultant subagents** started by `leo_consult`, which likewise build their
  own launch spec. Note that `can_consult` still governs *which* templates an
  agent may consult — it is the consultant's own tool surface that is
  unrestricted.

`leo agent list`, `logs`, and `attach` are read-only and ungated, as are the
config-editing commands (`leo task add`/`remove`) — those have no tool
equivalent and are operator surface, not agent surface.

There is also no inbound policy: `can_message` controls who an agent may
message, not who may message it. Enforcing that would require the daemon to
authenticate the caller, which it cannot do today — every agent carries the
same token.

## Web UI

The four lists are editable on a template's config page, inside the
**Advanced** section — *Deny tools*, *Can message*, *Can spawn*, *Can consult*.
Each is a comma-separated list; clearing a field lifts that restriction.
