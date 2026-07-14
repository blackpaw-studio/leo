# Rename a Template via the Web UI

**Date:** 2026-07-14
**Status:** Approved for planning

## Goal

Let a user rename an agent template from the web UI. Renaming must cascade so
that nothing is left pointing at a name that no longer exists.

## Background

Templates are stored as a YAML map keyed by name: `Config.Templates
map[string]TemplateConfig` (`internal/config/config.go`). The name is not a
free-floating label — it is referenced from several places:

- **Tasks:** `TaskConfig.Template` (persistent tasks target a template by name).
  Config validation rejects a task whose `template:` points at a missing
  template, so a bare re-key would fail `Validate()` for any referencing task.
- **Agentstore records:** `agentstore.Record.Template` records which template an
  agent was spawned from (`~/.leo/state/agents.json`).
- **Agent identity:** running/suspended agents embed the template name in their
  own name and tmux session (`leo-<template>-<owner>-<repo>`). This is fixed at
  spawn time and is the agent's identity.

The web UI already has an established rename pattern for agents
(`POST /web/agent/{name}/rename` → `handleWebAgentRename`) and template
add/delete handlers (`handleTemplateAdd`, `handleTemplateDelete`) that mutate the
loaded config and call `validateAndSave`. This feature follows those patterns.

## Decisions

1. **Cascade updates** (not block, not warn): a rename rewrites every reference
   it safely can in the same operation.
2. **Agents: update the pointer only.** Each agentstore `Record.Template` is
   moved to the new name. The agent's own name and tmux session keep their
   spawn-time identity. Renaming live agent identities is out of scope — far
   larger and riskier, and not required for correctness.
3. **Rename control lives on the template edit page**, next to where Delete
   already lives.
4. **Web-only.** No `leo template rename` CLI command in this change. The config
   helper is reusable, so a CLI command is an easy follow-up.

## Design

### 1. Config-level rename (pure, unit-testable)

New helper in `internal/config`:

```go
func RenameTemplate(cfg *Config, oldName, newName string) error
```

Behavior:

- Error if `newName` is empty.
- Error if `cfg.Templates[oldName]` does not exist.
- Error if `cfg.Templates[newName]` already exists (collision).
- Re-key: `cfg.Templates[newName] = cfg.Templates[oldName]`; delete old key.
- Rewrite references: for every task in `cfg.Tasks` whose `Template == oldName`,
  set `Template = newName`.

Name-shape validation (letters/digits/dot/underscore/dash) stays in the web
handler via `validEntityName` — the `config` package must not import `web`, and
`validateAndSave` → `Validate()` is the backstop for a malformed map key.

Same-name (`old == new`) is handled at the handler as a no-op redirect and is
not this helper's concern; if called with equal names it returns the collision
error, which the handler avoids by short-circuiting first.

This is a pure map/struct mutation with no disk or agentstore side effects,
matching how `handleTemplateAdd`/`handleTemplateDelete` mutate the loaded `cfg`
before `validateAndSave`.

### 2. Agentstore pointer cascade (best-effort)

After the config save succeeds, load persisted records and, for each record with
`Template == oldName`, call the existing
`agentstore.Update(homePath, name, func(Record) Record)` to set `Template =
newName`. Failures are logged, not fatal — consistent with the best-effort
persistence convention already used for agentstore saves in the agent manager.
All persisted records are considered regardless of live/suspended state.

**Known cosmetic quirk (documented, not fixed):** an agent spawned from the old
template keeps its `leo-<oldName>-...` name and tmux session. Only its
`Template` pointer moves. This is intentional per decision 2.

### 3. Web handler + route

Route (registered in `internal/web/web.go` alongside the other template routes):

```
POST /web/template/{name}/rename → handleTemplateRename
```

Handler flow:

1. Read `new_name` from the form. Reject empty.
2. If `new_name == name`, redirect to the edit page unchanged (no-op).
3. Validate `new_name` with `validEntityName`; reject with `entityNameError`.
4. Load config. Reject if `new_name` already exists (collision) — surfaced as a
   clear flash before calling the helper.
5. `config.RenameTemplate(cfg, name, newName)`.
6. `validateAndSave(cfg)` — full `Validate()` now passes because task refs were
   rewritten.
7. Agentstore pointer cascade (best-effort, section 2).
8. `reloadConfigOrWarn()`.
9. **Success:** set `HX-Redirect: /config/templates/{url.PathEscape(newName)}`
   and return 200. The current page URL still holds the old name, so a redirect
   is required.
10. **Error paths:** `renderFlashToContainer(w, "error", …)`, mirroring
    `handleWebAgentRename`'s error convention (retarget `#flash-container`,
    innerHTML, 200 status).

### 4. UI

`internal/web/templates/pages/template_edit.html` currently renders only the
breadcrumb and the shared `config_form`. Add a small rename form as its own card,
sibling to the config form:

- Text input `new_name`, pre-filled with `.Data.Name`.
- A "Rename" submit button.
- `hx-post="/web/template/{{.Data.Name}}/rename"`.

The generic `config_form` component is left untouched — rename is
template-specific and does not belong in a shared component.

## Testing

- **`config.RenameTemplate`** (table-driven, `internal/config`):
  - Happy path: re-keys the template and rewrites a referencing task's
    `Template` field; unrelated tasks and templates are unchanged.
  - Old name missing → error.
  - New name already exists → error.
  - Empty new name → error.
- **Handler** (`internal/web`, mirroring `TestWebAgentRename*`):
  - Success: `HX-Redirect` points at the new edit URL; config on disk has the
    template re-keyed and task refs updated.
  - Collision: flash error, config unchanged.
  - Invalid name: flash error, config unchanged.

## Out of scope

- Renaming live agent identities (tmux sessions, agentstore keys).
- A `leo template rename` CLI command.
