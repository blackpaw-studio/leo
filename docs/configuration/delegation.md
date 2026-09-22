# Delegation profiles

Delegation profiles give stable names to kinds of work while letting the
operator switch the template, model, and reasoning effort used for that work.
Dispatch a role, rather than selecting a template directly, when the routing
policy belongs to the operator.

```yaml
delegation:
  roles:
    implement:
      use_for: Implementing and testing a bounded change
    review.security:
      use_for: Security-focused review
  active_profile: fast
  profiles:
    fast:
      description: Lower-cost routine work
      roles:
        implement: codex-worker          # string target
        review.security:                 # object target
          template: claude-reviewer
          model: sonnet
          effort: high
    thorough:
      roles:
        implement:
          template: claude-worker
          model: opus
          effort: max
        review.security: claude-reviewer
```

`roles` declares the stable role names. `use_for` is optional operator-facing
guidance injected into managed agents; it does not choose a template. A target
can be a string (`template: <that string>`) or an object with `template` and
optional `model` and `effort`. Object targets accept only those three keys.

`active_profile` names the profile used by every role dispatch. Profiles may
have different mappings. A declared role must be mapped in the active profile;
it may be absent from an inactive profile while that profile is being edited.
If `roles` is omitted, the active profile's roles are canonical instead.

## Resolution and overrides

Role resolution is exact: `review.security` does **not** fall back to
`review`. An unmapped role fails with:

```text
role "review.security" is not mapped in active delegation profile "fast" (mapped: implement)
```

The selected target supplies the template, model, and effort. Explicit
dispatch `model` and `effort` values take precedence over the selected
profile's values; an explicit template is mutually exclusive with `role`.

## Validation and warnings

`leo validate` checks that `active_profile` is present and exists, that there
is at least one profile, and that role/profile names contain only letters,
digits, dot, underscore, or dash. It also checks every mapped target has a
template, that the template exists, and validates target models and effort
against that template's resolved harness. With a declared `roles` map, every
declared role must be mapped in the active profile.

The validator reports `delegation.profiles.<profile>.roles.<role>.template is
required` for an empty target and reports unknown role-target mapping keys at
YAML load time. It warns (rather than fails) when a profile maps a role that
is not declared in `delegation.roles`; no such warnings are emitted when the
`roles` map is absent.

Effort is harness-specific:

| Harness | Allowed effort |
| --- | --- |
| Claude | `low`, `medium`, `high`, `xhigh`, `max` |
| Codex | `minimal`, `low`, `medium`, `high`, `xhigh` |
| OpenCode | Any non-empty value using letters, digits, `.`, `_`, or `-` |

For a harness without effort support, a non-empty effort fails validation with
`effort not supported by harness <name>`.

## Agent guidance and reloads

When both `delegation` and `web.enabled: true` are configured, Leo adds the
declared roles and their `use_for` text to the built-in system guidance for
managed agents and tasks. The block deliberately contains no profile routing,
template, model, or effort values, so changing the active profile does not
change the guidance. One-off dispatches and consults do **not** receive this
block.

Daemon config reload replaces an existing managed OpenCode agent's `AGENTS.md`
block. Existing Claude and Codex agents need a restart to receive changed role
guidance. `leo delegation use` reloads a running daemon after saving; if the
daemon is not running, the new profile applies when it starts.

## CLI, MCP, and web UI

Use the local CLI to inspect and change policy:

```console
leo delegation list
leo delegation show [profile]
leo delegation render
leo delegation resolve <role>
leo delegation use <profile>
leo dispatch run --role implement --effort high "Add parser tests"
```

The delegation commands are unavailable in remote-client mode. `render` prints
the exact injected block. `use` validates and saves the configuration, prints
warnings, then reloads a running daemon.

Managed agents also have the read-only `leo_delegation` MCP tool, which shows
the active profile's routing. `leo_dispatch` accepts exactly one of `template`
or `role`, plus optional `model` and `effort`; see [Dispatches](dispatches.md).
The web dashboard's **Delegation** page edits roles, profiles, mappings, and
the active profile, shows switch diffs, warnings, and recent role dispatches.

## Permissions

For a role dispatch, `permissions.can_consult` is checked against the
**resolved template**, not the role name. Grant the template that the active
profile routes to; switching profiles can therefore change whether a caller
is permitted to dispatch that role.
