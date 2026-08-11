# Template permissions

Per-template control over which Leo MCP tools an agent may use, and which
agents/templates it may message, spawn, or consult.

## Motivation

Every agent Leo spawns gets the full 13-tool `leo` MCP surface: it can spawn
fleets, stop its siblings, toggle scheduled tasks, and message anyone. There is
no way to say "this scout template reads and reports, it does not run the
cluster."

Today the only lever is claude's `harness_options.disallowed_tools`, which is
claude-only and works on raw tool-name strings. This spec adds a
harness-agnostic, semantic surface on the template itself.

## Non-goals

This is a **guardrail, not a security boundary.** Enforcement happens inside the
agent's own `leo mcp-server` process; agents run as the same UID and hold the
shared `LEO_API_TOKEN`, so an agent that wants to bypass the check can `curl`
the daemon directly. The goal is shaping what an agent can reach for, not
containing a hostile one. Real enforcement needs per-agent bearer tokens and
daemon-side authorization — a separate change this spec deliberately leaves out,
and whose config surface would be identical to the one defined here.

Also out of scope:

- **Oneshot tasks and consultant subagents.** Both build their own `LaunchSpec`
  (`internal/run/runner.go`, `internal/consult/consult.go:136`) rather than
  going through template launch resolution. Permissions do not apply to them.
  Persistent tasks that name a `template:` *do* inherit that template's
  permissions, because they spawn through the normal agent path.
- **Inbound policy** ("who may message *me*"). That can only be enforced by the
  daemon, which cannot authenticate the caller today.

## Config surface

```yaml
templates:
  scout:
    permissions:
      deny_tools:  [leo_spawn_agent, leo_stop_agent, leo_toggle_task]
      can_message: [rocket, olympus, "scout-*"]
      can_spawn:   [codex]
      can_consult: [fable, opus]
```

`permissions` is optional. Omitting it — or omitting any individual key — is
exactly today's behavior: unrestricted.

### Semantics

| Key | Meaning |
| --- | --- |
| `deny_tools` | Tool names removed from this agent's MCP surface. |
| `can_message` | Allowed `to` targets for `leo_send_message`. |
| `can_spawn` | Allowed `template` values for `leo_spawn_agent`. |
| `can_consult` | Allowed `template` values for `leo_consult`. |

**An absent or empty allowlist means unrestricted.** Total denial is expressed
by denying the tool, not by an empty list:

```yaml
permissions:
  deny_tools: [leo_send_message]   # cannot message anyone
```

This collapses the absent-vs-empty ambiguity that YAML/JSON `omitempty` would
otherwise silently erase on a config round-trip through the web UI.

**Matching** is exact and case-sensitive, with `*`/`?` glob support via
`path.Match` so generated agent names are addressable (`scout-*` covers
`scout-leo`, `scout-olympus`). A pattern that fails to compile never matches.

`leo_send_message` accepts shorthand names that the daemon resolves; the
permission check runs against the *literal* argument before that resolution. An
allowlist of `[rocket]` therefore rejects `to: "rock"`. Fail-closed, with an
error naming the allowed targets.

**`leo_skill` cannot be denied.** `Validate()` rejects it in `deny_tools`. The
system-context nudge (`leomcp.LeoNudge`) unconditionally tells every agent to
call `leo_skill`, and a template that could contradict that nudge would produce
a confusing dead end.

## Architecture

### New package: `internal/leotools`

A leaf package — no Leo imports — holding the shared vocabulary. It exists
because `internal/mcp` transitively imports `internal/config` (via
`internal/consult`), so `config` cannot import `mcp` to validate tool names.

```go
package leotools

// Names is the canonical, ordered list of leo MCP tool names.
var Names = []string{"leo_skill", "leo_clear", /* ... */}

// Permissions is the per-template permission set. Zero value = unrestricted.
type Permissions struct {
    DenyTools  []string `yaml:"deny_tools,omitempty"  json:"deny_tools,omitempty"`
    CanMessage []string `yaml:"can_message,omitempty" json:"can_message,omitempty"`
    CanSpawn   []string `yaml:"can_spawn,omitempty"   json:"can_spawn,omitempty"`
    CanConsult []string `yaml:"can_consult,omitempty" json:"can_consult,omitempty"`
}

func (p Permissions) IsZero() bool
func (p Permissions) DeniesTool(name string) bool
func (p Permissions) AllowsMessage(target string) bool
func (p Permissions) AllowsSpawn(template string) bool
func (p Permissions) AllowsConsult(template string) bool
```

A test in `internal/mcp` asserts `Names` equals the registry's actual tool names
exactly, in both directions, so the list cannot drift from the definitions.

### Config

`config.TemplateConfig` gains a `Permissions leotools.Permissions` field tagged
`yaml:"permissions,omitempty"`.

`Config.Validate()` adds, per template:

- every `deny_tools` entry must be in `leotools.Names`; unknown names error with
  the valid list (a typo that silently grants full access is the failure mode
  this prevents);
- `leo_skill` in `deny_tools` is rejected explicitly;
- `can_spawn` / `can_consult` entries containing no glob metacharacter must name
  a defined template; glob entries are accepted unchecked;
- `can_message` is not checked — agent names are dynamic.

### Transport: `LEO_PERMISSIONS`

The resolved `Permissions` is marshalled to compact JSON and exported as
`LEO_PERMISSIONS` in the agent's environment. When a template has no
permissions block the variable is omitted entirely, so unrestricted agents see
byte-for-byte today's environment.

The single injection seam is **`agent.BuildTemplateArgs`**, which already takes
`tmpl` and already returns the harness env overlay that every spawn path merges
as its base layer (`internal/agent/manager.go:357`, `:630`, `:1218`, `:1230`).
Folding the variable in there covers spawn, resume, and restart uniformly — and
means restart re-resolves permissions from current config, which is the desired
behavior.

Per-harness plumbing in `internal/agent/args.go`:

| Harness | Change |
| --- | --- |
| `claude` | None beyond the env overlay — `leo mcp-server` is a child of `claude` and inherits the process environment. |
| `codex` | Append `"LEO_PERMISSIONS"` to `LeoMCPBridge.EnvVars` (a forward-list of names). |
| `opencode` | Add the key to `LeoMCPBridge.Env` (an explicit map). |

**Permissions are fixed at process start.** Editing a template's permissions
requires restarting agents spawned from it. Documented, not worked around.

### Enforcement: `internal/mcp`

`registryFromEnv` parses `LEO_PERMISSIONS` and passes the result to
`newRegistry(client, processName, perms)`.

- **Malformed `LEO_PERMISSIONS` → local-only mode.** A warning to stderr and
  only `leo_skill` is registered. Fail-closed and loud, reusing the existing
  degraded mode rather than bricking the agent or silently running
  unrestricted.
- **Denied tools are never registered.** `registry.add` / `addContext` skip
  them and record the name in a `denied` set. That removes them from
  `tools/list` *and* from the handler map in one move — a model that calls a
  hidden tool from memory still gets rejected. `callContext` checks `denied`
  before its unknown-tool fallback so the error reads
  `tool "leo_spawn_agent" is not permitted for this agent` rather than
  `unknown tool`.
- **Allowlists are checked in the handler**, before the daemon call:

  ```
  not permitted to message "leo"; allowed targets: rocket, olympus, scout-*
  ```

- **Narrowed tools advertise their limits.** When an allowlist is set, its
  values are appended to the tool description (`You may only message: rocket,
  olympus.`) so the model does not have to discover the boundary by failing.

### Web UI

`permissions` is a nested struct, and `internal/web/schema`'s registry is flat —
`registry_drift_test.go` fails on any unregistered config field, so this needs a
decision either way.

Treatment follows the existing `harness_options` pattern: `permissions` is added
to `Excluded[SectionTemplate]` (excluded from the flat registry, has its own
UI), and a new `SectionPermissions` is registered with four `KindCSV` fields
over `leotools.Permissions`, rendered as a sub-form in the template page.

This is the last implementation step. If it grows beyond a section registration
plus a template partial, drop it and ship config-file-only — but say so, don't
silently skip it.

### Docs

- New `docs/configuration/permissions.md` — surface, semantics, the
  guardrail-not-boundary caveat, the restart-to-apply note.
- Links from `docs/configuration/index.md` and an entry in
  `docs/configuration/config-reference.md`.

## Testing

Test-first, per repo discipline.

**`internal/leotools`** — exact match; glob match; absent and empty allowlists
both unrestricted; malformed glob never matches; `IsZero`; JSON round-trip
preserves every field.

**`internal/mcp`** — denied tool absent from `tools/list`; calling it returns
the not-permitted error, not `unknown tool`; allowlist rejection text for
message/spawn/consult; allowed values reach the daemon client unchanged;
description suffix appears only when narrowed; malformed `LEO_PERMISSIONS`
yields local-only mode; **registry names match `leotools.Names` exactly, both
directions.**

**`internal/agent`** — `LEO_PERMISSIONS` present and correct in the env returned
by `BuildTemplateArgs` for each harness; absent when the template has no
permissions; codex `EnvVars` carries the name; a restart re-resolves it from
current config.

**`internal/config`** — unknown `deny_tools` name rejected with the valid list;
`leo_skill` in `deny_tools` rejected; `can_spawn`/`can_consult` naming an
undefined template rejected; glob entries accepted; empty `permissions` block
valid.

**`internal/web/schema`** — drift test passes; `SectionPermissions` round-trips
values.

## Acceptance

1. A template with `deny_tools: [leo_spawn_agent]` spawns an agent whose
   `tools/list` omits `leo_spawn_agent`, and calling it anyway returns the
   not-permitted error.
2. A template with `can_message: [rocket]` can message `rocket` and is refused
   for any other target, with the allowed list in the error.
3. A template with no `permissions` block produces a byte-identical environment
   and tool surface to today.
4. `make test` and `make lint` pass; the schema drift test passes.
